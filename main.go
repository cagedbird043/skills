package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var version = "dev" // injected via -ldflags=-X main.version=$(git describe --tags --always)

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

// ── add command types ─────────────────────────────────────────────────

type addOptions struct {
	Repo      string
	Path      string
	Name      string
	Ref       string
	Target    string
	FilesSpec string
	NoInstall bool
}

func parseAddArgs(args []string) (addOptions, error) {
	var o addOptions
	o.Path = "."
	o.Ref = "main"
	o.Target = "shared"

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			if o.Repo == "" {
				o.Repo = arg
				continue
			}
			if o.Path == "." {
				o.Path = arg
				continue
			}
			return o, fmt.Errorf("unexpected argument %q", arg)
		}

		// Parse --flag or --flag=value
		var name, val string
		hasEq := strings.Contains(arg, "=")
		if hasEq {
			parts := strings.SplitN(arg, "=", 2)
			name = parts[0]
			val = parts[1]
		} else {
			name = arg
		}

		switch name {
		case "--name":
			if !hasEq {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("--name requires a value")
				}
				val = args[i]
			}
			o.Name = val
		case "--ref":
			if !hasEq {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("--ref requires a value")
				}
				val = args[i]
			}
			o.Ref = val
		case "--target":
			if !hasEq {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("--target requires a value")
				}
				val = args[i]
			}
			o.Target = val
		case "--files":
			if !hasEq {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("--files requires a value")
				}
				val = args[i]
			}
			o.FilesSpec = val
		case "--no-install":
			if hasEq && val != "" {
				return o, fmt.Errorf("--no-install does not take a value")
			}
			o.NoInstall = true
		default:
			return o, fmt.Errorf("unknown flag %q", name)
		}
	}

	if o.Repo == "" {
		return o, fmt.Errorf("repository is required (owner/repo)")
	}

	// Validate repo format: owner/repo, no URL, no spaces, no ".."
	parts := strings.Split(o.Repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.Contains(o.Repo, "..") || strings.Contains(o.Repo, " ") {
		return o, fmt.Errorf("invalid repo format %q — expected owner/repo", o.Repo)
	}

	return o, nil
}


// ── CLI ──────────────────────────────────────────────────────────────

