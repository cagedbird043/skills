package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type doctorJSONItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorJSONCounts struct {
	OK    int `json:"ok"`
	Drift int `json:"drift"`
}

type doctorJSONPruned struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Action    string `json:"action"`
}

type doctorJSONReport struct {
	ManifestPath string             `json:"manifest_path"`
	LockPath     string             `json:"lock_path"`
	SkillCount   int                `json:"skill_count"`
	Items        []doctorJSONItem   `json:"items"`
	Counts       doctorJSONCounts   `json:"counts"`
	DryRun       bool               `json:"dry_run"`
	Pruned       []doctorJSONPruned `json:"pruned"`
}

func cmdDoctor(m *Manifest, lock *LockFile, manifestPath string, pruneTmp, dryRun, jsonOutput bool) {
	items := collectDoctorItems(m, lock)
	if jsonOutput {
		printDoctorJSON(m, manifestPath, items, pruneTmp, dryRun)
		return
	}

	fmt.Printf("  %s: %s\n", bold("manifest"), manifestPath)
	fmt.Printf("  %s: %s\n", bold("lock"), getLockPath(manifestPath))
	fmt.Printf("  %s: %d\n", bold("skills"), len(m.Skills))
	fmt.Println()
	fmt.Println(bold("Doctor:"))

	if len(items) == 0 {
		fmt.Println("  " + green("No drift detected."))
		return
	}

	okCount := 0
	issueCount := 0
	tmpCount := 0
	for _, item := range items {
		statusColor := doctorStatusLabel(item.Status)
		switch item.Status {
		case "ok":
			okCount++
		case statusTmpLeftover:
			tmpCount++
			issueCount++
		default:
			issueCount++
		}
		if item.Detail != "" {
			fmt.Printf("  %-16s %-24s %s\n", statusColor, item.Name, dim(item.Detail))
		} else {
			fmt.Printf("  %-16s %-24s\n", statusColor, item.Name)
		}
	}
	fmt.Println()
	if issueCount == 0 {
		fmt.Printf("  %s\n", green(fmt.Sprintf("%d ok, no drift", okCount)))
		return
	}
	fmt.Printf("  %s\n", yellow(fmt.Sprintf("%d ok, %d drift item(s)", okCount, issueCount)))
	fmt.Printf("  %s\n", dim("Reconcile local state with 'skills sync'; use 'skills update' when refs changed upstream."))
	if tmpCount > 0 && !pruneTmp {
		fmt.Printf("  %s\n", dim(fmt.Sprintf("%d leftover(s) from interrupted installs are safe to delete: run 'skills doctor --prune-tmp'.", tmpCount)))
	}
	if pruneTmp {
		fmt.Println()
		pruneTmpLeftovers(m, dryRun)
	}
}

func printDoctorJSON(m *Manifest, manifestPath string, items []auditItem, pruneTmp, dryRun bool) {
	report := doctorJSONReport{
		ManifestPath: manifestPath,
		LockPath:     getLockPath(manifestPath),
		SkillCount:   len(m.Skills),
		Items:        make([]doctorJSONItem, 0, len(items)),
		DryRun:       dryRun,
		Pruned:       make([]doctorJSONPruned, 0),
	}
	for _, item := range items {
		report.Items = append(report.Items, doctorJSONItem(item))
		if item.Status == "ok" {
			report.Counts.OK++
		} else {
			report.Counts.Drift++
		}
	}
	if pruneTmp {
		report.Pruned = pruneTmpLeftoversJSON(m, dryRun)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		warn("doctor JSON: %v", err)
		return
	}
	fmt.Println(string(data))
}

func doctorStatusLabel(status string) string {
	switch status {
	case "ok":
		return green("ok")
	case "invalid-target", "missing", "path-changed", "stale-disk", "stale", "unmanaged", "mirror-missing", "mirror-wrong-link", "mirror-conflict", "mirror-stray":
		return red(status)
	case "uninstalled":
		return yellow(status)
	default:
		return yellow(status)
	}
}

