package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ── test setup — injectable GitHub fakes ─────────────────────────────

func fakeGitHub() {
	fetchLatestCommitFn = func(_, _ string) (string, error) {
		return "fakecommit1234567890123456789012345678901234", nil
	}
	fetchTreeFn = func(_, _ string) (tree []treeEntry, err error) {
		// Return all possible test paths so tests don't depend on specific tree matches
		return []treeEntry{
			{Path: "skills/test/SKILL.md", Mode: "100644", Type: "blob"},
			{Path: "skills/test/README.md", Mode: "100644", Type: "blob"},
			{Path: "skills/new-path/SKILL.md", Mode: "100644", Type: "blob"},
			{Path: "skills/old-path/SKILL.md", Mode: "100644", Type: "blob"},
		}, nil
	}
	downloadFileFn = func(_, _, filePath string) ([]byte, error) {
		name := filepath.Base(filePath)
		return []byte("# " + name), nil
	}
}

func restoreGitHub() {
	fetchLatestCommitFn = fetchLatestCommit
	fetchTreeFn = fetchTree
	downloadFileFn = downloadFile
}

// ── helpers ──────────────────────────────────────────────────────────

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

// ── manifest / lock I/O ──────────────────────────────────────────────

func TestReadManifest(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, ".manifest.json")
	writeJSON(t, mf, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: "~/.agents/skills"},
		},
		Skills: []SkillEntry{
			{
				Name:   "test-skill",
				Target: "shared",
				Source: SourceEntry{Repo: "user/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})

	m, err := readManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Skills) != 1 || m.Skills[0].Name != "test-skill" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestReadManifestMissing(t *testing.T) {
	_, err := readManifest("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestReadLockMissing(t *testing.T) {
	l, err := readLock("/nonexistent/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	if l.Version != 1 {
		t.Fatalf("expected version 1, got %d", l.Version)
	}
	if l.Skills == nil {
		t.Fatal("expected non-nil Skills map")
	}
}

func TestWriteLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	lf := filepath.Join(dir, ".lock.json")

	l := &LockFile{
		Version: 1,
		Updated: "2026-01-01T00:00:00Z",
		Skills: map[string]LockSkill{
			"drawio": {Commit: "abc123", Path: "skills/drawio"},
		},
	}
	if err := writeLock(lf, l); err != nil {
		t.Fatal(err)
	}

	l2, err := readLock(lf)
	if err != nil {
		t.Fatal(err)
	}
	if l2.Skills["drawio"].Commit != "abc123" {
		t.Fatalf("unexpected commit: %s", l2.Skills["drawio"].Commit)
	}
}

// ── path helpers ─────────────────────────────────────────────────────

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		input, expected string
	}{
		{"~/test", filepath.Join(home, "test")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
	}
	for _, tc := range tests {
		got := expandPath(tc.input)
		if got != tc.expected {
			t.Errorf("expandPath(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestResolveTargetPath(t *testing.T) {
	dirs := []DirEntry{
		{Name: "shared", Path: "~/.agents/skills"},
		{Name: "codex", Path: "~/.codex/skills"},
	}

	home, _ := os.UserHomeDir()
	if got := resolveTargetPath("shared", dirs); got != filepath.Join(home, ".agents", "skills") {
		t.Errorf("shared = %q", got)
	}
	if got := resolveTargetPath("codex", dirs); got != filepath.Join(home, ".codex", "skills") {
		t.Errorf("codex = %q", got)
	}
	if got := resolveTargetPath("nonexistent", dirs); got != "" {
		t.Errorf("nonexistent = %q, want empty", got)
	}
}

// ── applySymlinks safety ─────────────────────────────────────────────

func TestApplySymlinks_RealDirNotDeleted(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "target")
	to := filepath.Join(dir, "source")

	// Create a real directory at "from"
	if err := os.MkdirAll(from, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(from, "KEEP"), "important data")

	m := &Manifest{
		Symlinks: []SymlinkEntry{
			{From: from, To: to},
		},
	}
	applySymlinks(m)

	// Real directory should still exist
	if _, err := os.Stat(filepath.Join(from, "KEEP")); err != nil {
		t.Fatal("real directory was deleted!")
	}
}

func TestApplySymlinks_WrongSymlinkReplaced(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "target")
	to1 := filepath.Join(dir, "source1")
	to2 := filepath.Join(dir, "source2")

	// Create source dirs
	os.MkdirAll(to1, 0755)
	os.MkdirAll(to2, 0755)

	// Create wrong symlink
	if err := os.Symlink(to1, from); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Symlinks: []SymlinkEntry{
			{From: from, To: to2},
		},
	}
	applySymlinks(m)

	// Should now point to to2
	existing, err := os.Readlink(from)
	if err != nil {
		t.Fatal(err)
	}
	if existing != to2 {
		t.Fatalf("expected symlink to %q, got %q", to2, existing)
	}
}

func TestApplySymlinks_CorrectSymlinkSkipped(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "target")
	to := filepath.Join(dir, "source")

	os.MkdirAll(to, 0755)
	if err := os.Symlink(to, from); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Symlinks: []SymlinkEntry{
			{From: from, To: to},
		},
	}
	applySymlinks(m)

	existing, err := os.Readlink(from)
	if err != nil {
		t.Fatal(err)
	}
	if existing != to {
		t.Fatalf("symlink changed unexpectedly: %q → %q", to, existing)
	}
}

