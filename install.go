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
	"sync"
	"time"
)

const githubAPI = "https://api.github.com"

// ── GitHub client ────────────────────────────────────────────────────

var httpClient = &http.Client{}

func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	// Try gh CLI — search common locations
	ghPaths := []string{"gh", "/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/home/linuxbrew/.linuxbrew/bin/gh"}
	for _, p := range ghPaths {
		cmd := exec.Command(p, "auth", "token")
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
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
		req, err := http.NewRequest("GET", content.DownloadURL, nil)
		if err != nil {
			return nil, fmt.Errorf("symlink request %s: %w", filePath, err)
		}
		if token := githubToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		dresp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("download symlink %s: %w", filePath, err)
		}
		defer dresp.Body.Close()
		if dresp.StatusCode != 200 {
			return nil, fmt.Errorf("download symlink %s: HTTP %d", filePath, dresp.StatusCode)
		}
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

func InstallSkill(skill SkillEntry, destDir string, refOverride string) InstallResult {
	r := InstallResult{Name: skill.Name}

	repo := skill.Source.Repo
	ref := skill.Source.Ref
	if ref == "" {
		ref = "main"
	}
	if refOverride != "" {
		ref = refOverride
	}
	prefix := skill.Source.Path

	tree, err := fetchTree(repo, ref)
	if err != nil {
		r.Action = "failed"
		r.Error = fmt.Sprintf("tree: %v", err)
		return r
	}

	// Download to temp directory for atomic install
	tmpDir, err := os.MkdirTemp("", "skills-"+skill.Name)
	if err != nil {
		r.Action = "failed"
		r.Error = fmt.Sprintf("temp dir: %v", err)
		return r
	}
	defer os.RemoveAll(tmpDir)

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
			continue
		}

		relPath := strings.TrimPrefix(entry.Path, prefixSlash)
		if relPath == "" {
			continue
		}

		localPath := filepath.Join(tmpDir, relPath)
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

	// Verify SKILL.md exists before committing
	if _, err := os.Stat(filepath.Join(tmpDir, "SKILL.md")); err != nil {
		r.Action = "failed"
		r.Error = fmt.Sprintf("SKILL.md missing after download (%d files ok, %d failed)", files, failed)
		return r
	}

	// Atomic replace — remove old, move new
	os.RemoveAll(destDir)
	if err := os.Rename(tmpDir, destDir); err != nil {
		r.Action = "failed"
		r.Error = fmt.Sprintf("rename to %s: %v", destDir, err)
		return r
	}
	tmpDir = "" // prevent defer RemoveAll

	if failed > 0 {
		r.Action = "failed"
		r.Error = fmt.Sprintf("%d files failed, %d ok", failed, files)
	} else {
		r.Action = "ok"
	}

	return r
}

// installOneSkill installs a skill trusting the lock file (no remote commit check).
// If locked and SKILL.md exists → skip (0 API calls).
// If locked but SKILL.md missing → re-download (1 tree API call).
// If not locked → download latest + fetch commit (2 API calls), record real commit.
func installOneSkill(skill SkillEntry, lock *LockFile, dirs []DirEntry) (InstallResult, *LockSkill) {
	targetPath := resolveTargetPath(skill.Target, dirs)
	if targetPath == "" {
		return InstallResult{Name: skill.Name, Action: "failed", Error: fmt.Sprintf("unknown target %q", skill.Target)}, nil
	}
	destDir := filepath.Join(expandPath(targetPath), skill.Name)

	ls, hasLock := lock.Skills[skill.Name]

	// Locked and on disk → skip
	if hasLock {
		if ls.Commit == "" {
			// Lock exists but commit is empty — stale lock from older version
			// Fetch commit to fill it
			if commit, err := fetchLatestCommit(skill.Source.Repo, skill.Source.Ref); err == nil {
				return InstallResult{Name: skill.Name, Action: "ok", Error: "already installed"},
					&LockSkill{Commit: commit, Path: skill.Source.Path}
			}
		} else if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err == nil {
			return InstallResult{Name: skill.Name, Action: "ok", Error: "already installed"}, nil
		}
	}

	// Fetch commit first so we can use it as ref (avoid branch race)
	commit, err := fetchLatestCommit(skill.Source.Repo, skill.Source.Ref)
	if err != nil {
		// Commit fetch failed — still try download with branch ref
		result := InstallSkill(skill, destDir, "")
		if result.Action == "ok" {
			result.Error = "installed (commit not recorded)"
		}
		return result, nil
	}

	result := InstallSkill(skill, destDir, commit)
	if result.Action == "ok" {
		return result, &LockSkill{Commit: commit, Path: skill.Source.Path}
	}
	return result, nil
}