func usage() {
	fmt.Fprintf(os.Stderr, `%s — Agent skill manager  v%s

%s:
  skills <command> [options]

%s:
  list              List all skills with installation status
  add <owner/repo>  Add a new skill from a GitHub repository
  install [name]    Install from lock (no remote check — fast)
  update            Check remote commits, update changed skills
  remove <name...>  Remove one or more skills from manifest, lock, disk, and mirrors
  fmt               Format manifest to canonical form
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
  skills add cagedbird043/skills
  skills add cagedbird043/skills skills/caveman
  skills add cagedbird043/skills --name myname --ref develop
  skills add cagedbird043/skills --files "SKILL.md=README.md"
  skills add cagedbird043/skills --dry-run
  skills add cagedbird043/skills --no-install
  skills install
  skills install drawio
  skills update
  skills remove drawio
  skills remove drawio docx pdf
  skills remove drawio --keep-manifest
  skills remove drawio --dry-run
  skills install --dry-run
  skills fmt
  skills fmt --dry-run
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

	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".config", "skills", ".manifest.json")
	}
	return ""
}

func main() {
	manifestPath := ""
	var positional []string
	dryRun := false
	yes := false
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
		if arg == "-y" || arg == "--yes" {
			yes = true
			continue
		}
		if arg == "-k" || arg == "--keep-manifest" {
			keepManifest = true
			continue
		}
		if arg == "--complete-names" {
			completeNames(manifestPath)
			return
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
		fmt.Fprintf(os.Stderr, "skills: lock file corrupted: %v\n", err)
		fmt.Fprintf(os.Stderr, "  → run 'skills update' to regenerate\n")
		os.Exit(1)
	}

	switch subcmd {
	case "list":
		cmdList(m, lock)
	case "install":
		target := ""
		if len(positional) > 1 {
			target = positional[1]
		}
		cmdInstall(m, lock, manifestPath, target, dryRun)
	case "add":
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "skills: add requires a repository (owner/repo)")
			os.Exit(1)
		}
		opts, err := parseAddArgs(positional[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "skills: add: %v\n", err)
			os.Exit(1)
		}
		cmdAdd(m, lock, manifestPath, opts, dryRun)
	case "fmt":
		cmdFmt(m, manifestPath, dryRun)
	case "update":
		target := ""
		if len(positional) > 1 {
			target = positional[1]
		}
		cmdUpdate(m, lock, manifestPath, target, dryRun, yes)
	case "remove":
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "skills: remove requires at least one skill name")
			os.Exit(1)
		}
		cmdRemove(m, lock, manifestPath, positional[1:], keepManifest, dryRun)
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

// completeNames prints all skill names from the manifest (one per line).
// Used by shell completion scripts via --complete-names flag.
func completeNames(manifestPath string) {
	mp := findManifest(manifestPath)
	if mp == "" {
		return
	}
	m, err := readManifest(mp)
	if err != nil {
		return
	}
	for _, s := range m.Skills {
		fmt.Println(s.Name)
	}
}

func cmdCompletion(shell string) {
	switch shell {
	case "zsh":
		fmt.Print(`#compdef skills

_skills() {
  local -a cmds
  cmds=(
    'add:add a new skill from a GitHub repository'
    'list:list all skills with status'
    'install:install from lock (fast, no remote check)'
    'update:audit and update skills'
    'remove:remove a skill from manifest and disk'
    'info:show skill details'
    'fmt:format manifest to canonical form'
    'completion:generate shell completion'
  )
  _describe -t commands 'skills command' cmds
  # Positional: skill name completion for commands that take a skill argument
  case "$words[2]" in
    remove|install|info|update)
      if [[ $CURRENT -ge 3 ]]; then
        local -a skill_names
        skill_names=(${(f)"$(skills --complete-names 2>/dev/null)"})
        _describe -t skills 'skill name' skill_names
        return
      fi
      ;;
  esac

  # Flag completion
  case "$words[2]" in
    update)
      _alternative \
        'args: :(--dry-run -n --yes -y)'
      ;;
    remove)
      _alternative \
        'args: :(--dry-run -n --keep-manifest -k)'
      ;;
  esac
}

