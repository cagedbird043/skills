package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `skills — Agent skill manager  v%s

Usage:
  skills <command> [options]

Commands:
  list              List all managed skills with status
  install [name]    Install all skills, or a specific one
  status [name]     Check skill(s) against manifest and lock
  verify            Verify skill directories exist on disk

Options:
  --manifest <path>  Path to .manifest.json (default: auto-detect)
  -m <path>          Same as --manifest
  --version          Print version

Environment:
  SKILLS_MANIFEST    Path to manifest (alternative to --manifest)

Default manifest search order:
  1. --manifest / SKILLS_MANIFEST
  2. $PWD/.manifest.json
  3. $PWD/.skills.json
  4. ~/.config/skills/.manifest.json
`, version)
}

func findManifest(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("SKILLS_MANIFEST"); env != "" {
		return env
	}

	candidates := []string{
		filepath.Join(".", ".manifest.json"),
		filepath.Join(".", ".skills.json"),
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "skills", ".manifest.json"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func main() {
	// Parse flags and subcommand
	manifestPath := ""
	var positional []string

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--manifest" || arg == "-m" {
			if i+1 < len(os.Args) {
				manifestPath = os.Args[i+1]
				i++
				continue
			}
			fmt.Fprintln(os.Stderr, "skills: --manifest requires a path")
			os.Exit(1)
		}
		if arg == "--version" {
			fmt.Println("skills", version)
			return
		}
		if arg == "--help" || arg == "-h" {
			usage()
			return
		}
		positional = append(positional, arg)
	}

	if len(positional) < 1 {
		usage()
		os.Exit(1)
	}

	if manifestPath == "" {
		manifestPath = findManifest("")
	}
	if manifestPath == "" {
		fmt.Fprintln(os.Stderr, "skills: no manifest found. Use --manifest or set SKILLS_MANIFEST.")
		os.Exit(1)
	}

	subcmd := positional[0]

	// Read manifest
	m, err := readManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills: %v\n", err)
		os.Exit(1)
	}

	// Read lock
	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills: warning: %v\n", err)
	}

	switch subcmd {
	case "list":
		cmdList(m, lock)

	case "install":
		target := ""
		if len(positional) > 1 {
			target = positional[1]
		}
		cmdInstall(m, lock, manifestPath, target)

	case "status":
		target := ""
		if len(positional) > 1 {
			target = positional[1]
		}
		cmdStatus(m, lock, target)

	case "verify":
		cmdVerify(m)

	default:
		fmt.Fprintf(os.Stderr, "skills: unknown command %q\n", subcmd)
		usage()
		os.Exit(1)
	}
}

// ── commands ─────────────────────────────────────────────────────────

func cmdList(m *Manifest, lock *LockFile) {
	// Build name→target map
	dirNames := make(map[string]string)
	for _, d := range m.Directories {
		dirNames[d.Name] = d.Path
	}

	fmt.Printf("%-24s %-12s %s\n", "SKILL", "TARGET", "STATUS")
	fmt.Println(strings.Repeat("─", 56))
	for _, s := range m.Skills {
		dirPath := dirNames[s.Target]
		diskPath := filepath.Join(expandPath(dirPath), s.Name)

		status := "─"
		if _, err := os.Stat(filepath.Join(diskPath, "SKILL.md")); err == nil {
			if ls, ok := lock.Skills[s.Name]; ok && ls.Commit != "" {
				status = "✓ " + ls.Commit[:8]
			} else {
				status = "✓ unknown commit"
			}
		} else {
			status = "✗ not installed"
		}

		targetLabel := s.Target
		// Truncate if needed
		if len(targetLabel) > 11 {
			targetLabel = targetLabel[:11]
		}

		fmt.Printf("  %-22s %-12s %s\n", s.Name, targetLabel, status)
	}
}

func cmdInstall(m *Manifest, lock *LockFile, manifestPath, target string) {
	if target != "" {
		// Single skill install
		var found *SkillEntry
		for _, s := range m.Skills {
			if s.Name == target {
				found = &s
				break
			}
		}
		if found == nil {
			fmt.Fprintf(os.Stderr, "skills: skill %q not found in manifest\n", target)
			os.Exit(1)
		}

		targetPath := resolveTargetPath(found.Target, m.Directories)
		if targetPath == "" {
			fmt.Fprintf(os.Stderr, "skills: unknown target %q for %s\n", found.Target, found.Name)
			os.Exit(1)
		}

		destDir := filepath.Join(expandPath(targetPath), found.Name)

		latestCommit, err := fetchLatestCommit(found.Source.Repo, found.Source.Ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skills: %s: %v\n", found.Name, err)
			os.Exit(1)
		}

		// Check lock
		if ls, ok := lock.Skills[found.Name]; ok && ls.Commit == latestCommit {
			if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err == nil {
				fmt.Printf("  ✓ %s (already up to date)\n", found.Name)
				return
			}
		}

		result := InstallSkill(*found, destDir, latestCommit)
		if result.Action == "ok" {
			lock.Skills[found.Name] = LockSkill{Commit: latestCommit, Path: found.Source.Path}
			lock.Updated = getCurrentTime()
			if err := writeLock(getLockPath(manifestPath), lock); err != nil {
				fmt.Fprintf(os.Stderr, "warning: write lock: %v\n", err)
			}
			fmt.Printf("  ✓ %s installed\n", found.Name)
		} else {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", found.Name, result.Error)
		}
		return
	}

	// Install all
	results := InstallAll(m, lock, manifestPath)
	ok, updated, failed := 0, 0, 0
	for _, r := range results {
		switch r.Action {
		case "ok":
			if r.Error == "already installed" {
				ok++
			} else {
				updated++
			}
			fmt.Printf("  ✓ %s\n", r.Name)
		case "updated":
			updated++
			fmt.Printf("  ✓ %s (updated)\n", r.Name)
		case "failed":
			failed++
			fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", r.Name, r.Error)
		}
	}
	fmt.Println()
	fmt.Printf("  ✓ %d up to date, %d installed, %d failed\n", ok, updated, failed)
}

func cmdStatus(m *Manifest, lock *LockFile, target string) {
	_ = lock
	_ = target
	fmt.Println("status: not yet implemented")
}

func cmdVerify(m *Manifest) {
	bad := 0
	for _, s := range m.Skills {
		targetPath := resolveTargetPath(s.Target, m.Directories)
		if targetPath == "" {
			fmt.Fprintf(os.Stderr, "  ✗ %s: unknown target %q\n", s.Name, s.Target)
			bad++
			continue
		}
		skillDir := filepath.Join(expandPath(targetPath), s.Name)
		sm := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(sm); err == nil {
			fmt.Printf("  ✓ %s\n", s.Name)
		} else {
			fmt.Fprintf(os.Stderr, "  ✗ %s: SKILL.md not found at %s\n", s.Name, sm)
			bad++
		}
	}
	if bad > 0 {
		fmt.Printf("\n  %d skill(s) missing\n", bad)
	} else {
		fmt.Println("\n  All skills present.")
	}
}

func getCurrentTime() string {
	t := timeNow()
	return t.Format("2006-01-02T15:04:05+08:00")
}

// timeNow is a variable so tests can override it
var timeNow = func() time.Time {
	return time.Now()
}