// updateOneSkill checks remote commit vs lock, installs if newer.
func updateOneSkill(skill SkillEntry, lock *LockFile, dirs []DirEntry) (InstallResult, *LockSkill) {
	targetPath := resolveTargetPath(skill.Target, dirs)
	if targetPath == "" {
		return InstallResult{Name: skill.Name, Action: "failed", Error: fmt.Sprintf("unknown target %q", skill.Target)}, nil
	}
	destDir := filepath.Join(expandPath(targetPath), skill.Name)

	ls, hasLock := lock.Skills[skill.Name]
	lockedCommit := ""
	if hasLock {
		lockedCommit = ls.Commit
	}

	latestCommit, err := fetchLatestCommit(skill.Source.Repo, skill.Source.Ref)
	if err != nil {
		msg := fmt.Sprintf("check commit: %v", err)
		if isRateLimit(err) {
			msg += " — set GITHUB_TOKEN or install gh for higher rate limits"
		}
		return InstallResult{Name: skill.Name, Action: "failed", Error: msg}, nil
	}

	if hasLock && lockedCommit == latestCommit {
		if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err == nil {
			return InstallResult{Name: skill.Name, Action: "ok", Error: "already installed"}, nil
		}
	}

	// Use commit SHA as ref to avoid branch race
	result := InstallSkill(skill, destDir, latestCommit)
	if result.Action == "ok" {
		return result, &LockSkill{Commit: latestCommit, Path: skill.Source.Path}
	}
	return result, nil
}

func isRateLimit(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "rate limit"))
}

// runParallel executes a skill processing function across all skills in parallel.
func runParallel(m *Manifest, lock *LockFile, manifestPath string, fn func(SkillEntry, *LockFile, []DirEntry) (InstallResult, *LockSkill)) []InstallResult {
	type jobResult struct {
		InstallResult
		lockUpdate *LockSkill
		name       string
	}

	n := len(m.Skills)
	jobs := make(chan SkillEntry, n)
	results := make(chan jobResult, n)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for skill := range jobs {
				r, ls := fn(skill, lock, m.Directories)
				results <- jobResult{InstallResult: r, lockUpdate: ls, name: skill.Name}
			}
		}()
	}

	for _, skill := range m.Skills {
		jobs <- skill
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	var allResults []InstallResult
	for r := range results {
		if r.lockUpdate != nil {
			lock.Skills[r.name] = *r.lockUpdate
		}
		allResults = append(allResults, r.InstallResult)
	}

	// Apply manifest symlinks
	applySymlinks(m)

	lock.Updated = time.Now().Format(time.RFC3339)
	if err := writeLock(getLockPath(manifestPath), lock); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write lock: %v\n", err)
	}

	return allResults
}

// InstallAll installs all skills trusting the lock file (no remote commit checks).
func InstallAll(m *Manifest, lock *LockFile, manifestPath string) []InstallResult {
	return runParallel(m, lock, manifestPath, installOneSkill)
}

// UpdateAll checks remote commits and updates skills that have changed.
func UpdateAll(m *Manifest, lock *LockFile, manifestPath string) []InstallResult {
	return runParallel(m, lock, manifestPath, updateOneSkill)
}

// applySymlinks creates all symlinks declared in the manifest.
func applySymlinks(m *Manifest) {
	for _, sym := range m.Symlinks {
		from := expandPath(sym.From)
		to := expandPath(sym.To)

		// Check if symlink already points to the right place
		if existing, err := os.Readlink(from); err == nil && existing == to {
			continue
		}

		// Remove any existing file/symlink
		if err := os.RemoveAll(from); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: remove %s: %v\n", from, err)
			continue
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(from), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: mkdir %s: %v\n", filepath.Dir(from), err)
			continue
		}

		if err := os.Symlink(to, from); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: symlink %s -> %s: %v\n", from, to, err)
		}
	}
}

func getLockPath(manifestPath string) string {
	// Default: same dir as manifest, .lock.json
	dir := filepath.Dir(manifestPath)
	return filepath.Join(dir, ".lock.json")
}