_skills "$@"
`)
	case "bash":
		fmt.Print(`_skills() {
  local cur prev words cword
  _init_completion || return
  case "$prev" in
    remove|install|info|update)
      [[ $cur != -* ]] || return 0
      COMPREPLY=($(compgen -W "$(skills --complete-names 2>/dev/null)" -- "$cur"))
      return
      ;;
  esac
  COMPREPLY=($(compgen -W "add list install update remove info fmt completion" -- "$cur"))
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
// With --dry-run, shows what would be installed without touching disk.
func cmdInstall(m *Manifest, lock *LockFile, manifestPath, target string, dryRun bool) {
	if target != "" {
		if err := validateSkillName(target); err != nil {
			fail("%v", err)
			os.Exit(1)
		}
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

		if dryRun {
			fmt.Printf("  %s %s (from %s/%s @ %s)\n", bold("install"), found.Name, found.Source.Repo, found.Source.Path, found.Source.Ref)
			return
		}

		r, ls := installOneSkill(*found, lock, m.Directories)
		if ls != nil {
			lock.Skills[found.Name] = *ls
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

	if dryRun {
		fmt.Printf("  %s all skills from lock\n", bold("install"))
		return
	}

	results := InstallAll(m, lock, manifestPath)
	printSummary(results)
}

type auditItem struct {
	Name   string
	Status string // ok | missing | uninstalled | stale | orphan | path-changed | degraded | outdated
	Detail string
}

// cmdUpdate audits all skills, shows a plan, and executes updates.
//   skills update           →  audit + show plan + confirm + execute
//   skills update --dry-run →  audit + show plan only
//   skills update -y        →  audit + show plan + execute (no confirm)
//   skills update <name>    →  update single skill (passthrough to updateOneSkill)
func cmdUpdate(m *Manifest, lock *LockFile, manifestPath, target string, dryRun, yes bool) {
	// Single-skill update: passthrough to existing updateOneSkill
	if target != "" {
		if err := validateSkillName(target); err != nil {
			fail("%v", err)
			os.Exit(1)
		}
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
			if err := writeLock(getLockPath(manifestPath), lock); err != nil {
				warn("lock write: %v", err)
			}
		}
		applySymlinks(m)
		applyMirrors(m)
		if r.Action == "ok" || r.Action == "updated" {
			ok("%s", found.Name)
		} else {
			fail("%s: %s", found.Name, r.Error)
		}
		return
	}

	// ── Full audit ─────────────────────────────────────────────────

	var items []auditItem

	// Check manifest skills
	manifestNames := make(map[string]bool)
	for _, s := range m.Skills {
		manifestNames[s.Name] = true
		ls, hasLock := lock.Skills[s.Name]
		targetPath := resolveTargetPath(s.Target, m.Directories)
		diskExists := false
		if targetPath != "" {
			skillMD := filepath.Join(expandPath(targetPath), s.Name, "SKILL.md")
			if _, err := os.Stat(skillMD); err == nil {
				diskExists = true
			}
		}

		if !hasLock && !diskExists {
			items = append(items, auditItem{s.Name, "uninstalled", "never installed"})
			continue
		}
		if hasLock && ls.Path != s.Source.Path {
			items = append(items, auditItem{s.Name, "path-changed",
				fmt.Sprintf("lock path %q → %q", ls.Path, s.Source.Path)})
			continue
		}
		if hasLock && !diskExists {
			items = append(items, auditItem{s.Name, "missing", "files not found on disk"})
			continue
		}
		if !hasLock && diskExists {
			items = append(items, auditItem{s.Name, "stale-disk", "lock missing, disk present"})
			continue
		}
		// Locally consistent — check remote commit
		latestCommit, err := fetchLatestCommitFn(s.Source.Repo, s.Source.Ref)
		if err != nil {
			items = append(items, auditItem{s.Name, "degraded",
				fmt.Sprintf("remote check failed: %v", err)})
			continue
		}
		if hasLock && ls.Commit != latestCommit {
			items = append(items, auditItem{s.Name, "outdated",
				fmt.Sprintf("commit %s..%s", ls.Commit[:min(8, len(ls.Commit))], latestCommit[:8])})
			continue
		}
		items = append(items, auditItem{s.Name, "ok", ""})
	}

	// Check for stale (lock has it but manifest doesn't)
	for name, ls := range lock.Skills {
		if !manifestNames[name] {
			items = append(items, auditItem{name, "stale",
				fmt.Sprintf("not in manifest, lock has commit %s", ls.Commit[:min(8, len(ls.Commit))])})
		}
	}

	// Check for orphan (disk has directory with SKILL.md but not in manifest or lock)
	for _, d := range m.Directories {
		dirPath := expandPath(d.Path)
		if entries, err := os.ReadDir(dirPath); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				if manifestNames[name] {
					continue
				}
				if _, inLock := lock.Skills[name]; inLock {
					continue
				}
				if _, err := os.Stat(filepath.Join(dirPath, name, "SKILL.md")); err == nil {
					items = append(items, auditItem{name, "orphan",
						fmt.Sprintf("only on disk in %s", d.Name)})
				}
			}
		}
	}

	// Sort: non-ok first, then alphabetical
	sortItems(items)

	// ── Show plan ──────────────────────────────────────────────────

	if !quiet {
		fmt.Println(bold("Plan:"))
		needsUpdate := 0
		nDegraded := 0
		nStale := 0
		for _, item := range items {
			var statusColor string
			switch item.Status {
			case "ok":
				statusColor = green("ok")
			case "missing", "uninstalled", "path-changed", "stale-disk", "outdated":
				statusColor = yellow(item.Status)
				needsUpdate++
			case "degraded":
				statusColor = yellow("degraded")
				nDegraded++
			case "stale", "orphan":
				statusColor = red(item.Status)
				nStale++
			default:
				statusColor = item.Status
			}
			if item.Detail != "" {
				fmt.Printf("  %-12s %-20s %s\n", statusColor, item.Name, dim(item.Detail))
			} else {
				fmt.Printf("  %-12s %-20s\n", statusColor, item.Name)
			}
		}
		fmt.Println()
		parts := []string{fmt.Sprintf("%d total", len(items))}
		if needsUpdate > 0 {
			parts = append(parts, fmt.Sprintf("%d to update", needsUpdate))
		}
		if nDegraded > 0 {
			parts = append(parts, fmt.Sprintf("%d unreachable", nDegraded))
		}
		if nStale > 0 {
			parts = append(parts, fmt.Sprintf("%d to clean up", nStale))
		}
		summary := strings.Join(parts, ", ")
		if needsUpdate > 0 || nDegraded > 0 || nStale > 0 {
			fmt.Println("  " + yellow(summary))
		} else {
			fmt.Println("  " + green(summary))
		}
	}

	if dryRun {
		return
	}

	// ── Stale lock cleanup (before early return) ─────────────────

	staleFound := false
	for _, item := range items {
		if item.Status == "stale" {
			delete(lock.Skills, item.Name)
			staleFound = true
		}
	}

	// ── Confirm ────────────────────────────────────────────────────

	// Check if any skill needs execution (install/update)
	var needsUpdate []SkillEntry
	for _, item := range items {
		if item.Status == "missing" || item.Status == "uninstalled" || item.Status == "path-changed" || item.Status == "stale-disk" || item.Status == "outdated" {
			for _, s := range m.Skills {
				if s.Name == item.Name {
					needsUpdate = append(needsUpdate, s)
					break
				}
			}
		}
	}

	if len(needsUpdate) == 0 {
		if staleFound {
			if err := writeLock(getLockPath(manifestPath), lock); err != nil {
				warn("lock write: %v", err)
			}
			if !quiet {
				ok("cleaned stale lock entries")
			}
		} else if !quiet {
			fmt.Println("  " + green("Nothing to update."))
		}
		return
	}

	if !yes {
		fmt.Printf("  %s %d skill(s) to update. Proceed? [y/N] ", bold("?"), len(needsUpdate))
		var answer string
		fmt.Scanln(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("  " + yellow("cancelled"))
			return
		}
	}

	// ── Execute ────────────────────────────────────────────────────

	for _, s := range needsUpdate {
		r, ls := updateOneSkill(s, lock, m.Directories)
		if ls != nil {
			lock.Skills[s.Name] = *ls
		}
		if r.Action == "ok" || r.Action == "updated" {
			ok("%s", s.Name)
		} else {
			fail("%s: %s", s.Name, r.Error)
		}
	}

	if err := writeLock(getLockPath(manifestPath), lock); err != nil {
		warn("lock write: %v", err)
	}
	applySymlinks(m)
	applyMirrors(m)
}

