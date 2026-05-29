package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const githubAPI = "https://api.github.com"

// ── GitHub client ────────────────────────────────────────────────────

var httpClient = &http.Client{}

func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	// Try gh CLI
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func githubGET(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if token := githubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	return httpClient.Do(req)
}

// ── GitHub tree / commit SHA ─────────────────────────────────────────

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type treeResponse struct {
	SHA  string      `json:"sha"`
	Tree []treeEntry `json:"tree"`
}

type commitResponse struct {
	SHA string `json:"sha"`
}

func fetchLatestCommit(repo, ref string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/commits/%s?per_page=1", githubAPI, repo, ref)
	resp, err := githubGET(url)
	if err != nil {
		return "", fmt.Errorf("fetch commit for %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch commit for %s: HTTP %d", repo, resp.StatusCode)
	}
	var cr commitResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", fmt.Errorf("decode commit: %w", err)
	}
	return cr.SHA, nil
}

func fetchTree(repo, ref string) ([]treeEntry, error) {
	url := fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", githubAPI, repo, ref)
	resp, err := githubGET(url)
	if err != nil {
		return nil, fmt.Errorf("fetch tree for %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch tree for %s: HTTP %d", repo, resp.StatusCode)
	}
	var tr treeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode tree: %w", err)
	}
	return tr.Tree, nil
}

type contentResponse struct {
	Encoding    string `json:"encoding"`
	Content     string `json:"content"`
	Type        string `json:"type"`
	Target      string `json:"target,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

func downloadFile(repo, ref, filePath string) ([]byte, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", githubAPI, repo, filePath, ref)
	resp, err := githubGET(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", filePath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch %s: HTTP %d", filePath, resp.StatusCode)
	}

	var content contentResponse
	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filePath, err)
	}

	// Handle symlinks — use download_url directly
	if content.Type == "symlink" && content.DownloadURL != "" {
		dresp, err := http.Get(content.DownloadURL)
		if err != nil {
			return nil, fmt.Errorf("download symlink %s: %w", filePath, err)
		}
		defer dresp.Body.Close()
		return io.ReadAll(dresp.Body)
	}

	if content.Encoding != "base64" {
		return nil, fmt.Errorf("unexpected encoding %s for %s", content.Encoding, filePath)
	}

	return base64.StdEncoding.DecodeString(content.Content)
}

// ── install ──────────────────────────────────────────────────────────

// InstallResult reports what happened with a single skill.
type InstallResult struct {
	Name   string
	Action string // "ok", "updated", "failed"
	Error  string
}

func InstallSkill(skill SkillEntry, destDir string, currentCommit string) InstallResult {
	r := InstallResult{Name: skill.Name}

	// Ensure destination exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		r.Action = "failed"
		r.Error = fmt.Sprintf("mkdir: %v", err)
		return r
	}

	repo := skill.Source.Repo
	ref := skill.Source.Ref
	if ref == "" {
		ref = "main"
	}
	prefix := skill.Source.Path

	tree, err := fetchTree(repo, ref)
	if err != nil {
		r.Action = "failed"
		r.Error = fmt.Sprintf("tree: %v", err)
		return r
	}

	// Filter entries under the prefix
	prefixSlash := prefix + "/"
	files := 0
	failed := 0

	for _, entry := range tree {
		if entry.Type != "blob" {
			continue
		}
		if !strings.HasPrefix(entry.Path, prefixSlash) && entry.Path != prefix {
			continue
		}
		if entry.Path == prefix {
			// skip the directory entry itself if it's listed as a blob
			continue
		}

		relPath := strings.TrimPrefix(entry.Path, prefixSlash)
		if relPath == "" {
			continue
		}

		localPath := filepath.Join(destDir, relPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			failed++
			continue
		}

		data, err := downloadFile(repo, ref, entry.Path)
		if err != nil {
			failed++
			continue
		}
		if err := os.WriteFile(localPath, data, 0644); err != nil {
			failed++
			continue
		}
		// Set executable bit if mode is 100755
		if entry.Mode == "100755" {
			os.Chmod(localPath, 0755)
		}
		files++
	}

	if files == 0 && failed == 0 {
		r.Action = "ok"
		r.Error = "already installed"
		return r
	}

	if failed > 0 {
		r.Action = "failed"
		r.Error = fmt.Sprintf("%d files failed, %d ok", failed, files)
	} else {
		r.Action = "ok"
	}

	return r
}

// InstallAll installs or updates all skills from the manifest.
func InstallAll(m *Manifest, lock *LockFile, manifestPath string) []InstallResult {
	var results []InstallResult

	for _, skill := range m.Skills {
		targetPath := resolveTargetPath(skill.Target, m.Directories)
		if targetPath == "" {
			results = append(results, InstallResult{
				Name:   skill.Name,
				Action: "failed",
				Error:  fmt.Sprintf("unknown target %q", skill.Target),
			})
			continue
		}
		destDir := filepath.Join(targetPath, skill.Name)
		destDir = expandPath(destDir)

		// Check lock for current commit
		ls, hasLock := lock.Skills[skill.Name]
		lockedCommit := ""
		if hasLock {
			lockedCommit = ls.Commit
		}

		// Fetch latest commit to compare
		latestCommit, err := fetchLatestCommit(skill.Source.Repo, skill.Source.Ref)
		if err != nil {
			results = append(results, InstallResult{
				Name:   skill.Name,
				Action: "failed",
				Error:  fmt.Sprintf("check commit: %v", err),
			})
			continue
		}

		if hasLock && lockedCommit == latestCommit {
			// Check if SKILL.md actually exists on disk
			if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err == nil {
				results = append(results, InstallResult{
					Name:   skill.Name,
					Action: "ok",
					Error:  "already installed",
				})
				continue
			}
		}

		// Install (or update)
		result := InstallSkill(skill, destDir, latestCommit)
		if result.Action == "ok" {
			// Update lock
			lock.Skills[skill.Name] = LockSkill{
				Commit: latestCommit,
				Path:   skill.Source.Path,
			}
		}
		results = append(results, result)
	}

	// Write lock
	lock.Updated = time.Now().Format(time.RFC3339)
	if err := writeLock(getLockPath(manifestPath), lock); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write lock: %v\n", err)
	}

	return results
}

func getLockPath(manifestPath string) string {
	// Default: same dir as manifest, .lock.json
	dir := filepath.Dir(manifestPath)
	return filepath.Join(dir, ".lock.json")
}