// ── applyMirrors ───────────────────────────────────────────────

func TestApplyMirrors(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	// Create shared skills
	for _, name := range []string{"drawio", "docx", "pdf"} {
		skillDir := filepath.Join(sharedDir, name)
		os.MkdirAll(skillDir, 0755)
		writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# "+name)
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
			{Name: "docx", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/docx"}},
			{Name: "pdf", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/pdf"}},
		},
	}

	applyMirrors(m)

	// Verify symlinks
	for _, name := range []string{"drawio", "docx", "pdf"} {
		src := filepath.Join(sharedDir, name)
		dst := filepath.Join(claudeDir, name)
		existing, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
		if existing != src {
			t.Fatalf("%s: expected %q, got %q", name, src, existing)
		}
	}
}

func TestApplyMirrors_OrphanCleanup(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	os.MkdirAll(sharedDir, 0755)
	os.MkdirAll(claudeDir, 0755)

	// Create orphan symlink in claude dir
	orphanDir := filepath.Join(sharedDir, "orphan")
	os.MkdirAll(orphanDir, 0755)
	orphanLink := filepath.Join(claudeDir, "orphan")
	if err := os.Symlink(orphanDir, orphanLink); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{},
	}

	applyMirrors(m)

	// Orphan symlink should be removed
	if _, err := os.Stat(orphanLink); err == nil {
		t.Fatal("orphan symlink was not cleaned up")
	}
}

func TestApplyMirrors_RealFileNotReplaced(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	os.MkdirAll(sharedDir, 0755)
	os.MkdirAll(claudeDir, 0755)

	// Create a real file at claude dir (not a symlink)
	realFile := filepath.Join(claudeDir, "drawio")
	writeFile(t, realFile, "real file content")

	// Create shared skill
	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# drawio")

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
		},
	}

	applyMirrors(m)

	// Real file should still exist
	if _, err := os.Stat(realFile); err != nil {
		t.Fatal("real file was replaced by symlink!")
	}
}

func TestApplyMirrors_NoSymlinkForMissingSource(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	os.MkdirAll(sharedDir, 0755)
	os.MkdirAll(claudeDir, 0755)

	// Source skill directory exists but has no SKILL.md
	srcSkill := filepath.Join(sharedDir, "half-installed")
	os.MkdirAll(srcSkill, 0755)

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{Name: "half-installed", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/half"}},
		},
	}

	applyMirrors(m)

	// Should NOT create a symlink for a source without SKILL.md
	dst := filepath.Join(claudeDir, "half-installed")
	if _, err := os.Lstat(dst); err == nil {
		t.Fatal("mirror created symlink for source without SKILL.md")
	}
}