func sortItems(items []auditItem) {
	// Non-ok first, then alphabetical
	statusOrder := map[string]int{
		"outdated": 0, "degraded": 1, "missing": 2, "uninstalled": 3, "path-changed": 4, "stale-disk": 5,
		"stale": 6, "orphan": 7, "ok": 8,
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			oi := statusOrder[items[i].Status]
			oj := statusOrder[items[j].Status]
			if oi > oj || (oi == oj && items[i].Name > items[j].Name) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

// cmdRemove removes one or more skills from lock, manifest, disk, and mirror symlinks.
// Execution order: validate → lock → manifest → disk → applyMirrors.
// Lock and manifest are written once (batched) for all names.
func cmdRemove(m *Manifest, lock *LockFile, manifestPath string, names []string, keepManifest, dryRun bool) {
	// Phase 0: validate all names, check existence
	type removeTarget struct {
		name      string
		skillInfo *SkillEntry
		inLock    bool
	}
	targets := make([]removeTarget, 0, len(names))
	allOk := true
	for _, name := range names {
		if err := validateSkillName(name); err != nil {
			fail("%v", err)
			allOk = false
			continue
		}
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
			allOk = false
			continue
		}
		targets = append(targets, removeTarget{name, skillInfo, inLock})
	}
	if !allOk {
		os.Exit(1)
	}

	// Show plan
	if !quiet {
		for _, t := range targets {
			fmt.Printf("  %s %s:\n", bold("remove"), t.name)
			if t.skillInfo != nil {
				if keepManifest {
					fmt.Printf("    manifest: %s (keep entry)\n", yellow("keep"))
				} else {
					fmt.Printf("    manifest: remove entry\n")
				}
			}
			if t.inLock {
				fmt.Printf("    lock: remove entry\n")
			}
			for _, d := range m.Directories {
				dirPath := expandPath(d.Path)
				if _, err := os.Stat(filepath.Join(dirPath, t.name, "SKILL.md")); err == nil {
					fmt.Printf("    disk: remove %s/%s\n", d.Name, t.name)
				}
			}
			fmt.Printf("    mirrors: cleanup symlinks\n")
		}
	}

	if dryRun {
		return
	}

	// Phase 1: Lock — batch remove from map, write once
	lockChanged := false
	for _, t := range targets {
		if t.inLock {
			delete(lock.Skills, t.name)
			lockChanged = true
		}
	}
	if lockChanged {
		if err := writeLock(getLockPath(manifestPath), lock); err != nil {
			warn("lock write: %v", err)
		} else {
			ok("lock: removed %d entries", len(targets))
		}
	}

	// Phase 2: Manifest — batch filter, write once
	if !keepManifest {
		removeSet := make(map[string]bool, len(targets))
		for _, t := range targets {
			if t.skillInfo != nil {
				removeSet[t.name] = true
			}
		}
		if len(removeSet) > 0 {
			newSkills := make([]SkillEntry, 0, len(m.Skills))
			for _, s := range m.Skills {
				if !removeSet[s.Name] {
					newSkills = append(newSkills, s)
				}
			}
			m.Skills = newSkills
			if err := writeManifest(manifestPath, m); err != nil {
				warn("manifest write: %v", err)
			} else {
				ok("manifest: removed %d entries", len(removeSet))
			}
		}
	}

	// Phase 3: Disk — remove skill directories
	for _, t := range targets {
		for _, d := range m.Directories {
			dirPath := expandPath(d.Path)
			skillDir := filepath.Join(dirPath, t.name)
			if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
				if err := os.RemoveAll(skillDir); err != nil {
					warn("disk: %s: %v", skillDir, err)
				} else {
					ok("disk: removed %s/%s", d.Name, t.name)
				}
			}
		}
	}

	// Phase 4: Mirrors — applyMirrors cleans up orphan symlinks
	applyMirrors(m)
}


