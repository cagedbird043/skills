package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const version = "0.4.0"

var (
	quiet bool // -q flag
)

// ── ANSI colors (zero-dependency) ────────────────────────────────────

var useColor = os.Getenv("NO_COLOR") == "" && isTerminal()

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

func green(s string) string {
	if !useColor { return s }
	return "\033[32m" + s + "\033[0m"
}
func red(s string) string {
	if !useColor { return s }
	return "\033[31m" + s + "\033[0m"
}
func yellow(s string) string {
	if !useColor { return s }
	return "\033[33m" + s + "\033[0m"
}
func dim(s string) string {
	if !useColor { return s }
	return "\033[2m" + s + "\033[0m"
}
func bold(s string) string {
	if !useColor { return s }
	return "\033[1m" + s + "\033[0m"
}

// ── CLI ──────────────────────────────────────────────────────────────

func usage() {
	fmt.Fprintf(os.Stderr, `%s — Agent skill manager  v%s

%s:
  skills <command> [options]

%s:
  list              List all skills with installation status
  install [name]    Install from lock (no remote check — fast)
  update            Check remote commits, update changed skills
  remove <name>     Remove a skill from manifest, lock, disk, and mirrors
  verify            Check that all skill directories exist
  info <name>       Show details about a specific skill
  completion <shell> Generate shell completion (zsh, bash)

%s:
  -m, --manifest <path>  Path to manifest file
  -q, --quiet            Suppress normal output, show errors only
  -n, --dry-run          Show what would be done without doing it
  -k, --keep-manifest    With remove: keep the manifest entry
      --version          Print version

%s:
  SKILLS_MANIFEST        Path to manifest (alternative to --manifest)
  NO_COLOR               Set to any value to disable colored output

%s:
  skills list
  skills install
  skills install drawio
  skills update
  skills remove drawio
  skills remove drawio --keep-manifest
  skills remove drawio --dry-run
  skills verify
  skills info drawio
  skills completion zsh > ~/.local/share/zsh/site-functions/_skills
`, bold("skills"), version,
		bold("Usage"),
		bold("Commands"),
		bold("Options"),
		bold("Environment"),
		bold("Examples"))
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
	manifestPath := ""
	var positional []string
	dryRun := false
	keepManifest := false

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
		if arg == "-q" || arg == "--quiet" {
			quiet = true
			continue
		}
		if arg == "--version" {
			fmt.Println("skills", version)
			return
		}
		if arg == "--help" || arg == "-h" {
			usage()
			return
		}
		if arg == "-n" || arg == "--dry-run" {
			dryRun = true
			continue
		}
		if arg == "-k" || arg == "--keep-manifest" {
			keepManifest = true
			continue
		}
		positional = append(positional, arg)
	}

	if len(positional) < 1 {
		usage()
		os.Exit(1)
	}

	subcmd := positional[0]

	// Commands that don't need a manifest
	switch subcmd {
	case "completion":
		shell := "zsh"
		if len(positional) > 1 {
			shell = positional[1]
		}
		cmdCompletion(shell)
		return
	}

	if manifestPath == "" {
		manifestPath = findManifest("")
	}
	if manifestPath == "" {
		fmt.Fprintln(os.Stderr, "skills: no manifest found. Use --manifest or set SKILLS_MANIFEST.")
		os.Exit(1)
	}

	m, err := readManifest(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills: %v\n", err)
		os.Exit(1)
	}

	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		warn("lock: %v", err)
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
	case "update":
		target := ""
		if len(positional) > 1 {
			target = positional[1]
		}
		cmdUpdate(m, lock, manifestPath, target, dryRun)
	case "remove":
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "skills: remove requires a skill name")
			os.Exit(1)
		}
		cmdRemove(m, lock, manifestPath, positional[1], keepManifest, dryRun)
	case "verify":
		cmdVerify(m)
	case "info":
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "skills: info requires a skill name")
			os.Exit(1)
		}
		cmdInfo(m, lock, positional[1])
	default:
		fmt.Fprintf(os.Stderr, "skills: unknown command %q\n", subcmd)
		usage()
		os.Exit(1)
	}
}

// ── output helpers ───────────────────────────────────────────────────

func ok(msg string, args ...interface{}) {
	if quiet { return }
	fmt.Printf("  "+green("✓")+" %s\n", fmt.Sprintf(msg, args...))
}

func fail(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "  "+red("✗")+" %s\n", fmt.Sprintf(msg, args...))
}

func warn(msg string, args ...interface{}) {
	if quiet { return }
	fmt.Fprintf(os.Stderr, "  "+yellow("⚠")+" %s\n", fmt.Sprintf(msg, args...))
}

// ── commands ─────────────────────────────────────────────────────────