func TestApplyMirrors_ExternalSymlinkNotRemoved(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")
	externalDir := filepath.Join(dir, "external")

	os.MkdirAll(sharedDir, 0755)
	os.MkdirAll(claudeDir, 0755)
	os.MkdirAll(externalDir, 0755)

	// Create a claude-only symlink pointing outside the shared pool
	externalSymlink := filepath.Join(claudeDir, "claude-only")
	if err := os.Symlink(externalDir, externalSymlink); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{},
	}

	applyMirrors(m)

	// External symlink should survive orphan cleanup
	if _, err := os.Stat(externalSymlink); err != nil {
		t.Fatal("external symlink was incorrectly removed by orphan cleanup")
	}
}

// ── installOneSkill skip logic ───────────────────────────────────────

func TestInstallOneSkill_SkipsWhenLockedAndOnDisk(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	os.MkdirAll(sharedDir, 0755)

	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# drawio")

	lock := &LockFile{
		Skills: map[string]LockSkill{
			"drawio": {Commit: "abc123", Path: "skills/drawio"},
		},
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
	}

	result, ls := installOneSkill(
		SkillEntry{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
		lock, m.Directories,
	)

	if result.Action != "ok" || result.Error != "already installed" {
		t.Fatalf("expected skip, got %+v", result)
	}
	if ls != nil {
		t.Fatal("expected no lock update for skip")
	}
}

func TestInstallOneSkill_ReinstallsWhenLockedButDiskMissing(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")

	lock := &LockFile{
		Skills: map[string]LockSkill{
			"test": {Commit: "fakecommit1234567890123456789012345678901234", Path: "skills/test"},
		},
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
	}

	result, ls := installOneSkill(
		SkillEntry{Name: "test", Target: "shared", Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"}},
		lock, m.Directories,
	)

	if result.Action != "ok" {
		t.Fatalf("expected install to succeed with fakes, got %+v", result)
	}
	if ls == nil || ls.Commit != "fakecommit1234567890123456789012345678901234" {
		t.Fatalf("expected lock update with fakecommit, got %+v", ls)
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "test", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not installed: %v", err)
	}
}

func TestInstallOneSkill_EmptyLockWithDisk(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	os.MkdirAll(sharedDir, 0755)

	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# drawio")

	// Lock exists but commit is empty — stale from older version
	lock := &LockFile{
		Skills: map[string]LockSkill{
			"drawio": {Commit: "", Path: "skills/drawio"},
		},
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
	}

	result, ls := installOneSkill(
		SkillEntry{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
		lock, m.Directories,
	)

	// Should skip (disk has SKILL.md) and NOT fill commit
	if result.Action != "ok" || result.Error != "already installed" {
		t.Fatalf("expected skip, got %+v", result)
	}
	if ls != nil {
		t.Fatal("should NOT fill commit for empty lock (would cause staleness)")
	}
}

// ── updateOneSkill path change detection ─────────────────────────────

func TestUpdateOneSkill_PathChangeTriggersUpdate(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")

	// Lock says old path with a specific commit
	lock := &LockFile{
		Skills: map[string]LockSkill{
			"test": {Commit: "abc123", Path: "skills/old-path"},
		},
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
	}

	// Path changed, but fake commit matches locked commit → should still skip?
	// No — the path differs, so updateOneSkill must NOT skip
	result, _ := updateOneSkill(
		SkillEntry{Name: "test", Target: "shared", Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/new-path"}},
		lock, m.Directories,
	)

	if result.Action != "ok" {
		t.Fatalf("expected update to succeed (path differs, should reinstall), got %+v", result)
	}
}

func TestUpdateOneSkill_SkipsWhenPathAndCommitMatch_Integration(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	skillDir := filepath.Join(sharedDir, "test")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# test")

	lock := &LockFile{
		Skills: map[string]LockSkill{
			"test": {Commit: "fakecommit1234567890123456789012345678901234", Path: "skills/test"},
		},
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
	}

	// Commit and path both match → should skip
	result, _ := updateOneSkill(
		SkillEntry{Name: "test", Target: "shared", Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"}},
		lock, m.Directories,
	)

	if result.Action != "ok" || result.Error != "already installed" {
		t.Fatalf("expected skip (commit+path match), got %+v", result)
	}
}

// ── util / edge cases ────────────────────────────────────────────────

func TestGetLockPath(t *testing.T) {
	got := getLockPath("/home/user/.config/skills/.manifest.json")
	expected := "/home/user/.config/skills/.lock.json"
	if got != expected {
		t.Fatalf("getLockPath(%q) = %q, want %q", "/home/user/...", got, expected)
	}
}

func TestInstallOneSkill_NoFilesFoundFails(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")

	result := InstallSkill(
		SkillEntry{Name: "test", Source: SourceEntry{Repo: "anthropics/skills", Ref: "main", Path: "skills/definitely-does-not-exist"}},
		filepath.Join(sharedDir, "test"), "",
	)

	if result.Action != "failed" {
		t.Fatalf("expected failure for nonexistent source path, got %+v", result)
	}
}

// fakeGitHubRoot returns a fake GitHub that has root-level files
// (paths without a directory prefix), used to test root source paths.
func fakeGitHubRoot() {
	fetchLatestCommitFn = func(_, _ string) (string, error) {
		return "fakecommit1234567890123456789012345678901234", nil
	}
	fetchTreeFn = func(_, _ string) (tree []treeEntry, err error) {
		return []treeEntry{
			{Path: "SKILL.md", Mode: "100644", Type: "blob"},
			{Path: "README.md", Mode: "100644", Type: "blob"},
			{Path: "scripts/anysearch_cli.sh", Mode: "100755", Type: "blob"},
		}, nil
	}
	downloadFileFn = func(_, _, filePath string) ([]byte, error) {
		name := filepath.Base(filePath)
		return []byte("# " + name), nil
	}
}

func TestInstallSkill_RootPathWithDot_Succeeds(t *testing.T) {
	fakeGitHubRoot()
	defer restoreGitHub()

	result := InstallSkill(
		SkillEntry{Name: "anysearch", Source: SourceEntry{Repo: "anysearch-ai/anysearch-skill", Ref: "main", Path: "."}},
		t.TempDir(), "",
	)

	if result.Action != "ok" {
		t.Fatalf("expected success for root path '.', got %+v", result)
	}
}

func TestInstallSkill_RootPathWithEmptyString_Succeeds(t *testing.T) {
	fakeGitHubRoot()
	defer restoreGitHub()

	result := InstallSkill(
		SkillEntry{Name: "anysearch", Source: SourceEntry{Repo: "anysearch-ai/anysearch-skill", Ref: "main", Path: ""}},
		t.TempDir(), "",
	)

	if result.Action != "ok" {
		t.Fatalf("expected success for empty root path, got %+v", result)
	}
}

// ── cmdUpdate integration ─────────────────────────────────────────

func TestCmdUpdate_OutdatedDetectedAndInstalled(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// Create disk state: SKILL.md exists
	skillDir := filepath.Join(sharedDir, "test")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# test (old)")

	// Write manifest
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name:   "test",
				Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})

	// Write lock with OLD commit — fakeGitHub returns "fakecommit123..."
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test": {Commit: "oldcommit1234567890123456789012345678901234", Path: "skills/test"},
		},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	// Run cmdUpdate: yes=true (skip confirm), dryRun=false
	oldQuiet := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", false, true)
	quiet = oldQuiet

	// Lock should be updated with fake commit
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	ls, ok := lock2.Skills["test"]
	if !ok {
		t.Fatal("test skill missing from lock after update")
	}
	if ls.Commit != "fakecommit1234567890123456789012345678901234" {
		t.Fatalf("expected fake commit, got %q", ls.Commit)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal("SKILL.md missing after update")
	}
}

func TestCmdUpdate_DryRunDoesNotModify(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	skillDir := filepath.Join(sharedDir, "test")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# test (old)")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name:   "test",
				Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})

	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test": {Commit: "oldcommit1234567890123456789012345678901234", Path: "skills/test"},
		},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	// Dry run
	oldQuiet := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", true, true)
	quiet = oldQuiet

	// Lock should still have OLD commit
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	ls, ok := lock2.Skills["test"]
	if !ok {
		t.Fatal("test skill missing from lock after dry-run")
	}
	if ls.Commit != "oldcommit1234567890123456789012345678901234" {
		t.Fatalf("dry-run should not modify lock, got commit %q", ls.Commit)
	}
}