// cmdAdd adds a new skill to the manifest and optionally installs it.
// Transaction order: validate → dry-run check → fetch commit → install → write manifest → write lock → mirrors.
func cmdAdd(m *Manifest, lock *LockFile, manifestPath string, opts addOptions, dryRun bool) {
	// 1. Validate target exists
	targetExists := false
	for _, d := range m.Directories {
		if d.Name == opts.Target {
			targetExists = true
			break
		}
	}
	if !targetExists {
		fail("target %q not found in manifest directories", opts.Target)
		os.Exit(1)
	}

	// 2. Infer name
	name := opts.Name
	if name == "" {
		if opts.Path != "" && opts.Path != "." {
			name = strings.TrimRight(filepath.Base(opts.Path), "/")
		} else {
			name = strings.TrimSuffix(opts.Repo, ".git")
			if idx := strings.LastIndex(name, "/"); idx >= 0 {
				name = name[idx+1:]
			}
		}
		// Reject filename-like names (e.g. "SKILL.md") unless --name was given
		if strings.Contains(name, ".") {
			fail("inferred name %q looks like a filename — use --name to set a proper skill name", name)
			os.Exit(1)
		}
	}

	if err := validateSkillName(name); err != nil {
		fail("invalid skill name: %v", err)
		os.Exit(1)
	}

	// Check duplicate
	for _, s := range m.Skills {
		if s.Name == name {
			fail("skill %q already exists in manifest", name)
			os.Exit(1)
		}
	}

	// 3. Build candidate SkillEntry
	sourceType := ""
	files := make(map[string]string)

	if opts.FilesSpec != "" {
		sourceType = "github-files"
		pairs := strings.Split(opts.FilesSpec, ",")
		for _, pair := range pairs {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
				fail("invalid --files entry %q (expected local=remote)", pair)
				os.Exit(1)
			}
			if strings.Contains(kv[0], "..") || strings.HasPrefix(kv[0], "/") {
				fail("invalid local path %q in --files", kv[0])
				os.Exit(1)
			}
			if strings.Contains(kv[1], "..") || strings.HasPrefix(kv[1], "/") {
				fail("invalid remote path %q in --files", kv[1])
				os.Exit(1)
			}
			files[kv[0]] = kv[1]
		}
		// --files implies path must be "." or empty
		if opts.Path != "." && opts.Path != "" {
			fail("--files cannot be used with a path argument")
			os.Exit(1)
		}
	}

	entry := SkillEntry{
		Name:   name,
		Target: opts.Target,
		Source: SourceEntry{
			Type:  sourceType,
			Repo:  opts.Repo,
			Ref:   opts.Ref,
			Path:  opts.Path,
			Files: files,
		},
	}

	// 4. --dry-run: preview without network or disk
	if dryRun {
		fmt.Println("[dry-run] Would add skill:")
		fmt.Printf("  Name:   %s\n", name)
		fmt.Printf("  Repo:   %s\n", opts.Repo)
		fmt.Printf("  Ref:    %s\n", opts.Ref)
		fmt.Printf("  Target: %s\n", opts.Target)
		fmt.Printf("  Path:   %s\n", opts.Path)
		if opts.FilesSpec != "" {
			fmt.Printf("  Files:  %s\n", opts.FilesSpec)
		}
		fmt.Println("  (remote not checked)")
		return
	}

	// 5. Fetch latest commit — fail before writing manifest
	commit, err := fetchLatestCommitFn(opts.Repo, opts.Ref)
	if err != nil {
		msg := fmt.Sprintf("check commit: %v", err)
		if isRateLimit(err) {
			msg += " — set GITHUB_TOKEN or install gh for higher rate limits"
		}
		fail("%s", msg)
		os.Exit(1)
	}

	// 6. Install (unless --no-install) — fail before writing manifest
	installed := false
	if !opts.NoInstall {
		targetPath := resolveTargetPath(opts.Target, m.Directories)
		if targetPath == "" {
			fail("unknown target %q", opts.Target)
			os.Exit(1)
		}
		destDir := filepath.Join(expandPath(targetPath), name)
		result := InstallSkill(entry, destDir, "")
		if result.Action == "failed" {
			fail("install %s: %s", name, result.Error)
			os.Exit(1)
		}
		installed = true
	}

	// 7. Append to manifest and write
	m.Skills = append(m.Skills, entry)
	if err := writeManifest(manifestPath, m); err != nil {
		fail("write manifest: %v", err)
		os.Exit(1)
	}

	// 8. Write lock entry
	if installed {
		lock.Skills[name] = LockSkill{
			Commit:     commit,
			Path:       opts.Path,
			SourceHash: computeSourceHash(entry.Source),
		}
		if err := writeLock(getLockPath(manifestPath), lock); err != nil {
			warn("write lock: %v", err)
		}
	}

	// 9. Symlinks and mirrors
	applySymlinks(m)
	applyMirrors(m)

	ok("added %s", name)
}