func cmdCompletion(shell string) {
	switch shell {
	case "zsh":
		fmt.Print(`#compdef skills

_skills() {
  local -a cmds
  cmds=(
    'list:list all skills with status'
    'install:install from lock (fast, no remote check)'
    'update:check remote and update changed skills'
    'verify:check skill directories exist'
    'info:show skill details'
    'completion:generate shell completion'
  )
  _describe -t commands 'skills command' cmds
}

_skills "$@"
`)
	case "bash":
		fmt.Print(`_skills() {
  local cur prev words cword
  _init_completion || return
  COMPREPLY=($(compgen -W "list install update verify info completion" -- "$cur"))
}
complete -F _skills skills
`)
	default:
		fmt.Fprintf(os.Stderr, "skills: unsupported shell %q (supported: zsh, bash)\n", shell)
		os.Exit(1)
	}
}

func cmdList(m *Manifest, lock *LockFile) {
	dirNames := make(map[string]string)
	for _, d := range m.Directories {
		dirNames[d.Name] = d.Path
	}

	if !quiet {
		fmt.Printf("%-24s %-12s %s\n", bold("SKILL"), bold("TARGET"), bold("STATUS"))
		fmt.Println(dim(strings.Repeat("─", 60)))
	}
	for _, s := range m.Skills {
		dirPath := dirNames[s.Target]
		diskPath := filepath.Join(expandPath(dirPath), s.Name)

		var status string
		if _, err := os.Stat(filepath.Join(diskPath, "SKILL.md")); err == nil {
			if ls, ok := lock.Skills[s.Name]; ok && ls.Commit != "" {
				status = green("✓") + " " + ls.Commit[:8]
			} else {
				status = green("✓") + " installed"
			}
		} else {
			status = red("✗") + " not installed"
		}

		if !quiet {
			fmt.Printf("  %-22s %-12s %s\n", s.Name, s.Target, status)
		}
	}
}

func printSummary(results []InstallResult) {
	upToDate, updated, failed := 0, 0, 0
	for _, r := range results {
		switch r.Action {
		case "ok":
			if r.Error == "already installed" {
				upToDate++
			} else {
				updated++
			}
			ok("%s", r.Name)
		case "updated":
			updated++
			ok("%s (updated)", r.Name)
		case "failed":
			failed++
			fail("%s: %s", r.Name, r.Error)
		}
	}
	if !quiet {
		fmt.Println()
		summary := fmt.Sprintf("%d up to date, %d installed, %d failed", upToDate, updated, failed)
		if failed > 0 {
			fmt.Println("  " + yellow(summary))
		} else {
			fmt.Println("  " + green(summary))
		}
	}
}

// cmdInstall trusts the lock file — no remote commit checks.
func cmdInstall(m *Manifest, lock *LockFile, manifestPath, target string) {
	if target != "" {
		var found *SkillEntry
		for _, s := range m.Skills {
			if s.Name == target {
				found = &s
				break
			}
		}
		if found == nil {
			fail("skill %q not found in manifest", target)
			os.Exit(1)
		}

		// Reuse the same logic as bulk install
		r, ls := installOneSkill(*found, lock, m.Directories)
		if ls != nil {
			lock.Skills[found.Name] = *ls
			lock.Updated = time.Now().Format(time.RFC3339)
			writeLock(getLockPath(manifestPath), lock)
		}
		// Apply symlinks + mirrors for single install too
		applySymlinks(m)
		applyMirrors(m)
		if r.Action == "ok" {
			ok("%s", found.Name)
		} else {
			fail("%s: %s", found.Name, r.Error)
		}
		return
	}

	results := InstallAll(m, lock, manifestPath)
	printSummary(results)
}

// cmdUpdate checks remote commits — makes API calls.
func cmdUpdate(m *Manifest, lock *LockFile, manifestPath, target string, dryRun bool) {
	if target != "" {
		var found *SkillEntry
		for _, s := range m.Skills {
			if s.Name == target {
				found = &s
				break
			}
		}
		if found == nil {
			fail("skill %q not found in manifest", target)
			os.Exit(1)
		}

		r, ls := updateOneSkill(*found, lock, m.Directories)
		if ls != nil {
			lock.Skills[found.Name] = *ls
			lock.Updated = time.Now().Format(time.RFC3339)
			writeLock(getLockPath(manifestPath), lock)
		}
		applySymlinks(m)
		applyMirrors(m)
		if r.Action == "ok" {
			ok("%s", found.Name)
		} else {
			fail("%s: %s", found.Name, r.Error)
		}
		return
	}

	results := UpdateAll(m, lock, manifestPath)
	printSummary(results)
}