func TestIsRateLimit(t *testing.T) {
	if !isRateLimit(fmt.Errorf("HTTP 403")) {
		t.Fatal("should detect 403")
	}
	if !isRateLimit(fmt.Errorf("rate limit exceeded")) {
		t.Fatal("should detect rate limit string")
	}
	if isRateLimit(fmt.Errorf("HTTP 404")) {
		t.Fatal("should not detect 404 as rate limit")
	}
	if isRateLimit(nil) {
		t.Fatal("nil should not be rate limit")
	}
}

// ── validateSkillName ────────────────────────────────────────────────

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"foo/bar", false},
		{"foo\\bar", false},
		{"a\x00b", false},
		{"normal-name", true},
		{"very_long.name_with-dots", true},
		{"a", true},
	}
	for _, tc := range tests {
		err := validateSkillName(tc.name)
		if tc.valid && err != nil {
			t.Errorf("validateSkillName(%q) = %v, want nil", tc.name, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("validateSkillName(%q) = nil, want error", tc.name)
		}
	}
}

// ── writeManifest ────────────────────────────────────────────────────

func TestWriteManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, ".manifest.json")

	m := &Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: "~/.agents/skills", Comment: "main pool"},
			{Name: "codex", Path: "~/.codex/skills"},
		},
		Symlinks: []SymlinkEntry{
			{From: "~/.codex/skills", To: "~/.agents/skills"},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{
				Name: "drawio", Target: "shared",
				Source: SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/drawio"},
				Note: "test skill",
			},
		},
	}

	if err := writeManifest(mf, m); err != nil {
		t.Fatal(err)
	}

	m2, err := readManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 || m2.Skills[0].Name != "drawio" {
		t.Fatalf("unexpected skills: %+v", m2.Skills)
	}
	if len(m2.Mirrors) != 1 || m2.Mirrors[0].From != "shared" {
		t.Fatalf("mirrors lost: %+v", m2.Mirrors)
	}
	if len(m2.Symlinks) != 1 || m2.Symlinks[0].From != "~/.codex/skills" {
		t.Fatalf("symlinks lost: %+v", m2.Symlinks)
	}
}