// cmdFmt reformats the manifest to canonical form.
// With --dry-run, shows a unified diff instead of writing.
func cmdFmt(m *Manifest, manifestPath string, dryRun bool) {
	orig, err := os.ReadFile(manifestPath)
	if err != nil {
		fail("read manifest: %v", err)
		os.Exit(1)
	}

	canonical, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fail("marshal manifest: %v", err)
		os.Exit(1)
	}
	canonical = append(canonical, '\n')

	if bytes.Equal(orig, canonical) {
		ok("manifest is already canonical")
		return
	}

	if dryRun {
		fmt.Println(dim("--- current"))
		fmt.Println(bold("+++ canonical"))
		origLines := strings.Split(string(orig), "\n")
		canonLines := strings.Split(string(canonical), "\n")
		maxLen := len(origLines)
		if len(canonLines) > maxLen {
			maxLen = len(canonLines)
		}
		i, j := 0, 0
		for i < maxLen || j < maxLen {
			var origLine, canonLine string
			if i < len(origLines) {
				origLine = origLines[i]
			}
			if j < len(canonLines) {
				canonLine = canonLines[j]
			}
			if origLine == canonLine {
				if origLine != "" || i < len(origLines) || j < len(canonLines) {
					fmt.Printf("  %s\n", dim(origLine))
				}
				i++
				j++
			} else {
				if origLine != "" {
					fmt.Printf("%s %s\n", red("-"), red(origLine))
				}
				if canonLine != "" {
					fmt.Printf("%s %s\n", green("+"), green(canonLine))
				}
				i++
				j++
			}
		}
	} else {
		if err := atomicWriteFile(manifestPath, canonical, 0644); err != nil {
			fail("write manifest: %v", err)
			os.Exit(1)
		}
		ok("manifest formatted")
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