// cmdRemove removes a skill from lock, manifest, disk, and mirror symlinks.
// Execution order: lock → manifest → disk → applyMirrors (resilient to partial failure).
func cmdRemove(m *Manifest, lock *LockFile, manifestPath, name string, keepManifest, dryRun bool) {
	if err := validateSkillName(name); err != nil {
		fail("%v", err)
		os.Exit(1)
	}

	// Capture skill info before any modifications
	var skillInfo *SkillEntry
	for _, s := range m.Skills {
		if s.Name == name {
			skillInfo = &s
			break
		}
	}
	_, inLock := lock.Skills[name]

	if skillInfo == nil && !inLock {
		fail("skill %q not found in manifest or lock", name)
		os.Exit(1)
	}

	// Show what we'd do
	if !quiet {
		fmt.Printf("  %s %s:\n", bold("remove"), name)
		if skillInfo != nil {
			if keepManifest {
				fmt.Printf("    manifest: %s (keep entry)\n", yellow("keep"))
			} else {
				fmt.Printf("    manifest: remove entry\n")
			}
		}
		if inLock {
			fmt.Printf("    lock: remove entry\n")
		}
		// Check disk
		for _, d := range m.Directories {
			dirPath := expandPath(d.Path)
			if _, err := os.Stat(filepath.Join(dirPath, name, "SKILL.md")); err == nil {
				fmt.Printf("    disk: remove %s/%s\n", d.Name, name)
			}
		}
		fmt.Printf("    mirrors: cleanup symlinks\n")
	}

	if dryRun {
		return
	}

	// 1. Lock — always try to remove
	if inLock {
		delete(lock.Skills, name)
		lock.Updated = time.Now().Format(time.RFC3339)
		if err := writeLock(getLockPath(manifestPath), lock); err != nil {
			warn("lock write: %v", err)
		} else {
			ok("lock: removed entry")
		}
	}

	// 2. Manifest — remove unless --keep-manifest
	if skillInfo != nil && !keepManifest {
		newSkills := make([]SkillEntry, 0, len(m.Skills)-1)
		for _, s := range m.Skills {
			if s.Name != name {
				newSkills = append(newSkills, s)
			}
		}
		m.Skills = newSkills
		if err := writeManifest(manifestPath, m); err != nil {
			warn("manifest write: %v", err)
		} else {
			ok("manifest: removed entry")
		}
	}

	// 3. Disk — remove skill directory from all configured directories
	for _, d := range m.Directories {
		dirPath := expandPath(d.Path)
		skillDir := filepath.Join(dirPath, name)
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
			if err := os.RemoveAll(skillDir); err != nil {
				warn("disk: %s: %v", skillDir, err)
			} else {
				ok("disk: removed %s/%s", d.Name, name)
			}
		}
	}

	// 4. Mirrors — applyMirrors cleans up orphan symlinks
	applyMirrors(m)
}

func cmdVerify(m *Manifest) {
	bad := 0
	for _, s := range m.Skills {
		targetPath := resolveTargetPath(s.Target, m.Directories)
		if targetPath == "" {
			fail("%s: unknown target %q", s.Name, s.Target)
			bad++
			continue
		}
		skillDir := filepath.Join(expandPath(targetPath), s.Name)
		sm := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(sm); err == nil {
			ok("%s", s.Name)
		} else {
			fail("%s: not found at %s", s.Name, sm)
			bad++
		}
	}
	if !quiet {
		if bad > 0 {
			fmt.Println("\n  " + yellow(fmt.Sprintf("%d skill(s) missing", bad)))
		} else {
			fmt.Println("\n  " + green("All skills present."))
		}
	}
}

func cmdInfo(m *Manifest, lock *LockFile, name string) {
	var found *SkillEntry
	for _, s := range m.Skills {
		if s.Name == name {
			found = &s
			break
		}
	}
	if found == nil {
		fail("skill %q not found in manifest", name)
		os.Exit(1)
	}

	targetPath := resolveTargetPath(found.Target, m.Directories)
	diskPath := ""
	if targetPath != "" {
		diskPath = filepath.Join(expandPath(targetPath), found.Name)
	}

	onDisk := false
	if diskPath != "" {
		if _, err := os.Stat(filepath.Join(diskPath, "SKILL.md")); err == nil {
			onDisk = true
		}
	}

	fmt.Printf("  %s: %s\n", bold("name"), found.Name)
	fmt.Printf("  %s: %s\n", bold("target"), found.Target)
	fmt.Printf("  %s: %s\n", bold("repo"), found.Source.Repo)
	fmt.Printf("  %s: %s\n", bold("ref"), found.Source.Ref)
	fmt.Printf("  %s: %s\n", bold("path"), found.Source.Path)
	if diskPath != "" {
		fmt.Printf("  %s: %s\n", bold("on disk"), diskPath)
		fmt.Printf("  %s: %v\n", bold("installed"), onDisk)
	}
	if ls, ok := lock.Skills[found.Name]; ok {
		fmt.Printf("  %s: %s\n", bold("locked commit"), ls.Commit)
	}
}