// ── cmdRemove ────────────────────────────────────────────────────────

func setupRemoveTest(t *testing.T) (string, *Manifest, *LockFile) {
	t.Helper()
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// Create shared skill
	skillDir := filepath.Join(sharedDir, "test-skill")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# test-skill")

	// Create mirror symlink
	os.MkdirAll(claudeDir, 0755)
	if err := os.Symlink(skillDir, filepath.Join(claudeDir, "test-skill")); err != nil {
		t.Fatal(err)
	}

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{
				Name: "test-skill", Target: "shared",
				Source: SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/test"},
			},
		},
	})

	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test-skill": {Commit: "abc123", Path: "skills/test"},
		},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	return manifestPath, m, lock
}

func TestCmdRemove_RemovesFromAllLayers(t *testing.T) {
	manifestPath, m, lock := setupRemoveTest(t)
	oldQ := quiet
	quiet = true
	cmdRemove(m, lock, manifestPath, "test-skill", false, false)
	quiet = oldQ

	// Manifest should not have test-skill
	for _, s := range m.Skills {
		if s.Name == "test-skill" {
			t.Fatal("manifest still has test-skill")
		}
	}
	// Lock should not have test-skill
	if _, ok := lock.Skills["test-skill"]; ok {
		t.Fatal("lock still has test-skill")
	}
	// Disk should be gone
	if _, err := os.Stat(filepath.Join(filepath.Dir(manifestPath), "shared", "test-skill", "SKILL.md")); err == nil {
		t.Fatal("disk skill directory still exists")
	}
	// Mirror symlink should be gone
	claudeDir := filepath.Join(filepath.Dir(manifestPath), "claude")
	if _, err := os.Lstat(filepath.Join(claudeDir, "test-skill")); err == nil {
		t.Fatal("mirror symlink still exists")
	}
}