func collectDoctorItems(m *Manifest, lock *LockFile) []auditItem {
	items := make([]auditItem, 0)
	manifestNames := make(map[string]bool, len(m.Skills))

	for _, s := range m.Skills {
		manifestNames[s.Name] = true
		targetPath := resolveTargetPath(s.Target, m.Directories)
		if targetPath == "" {
			items = append(items, auditItem{s.Name, "invalid-target", fmt.Sprintf("target %q is not resolvable", s.Target)})
			continue
		}

		ls, hasLock := lock.Skills[s.Name]
		skillMD := filepath.Join(expandPath(targetPath), s.Name, "SKILL.md")
		_, diskErr := os.Stat(skillMD)
		diskExists := diskErr == nil

		switch {
		case !hasLock && !diskExists:
			items = append(items, auditItem{s.Name, "uninstalled", "declared, but no lock entry and nothing on disk"})
		case hasLock && ls.Path != s.Source.Path:
			items = append(items, auditItem{s.Name, "path-changed", fmt.Sprintf("lock path %q -> %q", ls.Path, s.Source.Path)})
		case hasLock && !diskExists:
			items = append(items, auditItem{s.Name, "missing", "lock exists, but files are missing on disk"})
		case !hasLock && diskExists:
			items = append(items, auditItem{s.Name, "stale-disk", "disk present, but lock entry is missing"})
		default:
			items = append(items, auditItem{s.Name, "ok", ""})
		}
	}

	for name, ls := range lock.Skills {
		if !manifestNames[name] {
			detail := "not declared in manifest"
			if ls.Commit != "" {
				detail = fmt.Sprintf("not declared in manifest; lock commit %s", shortCommit(ls.Commit))
			}
			items = append(items, auditItem{name, "stale", detail})
		}
	}

	for _, dir := range doctorScanDirs(m) {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			// Leftovers are named ".<skill>.tmp-*"/".<skill>.old-*" and may or may
			// not carry SKILL.md depending on where the install died, so classify
			// them before the SKILL.md check below.
			if isTmpLeftoverName(name) {
				items = append(items, auditItem{name, statusTmpLeftover, fmt.Sprintf("interrupted install leftover in %s; safe to delete", dir.name)})
				continue
			}
			if manifestNames[name] {
				continue
			}
			if _, inLock := lock.Skills[name]; inLock {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir.path, name, "SKILL.md")); err == nil {
				items = append(items, auditItem{name, "unmanaged", fmt.Sprintf("a real skill in %s that skills did not install; keep it, or run 'skills add' to manage it", dir.name)})
			}
		}
	}

	items = append(items, collectMirrorDoctorItems(m)...)
	sortItems(items)
	return items
}

type doctorDir struct {
	name string
	path string
}

