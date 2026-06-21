package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdDoctor(m *Manifest, lock *LockFile, manifestPath string) {
	items := collectDoctorItems(m, lock)

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
	for _, item := range items {
		statusColor := doctorStatusLabel(item.Status)
		if item.Status == "ok" {
			okCount++
		} else {
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
}

func doctorStatusLabel(status string) string {
	switch status {
	case "ok":
		return green("ok")
	case "invalid-target", "missing", "path-changed", "stale-disk", "stale", "orphan", "mirror-missing", "mirror-wrong-link", "mirror-conflict", "mirror-orphan":
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
			if manifestNames[name] {
				continue
			}
			if _, inLock := lock.Skills[name]; inLock {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir.path, name, "SKILL.md")); err == nil {
				items = append(items, auditItem{name, "orphan", fmt.Sprintf("only on disk in %s", dir.name)})
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
					Status: "mirror-orphan",
					Detail: fmt.Sprintf("managed symlink remains after %s→%s drift", mirror.From, mirror.To),
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