func TestCmdRemove_KeepManifest(t *testing.T) {
	manifestPath, m, lock := setupRemoveTest(t)
	oldQ := quiet
	quiet = true
	cmdRemove(m, lock, manifestPath, "test-skill", true, false)
	quiet = oldQ

	// Manifest should still have test-skill
	found := false
	for _, s := range m.Skills {
		if s.Name == "test-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("manifest should still have test-skill (keep-manifest)")
	}
	// Lock should not have test-skill
	if _, ok := lock.Skills["test-skill"]; ok {
		t.Fatal("lock still has test-skill")
	}
	// Disk should be gone
	sharedDir := filepath.Join(filepath.Dir(manifestPath), "shared")
	if _, err := os.Stat(filepath.Join(sharedDir, "test-skill", "SKILL.md")); err == nil {
		t.Fatal("disk skill directory still exists")
	}
}

func TestCmdRemove_DryRun(t *testing.T) {
	manifestPath, m, lock := setupRemoveTest(t)
	oldQ := quiet
	quiet = true
	cmdRemove(m, lock, manifestPath, "test-skill", false, true)
	quiet = oldQ

	// Nothing should be modified
	found := false
	for _, s := range m.Skills {
		if s.Name == "test-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dry-run should not modify manifest")
	}
	if _, ok := lock.Skills["test-skill"]; !ok {
		t.Fatal("dry-run should not modify lock")
	}
	sharedDir := filepath.Join(filepath.Dir(manifestPath), "shared")
	if _, err := os.Stat(filepath.Join(sharedDir, "test-skill", "SKILL.md")); err != nil {
		t.Fatal("dry-run should not delete disk")
	}
}

// ── cmdUpdate state coverage ─────────────────────────────────────────

func TestCmdUpdate_UninstalledDetectedAndInstalled(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name: "test", Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{Version: 1, Skills: map[string]LockSkill{}})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", false, true)
	quiet = oldQ

	lock2, _ := readLock(lockPath)
	if _, ok := lock2.Skills["test"]; !ok {
		t.Fatal("uninstalled skill should be installed by update")
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "test", "SKILL.md")); err != nil {
		t.Fatal("SKILL.md missing after install")
	}
}

func TestCmdUpdate_Degraded(t *testing.T) {
	fetchLatestCommitFn = func(_, _ string) (string, error) {
		return "", fmt.Errorf("network error")
	}
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	skillDir := filepath.Join(sharedDir, "test")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# test")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name: "test", Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test": {Commit: "abc123", Path: "skills/test"},
		},
	})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", false, true)
	quiet = oldQ

	// Lock should NOT have changed (degraded)
	lock2, _ := readLock(lockPath)
	if lock2.Skills["test"].Commit != "abc123" {
		t.Fatal("degraded should not modify lock")
	}
}

func TestCmdUpdate_StaleLockCleaned(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{}, // empty
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"stale-skill": {Commit: "abc123", Path: "skills/stale"},
		},
	})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", false, true)
	quiet = oldQ

	lock2, _ := readLock(lockPath)
	if _, ok := lock2.Skills["stale-skill"]; ok {
		t.Fatal("stale lock entry was not cleaned")
	}
}

func TestCmdUpdate_StaleDisk(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// Create disk skill with SKILL.md
	os.MkdirAll(filepath.Join(sharedDir, "test"), 0755)
	writeFile(t, filepath.Join(sharedDir, "test", "SKILL.md"), "# test")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name: "test", Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})
	// Lock exists but empty — stale-disk
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{Version: 1, Skills: map[string]LockSkill{}})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	// Should be detectable (no error, just dry-run to see the state)
	oldQ := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", true, true)
	quiet = oldQ
	// No crash = test passes; stale-disk should not cause panic
}

func TestCmdUpdate_OrphanDetected(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// Create orphan skill directory with SKILL.md
	os.MkdirAll(filepath.Join(sharedDir, "orphan-skill"), 0755)
	writeFile(t, filepath.Join(sharedDir, "orphan-skill", "SKILL.md"), "# orphan")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{Version: 1, Skills: map[string]LockSkill{}})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdUpdate(m, lock, manifestPath, "", true, true)
	quiet = oldQ

	// Orphan should NOT be auto-deleted
	if _, err := os.Stat(filepath.Join(sharedDir, "orphan-skill", "SKILL.md")); err != nil {
		t.Fatal("orphan was incorrectly deleted by dry-run")
	}
}

// ── cmdInstall bulk ──────────────────────────────────────────────────