func doctorScanDirs(m *Manifest) []doctorDir {
	dirs := make([]doctorDir, 0, len(m.Directories)+1)
	seen := make(map[string]bool, len(m.Directories)+1)
	for _, dir := range m.Directories {
		path := expandPath(dir.Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		dirs = append(dirs, doctorDir{name: dir.Name, path: path})
	}

	needsOMP := false
	for _, skill := range m.Skills {
		if skill.Target == specialTargetOMP {
			needsOMP = true
			break
		}
	}
	if needsOMP {
		ompPath := resolveTargetPath(specialTargetOMP, m.Directories)
		if ompPath != "" && !seen[ompPath] {
			dirs = append(dirs, doctorDir{name: specialTargetOMP, path: expandPath(ompPath)})
		}
	}

	return dirs
}

func collectMirrorDoctorItems(m *Manifest) []auditItem {
	items := make([]auditItem, 0)
	dirPaths := make(map[string]string, len(m.Directories))
	for _, d := range m.Directories {
		dirPaths[d.Name] = expandPath(d.Path)
	}

	for _, mirror := range m.Mirrors {
		srcDir := dirPaths[mirror.From]
		dstDir := dirPaths[mirror.To]
		if srcDir == "" || dstDir == "" {
			items = append(items, auditItem{
				Name:   mirror.From + "→" + mirror.To,
				Status: "invalid-target",
				Detail: "mirror references an unknown directory",
			})
			continue
		}

		wanted := make(map[string]string)
		for _, s := range m.Skills {
			if s.Target != mirror.From {
				continue
			}
			excluded := false
			for _, ex := range mirror.Exclude {
				if ex == s.Name {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
			src := filepath.Join(srcDir, s.Name)
			if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
				continue
			}
			wanted[s.Name] = src

			dst := filepath.Join(dstDir, s.Name)
			fi, err := os.Lstat(dst)
			if os.IsNotExist(err) {
				items = append(items, auditItem{
					Name:   mirror.To + "/" + s.Name,
					Status: "mirror-missing",
					Detail: fmt.Sprintf("expected %s→%s symlink", mirror.From, mirror.To),
				})
				continue
			}
			if err != nil {
				continue
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				items = append(items, auditItem{
					Name:   mirror.To + "/" + s.Name,
					Status: "mirror-conflict",
					Detail: fmt.Sprintf("%s exists, but is not a symlink", mirror.To),
				})
				continue
			}
			target, err := os.Readlink(dst)
			if err != nil {
				continue
			}
			if filepath.Clean(target) != filepath.Clean(src) {
				items = append(items, auditItem{
					Name:   mirror.To + "/" + s.Name,
					Status: "mirror-wrong-link",
					Detail: fmt.Sprintf("points to %s, want %s", target, src),
				})
			}
		}

		srcPrefix := filepath.Clean(srcDir) + string(os.PathSeparator)
		entries, err := os.ReadDir(dstDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(dstDir, entry.Name())
			fi, err := os.Lstat(path)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(path)
			if err != nil {
				continue
			}
			cleanTarget := filepath.Clean(target)
			if cleanTarget != filepath.Clean(srcDir) && !strings.HasPrefix(cleanTarget, srcPrefix) {
				continue
			}
			if _, ok := wanted[entry.Name()]; !ok {
				items = append(items, auditItem{
					Name:   mirror.To + "/" + entry.Name(),
					Status: "mirror-stray",
					Detail: fmt.Sprintf("symlink skills created for %s→%s, now pointing at nothing it manages; 'skills sync' removes it", mirror.From, mirror.To),
				})
			}
		}
	}

	return items
}

func shortCommit(commit string) string {
	if len(commit) <= 8 {
		return commit
	}
	return commit[:8]
}

// statusTmpLeftover marks a staging/backup directory abandoned by an interrupted
// install. Unlike an "unmanaged" skill (a real skill placed on disk by hand), it
// holds no state worth keeping and is always safe to delete.
const statusTmpLeftover = "tmp-leftover"

type tmpLeftover struct {
	dir  string
	name string
	path string
}

// isTmpLeftoverName reports whether name is an install leftover: installOneSkill
// stages downloads in ".<skill>.tmp-<rand>" and parks the previous version in
// ".<skill>.old-<rand>", removing both once the swap succeeds.
func isTmpLeftoverName(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	return strings.Contains(name, ".tmp-") || strings.Contains(name, ".old-")
}

func collectTmpLeftovers(m *Manifest) []tmpLeftover {
	leftovers := make([]tmpLeftover, 0)
	for _, dir := range doctorScanDirs(m) {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !isTmpLeftoverName(entry.Name()) {
				continue
			}
			leftovers = append(leftovers, tmpLeftover{dir: dir.name, name: entry.Name(), path: filepath.Join(dir.path, entry.Name())})
		}
	}
	return leftovers
}

// pruneTmpLeftovers deletes install leftovers from every scanned target directory.
// It never touches manifest skills, unmanaged skills, or mirror symlinks.
func pruneTmpLeftovers(m *Manifest, dryRun bool) {
	leftovers := collectTmpLeftovers(m)
	if len(leftovers) == 0 {
		fmt.Printf("  %s\n", green("No interrupted-install leftovers to prune."))
		return
	}

	fmt.Println(bold("Prune:"))
	for _, l := range leftovers {
		label := l.dir + "/" + l.name
		if dryRun {
			fmt.Printf("  %-16s %s\n", yellow("would remove"), label)
			continue
		}
		if err := os.RemoveAll(l.path); err != nil {
			warn("prune %s: %v", l.path, err)
			continue
		}
		fmt.Printf("  %-16s %s\n", green("removed"), label)
	}
}

func pruneTmpLeftoversJSON(m *Manifest, dryRun bool) []doctorJSONPruned {
	leftovers := collectTmpLeftovers(m)
	pruned := make([]doctorJSONPruned, 0, len(leftovers))
	for _, l := range leftovers {
		action := "would_remove"
		if !dryRun {
			if err := os.RemoveAll(l.path); err != nil {
				warn("prune %s: %v", l.path, err)
				continue
			}
			action = "removed"
		}
		pruned = append(pruned, doctorJSONPruned{Name: l.name, Directory: l.dir, Action: action})
	}
	return pruned
}