func TestCmdInstall_BulkWithMultipleSkills(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// One skill already installed + locked
	os.MkdirAll(filepath.Join(sharedDir, "existing"), 0755)
	writeFile(t, filepath.Join(sharedDir, "existing", "SKILL.md"), "# existing")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name: "existing", Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/test"},
			},
			{
				Name: "new", Target: "shared",
				Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/new-path"},
			},
		},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"existing": {Commit: "fakecommit1234567890123456789012345678901234", Path: "skills/test"},
		},
	})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdInstall(m, lock, manifestPath, "")
	quiet = oldQ

	// Both should be on disk
	if _, err := os.Stat(filepath.Join(sharedDir, "existing", "SKILL.md")); err != nil {
		t.Fatal("existing skill removed")
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "new", "SKILL.md")); err != nil {
		t.Fatal("new skill not installed")
	}
	// Lock should have both
	lock2, _ := readLock(lockPath)
	if _, ok := lock2.Skills["existing"]; !ok {
		t.Fatal("existing not in lock")
	}
	if _, ok := lock2.Skills["new"]; !ok {
		t.Fatal("new not in lock")
	}
}

// ── cmdInfo ──────────────────────────────────────────────────────────

func TestCmdInfo_ShowsDetails(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name: "test", Target: "shared",
				Source: SourceEntry{Repo: "user/repo", Ref: "main", Path: "skills/test"},
			},
		},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test": {Commit: "deadbeef1234567890123456789012345678901234", Path: "skills/test"},
		},
	})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdInfo(m, lock, "test")
	quiet = oldQ
	// No panic = test passes
}

// ── cmdVerify deprecated ─────────────────────────────────────────────

// ── applySymlinks ────────────────────────────────────────────────────

func TestApplySymlinks_CreateNew(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "target")
	to := filepath.Join(dir, "source")

	os.MkdirAll(to, 0755)

	m := &Manifest{
		Symlinks: []SymlinkEntry{
			{From: from, To: to},
		},
	}
	applySymlinks(m)

	existing, err := os.Readlink(from)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if existing != to {
		t.Fatalf("expected %q, got %q", to, existing)
	}
}

// ── applyMirrors ─────────────────────────────────────────────────────

func TestApplyMirrors_MigrationFromBlanketSymlink(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	// Old blanket symlink: claude → shared
	os.MkdirAll(sharedDir, 0755)
	if err := os.Symlink(sharedDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	// Create a shared skill
	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# drawio")

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
		},
	}

	applyMirrors(m)

	// claude dir should now be a real directory (not a symlink)
	fi, err := os.Lstat(claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("blanket symlink was not replaced with a real directory")
	}
	// Mirror symlink should exist inside claude dir
	dst := filepath.Join(claudeDir, "drawio")
	if existing, err := os.Readlink(dst); err != nil || existing != skillDir {
		t.Fatalf("mirror symlink not created: err=%v, link=%q", err, existing)
	}
}

// ── installOneSkill ──────────────────────────────────────────────────

func TestInstallOneSkill_PathMismatchReinstall(t *testing.T) {
	fakeGitHub()
	defer restoreGitHub()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	os.MkdirAll(sharedDir, 0755)

	// Lock says old path
	lock := &LockFile{
		Skills: map[string]LockSkill{
			"test": {Commit: "fakecommit1234567890123456789012345678901234", Path: "skills/old-path"},
		},
	}
	dirs := []DirEntry{{Name: "shared", Path: sharedDir}}

	// Manifest says new path
	skill := SkillEntry{
		Name: "test", Target: "shared",
		Source: SourceEntry{Repo: "fake/repo", Ref: "main", Path: "skills/new-path"},
	}

	result, ls := installOneSkill(skill, lock, dirs)
	if result.Action != "ok" {
		t.Fatalf("install should succeed with path mismatch, got %+v", result)
	}
	if ls == nil || ls.Path != "skills/new-path" {
		t.Fatalf("lock should record new path, got %+v", ls)
	}
}

// ── atomicWriteFile ──────────────────────────────────────────────────

func TestAtomicWriteFile_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested", "file.json")
	data := []byte(`{"key": "value"}`)

	if err := atomicWriteFile(path, data, 0644); err != nil {
		t.Fatalf("atomicWriteFile with nested dirs: %v", err)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}
