package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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

func installFakeOMP(t *testing.T, agentDir string) {
	t.Helper()
	binDir := t.TempDir()
	ompPath := filepath.Join(binDir, "omp")
	writeFile(t, ompPath, "#!/bin/sh\nprintf '%s\\n' \""+agentDir+"\"\n")
	if err := os.Chmod(ompPath, 0o755); err != nil {
		t.Fatal(err)
	}
	pathEnv := binDir
	if oldPath := os.Getenv("PATH"); oldPath != "" {
		pathEnv += string(os.PathListSeparator) + oldPath
	}
	t.Setenv("PATH", pathEnv)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	tmpf := filepath.Join(t.TempDir(), "stdout")
	old := os.Stdout
	f, err := os.Create(tmpf)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = f
	defer func() {
		os.Stdout = old
	}()

	fn()

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(tmpf)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
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

	t.Run("omp via omp config path", func(t *testing.T) {
		agentDir := filepath.Join(t.TempDir(), "profile-agent")
		installFakeOMP(t, agentDir)
		if got := resolveTargetPath("omp", dirs); got != filepath.Join(agentDir, "skills") {
			t.Fatalf("omp = %q, want %q", got, filepath.Join(agentDir, "skills"))
		}
	})

	t.Run("omp fallback honors PI_CONFIG_DIR", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("PI_CONFIG_DIR", ".config/omp")
		if got := resolveTargetPath("omp", dirs); got != filepath.Join(home, ".config", "omp", "agent", "skills") {
			t.Fatalf("omp fallback = %q", got)
		}
	})
}

// ── applySymlinks safety ─────────────────────────────────────────────

func TestApplySymlinks_RealDirNotDeleted(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "target")
	to := filepath.Join(dir, "source")

	// Create a real directory at "from"
	if err := os.MkdirAll(from, 0o755); err != nil {
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
	os.MkdirAll(to1, 0o755)
	os.MkdirAll(to2, 0o755)

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

	os.MkdirAll(to, 0o755)
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
		os.MkdirAll(skillDir, 0o755)
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

func TestApplyMirrors_Exclude(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	// Create shared skills
	for _, name := range []string{"drawio", "anysearch"} {
		skillDir := filepath.Join(sharedDir, name)
		os.MkdirAll(skillDir, 0o755)
		writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# "+name)
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude", Exclude: []string{"anysearch"}},
		},
		Skills: []SkillEntry{
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
			{Name: "anysearch", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/anysearch"}},
		},
	}

	// Create a pre-existing anysearch symlink to test that it gets cleaned up
	preExistingLink := filepath.Join(claudeDir, "anysearch")
	os.MkdirAll(claudeDir, 0o755)
	if err := os.Symlink(filepath.Join(sharedDir, "anysearch"), preExistingLink); err != nil {
		t.Fatal(err)
	}

	applyMirrors(m)

	// Verify drawio is mirrored
	drawioDst := filepath.Join(claudeDir, "drawio")
	if _, err := os.Readlink(drawioDst); err != nil {
		t.Fatalf("expected drawio symlink: %v", err)
	}

	// Verify anysearch is NOT mirrored and pre-existing is cleaned up
	if _, err := os.Readlink(preExistingLink); err == nil {
		t.Fatal("expected anysearch symlink to be deleted (excluded)")
	}
}

func TestApplyMirrors_OrphanCleanup(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")

	os.MkdirAll(sharedDir, 0o755)
	os.MkdirAll(claudeDir, 0o755)

	// Create orphan symlink in claude dir
	orphanDir := filepath.Join(sharedDir, "orphan")
	os.MkdirAll(orphanDir, 0o755)
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

	os.MkdirAll(sharedDir, 0o755)
	os.MkdirAll(claudeDir, 0o755)

	// Create a real file at claude dir (not a symlink)
	realFile := filepath.Join(claudeDir, "drawio")
	writeFile(t, realFile, "real file content")

	// Create shared skill
	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0o755)
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

	os.MkdirAll(sharedDir, 0o755)
	os.MkdirAll(claudeDir, 0o755)

	// Source skill directory exists but has no SKILL.md
	srcSkill := filepath.Join(sharedDir, "half-installed")
	os.MkdirAll(srcSkill, 0o755)

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

	os.MkdirAll(sharedDir, 0o755)
	os.MkdirAll(claudeDir, 0o755)
	os.MkdirAll(externalDir, 0o755)

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
	os.MkdirAll(sharedDir, 0o755)

	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0o755)
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
	os.MkdirAll(sharedDir, 0o755)

	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0o755)
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
	os.MkdirAll(skillDir, 0o755)
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
	os.MkdirAll(skillDir, 0o755)
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
	os.MkdirAll(skillDir, 0o755)
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
				Note:   "test skill",
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
	os.MkdirAll(skillDir, 0o755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# test-skill")

	// Create mirror symlink
	os.MkdirAll(claudeDir, 0o755)
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
	cmdRemove(m, lock, manifestPath, []string{"test-skill"}, false, false)
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
	cmdRemove(m, lock, manifestPath, []string{"test-skill"}, true, false)
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
	cmdRemove(m, lock, manifestPath, []string{"test-skill"}, false, true)
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
	os.MkdirAll(skillDir, 0o755)
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
	os.MkdirAll(filepath.Join(sharedDir, "test"), 0o755)
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
	os.MkdirAll(filepath.Join(sharedDir, "orphan-skill"), 0o755)
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
	os.MkdirAll(filepath.Join(sharedDir, "existing"), 0o755)
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
	cmdInstall(m, lock, manifestPath, "", false)
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

	os.MkdirAll(to, 0o755)

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
	os.MkdirAll(sharedDir, 0o755)
	if err := os.Symlink(sharedDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	// Create a shared skill
	skillDir := filepath.Join(sharedDir, "drawio")
	os.MkdirAll(skillDir, 0o755)
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
	os.MkdirAll(sharedDir, 0o755)

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

	if err := atomicWriteFile(path, data, 0o644); err != nil {
		t.Fatalf("atomicWriteFile with nested dirs: %v", err)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// ── corrupted lock ───────────────────────────────────────────────────

func TestReadLockCorrupted(t *testing.T) {
	dir := t.TempDir()
	lf := filepath.Join(dir, ".lock.json")

	// Missing trailing comma after updated_at (common corruption)
	corrupt := `{
  "version": 1,
  "updated_at": "2026-05-31T13:20:34+08:00"
  "skills": {}
}`
	if err := os.WriteFile(lf, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := readLock(lf)
	if err == nil {
		t.Fatal("expected error for corrupted lock file, got nil")
	}
	if l != nil {
		t.Fatalf("expected nil lock on error, got %+v", l)
	}
}

// ── field preservation (unknown JSON fields round-trip) ──────────────

func TestSourceEntryPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, ".manifest.json")

	// Write manifest with extra "type" field in source
	raw := `{
  "version": 1,
  "directories": [],
  "skills": [
    {
      "name": "test",
      "target": "shared",
      "source": {
        "type": "github-dir",
        "repo": "a/b",
        "ref": "main",
        "path": "skills/test"
      }
    }
  ]
}`
	writeFile(t, mf, raw)

	m, err := readManifest(mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(m.Skills))
	}

	// Round-trip: write then read back
	if err := writeManifest(mf, m); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(mf)
	if err != nil {
		t.Fatal(err)
	}

	// The "type": "github-dir" must survive the round-trip
	if !strings.Contains(string(data), `"type"`) {
		t.Fatal(`"type" field was stripped from source during writeManifest`)
	}
	if !strings.Contains(string(data), `"github-dir"`) {
		t.Fatal(`"github-dir" value was stripped from source during writeManifest`)
	}
}

func TestSkillEntryPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, ".manifest.json")

	raw := `{
  "version": 1,
  "directories": [],
  "skills": [
    {
      "name": "test",
      "target": "shared",
      "category": "network",
      "source": {
        "repo": "a/b",
        "ref": "main",
        "path": "skills/test"
      }
    }
  ]
}`
	writeFile(t, mf, raw)

	m, err := readManifest(mf)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(mf, m); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(mf)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), `"category"`) {
		t.Fatal(`"category" field was stripped from SkillEntry during writeManifest`)
	}
}

func TestCmdRemovePreservesOtherSkillFields(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// Create disk dirs for both skills
	for _, name := range []string{"keep-me", "remove-me"} {
		skillDir := filepath.Join(sharedDir, name)
		os.MkdirAll(skillDir, 0o755)
		writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# "+name)
	}

	// Write manifest with "type" field in source for BOTH skills
	raw := fmt.Sprintf(`{
  "version": 1,
  "directories": [
    { "name": "shared", "path": %q }
  ],
  "skills": [
    {
      "name": "keep-me",
      "target": "shared",
      "source": {
        "type": "github-dir",
        "repo": "a/b",
        "ref": "main",
        "path": "skills/keep-me"
      }
    },
    {
      "name": "remove-me",
      "target": "shared",
      "source": {
        "type": "github-dir",
        "repo": "a/b",
        "ref": "main",
        "path": "skills/remove-me"
      }
    }
  ]
}`, sharedDir)
	writeFile(t, manifestPath, raw)

	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"keep-me":   {Commit: "abc123", Path: "skills/keep-me"},
			"remove-me": {Commit: "def456", Path: "skills/remove-me"},
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

	oldQ := quiet
	quiet = true
	cmdRemove(m, lock, manifestPath, []string{"remove-me"}, false, false)
	quiet = oldQ

	// Re-read manifest from disk
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	// "remove-me" should be gone
	for _, s := range m2.Skills {
		if s.Name == "remove-me" {
			t.Fatal("remove-me still in manifest after cmdRemove")
		}
	}

	// "keep-me" should still exist
	var keepMe *SkillEntry
	for _, s := range m2.Skills {
		if s.Name == "keep-me" {
			keepMe = &s
			break
		}
	}
	if keepMe == nil {
		t.Fatal("keep-me was removed from manifest")
	}

	// "keep-me" must still have its "type" field preserved
	rawAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// Count occurrences: should appear exactly once (for keep-me), not zero
	count := strings.Count(string(rawAfter), `"type": "github-dir"`)
	if count != 1 {
		t.Fatalf("expected 1 'type: github-dir' after remove, got %d.\nmanifest:\n%s", count, string(rawAfter))
	}
}

func TestCompleteNamesFlag(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, ".manifest.json")

	writeJSON(t, mf, Manifest{
		Version:     1,
		Directories: []DirEntry{},
		Skills: []SkillEntry{
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/drawio"}},
			{Name: "docx", Target: "shared", Source: SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/docx"}},
			{Name: "pdf", Target: "shared", Source: SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/pdf"}},
		},
	})

	// Set SKILLS_MANIFEST so completeNames can find it in test context
	t.Setenv("SKILLS_MANIFEST", mf)
	// Capture stdout via temp file
	tmpf := filepath.Join(dir, "out")
	old := os.Stdout
	f, err := os.Create(tmpf)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = f

	completeNames("")

	f.Close()
	os.Stdout = old

	out, _ := os.ReadFile(tmpf)
	names := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d: %v", len(names), names)
	}

	got := make(map[string]bool)
	for _, n := range names {
		got[n] = true
	}
	for _, want := range []string{"drawio", "docx", "pdf"} {
		if !got[want] {
			t.Fatalf("missing skill name %q in --complete-names output: %v", want, names)
		}
	}
}

func TestCmdRemove_MultipleSkills(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	// Create disk dirs for three skills
	for _, name := range []string{"drawio", "docx", "pdf"} {
		skillDir := filepath.Join(sharedDir, name)
		os.MkdirAll(skillDir, 0o755)
		writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# "+name)
	}

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/drawio"}},
			{Name: "docx", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/docx"}},
			{Name: "pdf", Target: "shared", Source: SourceEntry{Repo: "a/b", Path: "skills/pdf"}},
		},
	})

	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"drawio": {Commit: "abc123", Path: "skills/drawio"},
			"docx":   {Commit: "def456", Path: "skills/docx"},
			"pdf":    {Commit: "ghi789", Path: "skills/pdf"},
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

	oldQ := quiet
	quiet = true
	cmdRemove(m, lock, manifestPath, []string{"drawio", "pdf"}, false, false)
	quiet = oldQ

	// Re-read from disk
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	// drawio and pdf should be gone
	for _, name := range []string{"drawio", "pdf"} {
		for _, s := range m2.Skills {
			if s.Name == name {
				t.Fatalf("%s still in manifest after multi-remove", name)
			}
		}
		if _, ok := lock2.Skills[name]; ok {
			t.Fatalf("%s still in lock after multi-remove", name)
		}
		if _, err := os.Stat(filepath.Join(sharedDir, name, "SKILL.md")); err == nil {
			t.Fatalf("%s still on disk after multi-remove", name)
		}
	}

	// docx should remain
	found := false
	for _, s := range m2.Skills {
		if s.Name == "docx" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("docx was removed from manifest")
	}
	if _, ok := lock2.Skills["docx"]; !ok {
		t.Fatal("docx was removed from lock")
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "docx", "SKILL.md")); err != nil {
		t.Fatal("docx was removed from disk")
	}
}

// ── add command ───────────────────────────────────────────────────────

func TestParseAddArgs_Valid(t *testing.T) {
	t.Run("basic owner/repo", func(t *testing.T) {
		o, err := parseAddArgs([]string{"owner/repo"})
		if err != nil {
			t.Fatal(err)
		}
		if o.Repo != "owner/repo" || o.Path != "." || o.Ref != "main" || o.Target != "shared" {
			t.Fatalf("unexpected options: %+v", o)
		}
	})
	t.Run("with path", func(t *testing.T) {
		o, err := parseAddArgs([]string{"owner/repo", "skills/myskill"})
		if err != nil {
			t.Fatal(err)
		}
		if o.Repo != "owner/repo" || o.Path != "skills/myskill" {
			t.Fatalf("unexpected options: %+v", o)
		}
	})
	t.Run("with all flags", func(t *testing.T) {
		o, err := parseAddArgs([]string{
			"owner/repo", "--name", "myname", "--ref", "dev",
			"--target", "claude", "--files", "SKILL.md=README.md",
			"--no-install",
		})
		if err != nil {
			t.Fatal(err)
		}
		if o.Name != "myname" || o.Ref != "dev" || o.Target != "claude" {
			t.Fatalf("unexpected options: %+v", o)
		}
		if o.FilesSpec != "SKILL.md=README.md" || !o.NoInstall {
			t.Fatalf("unexpected options: %+v", o)
		}
	})
	t.Run("flag=value syntax", func(t *testing.T) {
		o, err := parseAddArgs([]string{"owner/repo", "--name=testname", "--ref=develop"})
		if err != nil {
			t.Fatal(err)
		}
		if o.Name != "testname" || o.Ref != "develop" {
			t.Fatalf("unexpected options: %+v", o)
		}
	})
}

func TestParseAddArgs_Invalid(t *testing.T) {
	t.Run("missing repo", func(t *testing.T) {
		_, err := parseAddArgs([]string{})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("invalid repo format", func(t *testing.T) {
		_, err := parseAddArgs([]string{"invalid"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("repo with URL", func(t *testing.T) {
		_, err := parseAddArgs([]string{"https://github.com/owner/repo.git"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("repo with spaces", func(t *testing.T) {
		_, err := parseAddArgs([]string{"owner /repo"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		_, err := parseAddArgs([]string{"owner/repo", "--bogus", "x"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("flag missing value", func(t *testing.T) {
		_, err := parseAddArgs([]string{"owner/repo", "--name"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("extra positional", func(t *testing.T) {
		_, err := parseAddArgs([]string{"owner/repo", "path1", "path2"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCmdAdd_GitHubDir_Success(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "skills")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	fakeGitHub()
	defer restoreGitHub()

	opts := addOptions{
		Repo:   "user/repo",
		Path:   "skills/test",
		Ref:    "main",
		Target: "shared",
	}

	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, false)
	quiet = oldQ

	// Verify manifest has the skill
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 || m2.Skills[0].Name != "test" {
		t.Fatalf("unexpected manifest skills: %+v", m2.Skills)
	}
	if m2.Skills[0].Source.Repo != "user/repo" || m2.Skills[0].Source.Path != "skills/test" {
		t.Fatalf("unexpected source: %+v", m2.Skills[0].Source)
	}

	// Verify lock has entry with commit and sourceHash
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	ls, ok := lock2.Skills["test"]
	if !ok {
		t.Fatal("test not in lock")
	}
	if ls.Commit != "fakecommit1234567890123456789012345678901234" {
		t.Fatalf("unexpected commit: %q", ls.Commit)
	}
	if ls.SourceHash == "" {
		t.Fatal("expected sourceHash in lock")
	}
	if ls.Path != "skills/test" {
		t.Fatalf("unexpected path: %q", ls.Path)
	}

	// Verify SKILL.md on disk
	targetPath := resolveTargetPath("shared", m2.Directories)
	if _, err := os.Stat(filepath.Join(expandPath(targetPath), "test", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not on disk: %v", err)
	}
}

func TestCmdAdd_OMPTarget_Success(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "omp-agent")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: filepath.Join(dir, "shared")},
		},
		Skills: []SkillEntry{},
	})
	installFakeOMP(t, agentDir)

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	fakeGitHub()
	defer restoreGitHub()

	opts := addOptions{
		Repo:   "user/repo",
		Path:   "skills/test",
		Ref:    "main",
		Target: "omp",
	}

	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, false)
	quiet = oldQ

	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 || m2.Skills[0].Target != "omp" {
		t.Fatalf("unexpected manifest skills: %+v", m2.Skills)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "skills", "test", "SKILL.md")); err != nil {
		t.Fatalf("OMP target SKILL.md not on disk: %v", err)
	}
}

func TestCmdAdd_GitHubDir_DryRun(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "skills")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	opts := addOptions{
		Repo:   "user/repo",
		Path:   "skills/test",
		Ref:    "main",
		Target: "shared",
	}

	// Dry-run — should not write manifest, lock, or disk
	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, true)
	quiet = oldQ

	// Manifest should be unchanged (empty)
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 0 {
		t.Fatal("dry-run should not modify manifest")
	}

	// Lock should be unchanged
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock2.Skills) != 0 {
		t.Fatal("dry-run should not modify lock")
	}

	// No disk directory should exist
	targetPath := resolveTargetPath("shared", m2.Directories)
	if _, err := os.Stat(filepath.Join(expandPath(targetPath), "test", "SKILL.md")); err == nil {
		t.Fatal("dry-run should not create files on disk")
	}
}

func TestCmdAdd_GitHubFiles(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "skills")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	fakeGitHub()
	defer restoreGitHub()

	opts := addOptions{
		Repo:      "user/repo",
		Path:      ".",
		Ref:       "main",
		Target:    "shared",
		FilesSpec: "SKILL.md=docs/SKILL.md,README.md=docs/README.md",
	}

	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, false)
	quiet = oldQ

	// Verify manifest has type github-files
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(m2.Skills))
	}
	skill := m2.Skills[0]
	if skill.Source.Type != "github-files" {
		t.Fatalf("expected github-files type, got %q", skill.Source.Type)
	}
	if len(skill.Source.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(skill.Source.Files))
	}
	if skill.Source.Files["SKILL.md"] != "docs/SKILL.md" {
		t.Fatalf("unexpected files mapping: %+v", skill.Source.Files)
	}

	// Verify lock
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	ls, ok := lock2.Skills["repo"] // name inferred from repo
	if !ok {
		t.Fatal("repo not in lock")
	}
	if ls.SourceHash == "" {
		t.Fatal("expected sourceHash")
	}

	// Verify SKILL.md on disk (downloaded from docs/SKILL.md)
	targetPath := resolveTargetPath("shared", m2.Directories)
	skillDir := filepath.Join(expandPath(targetPath), "repo")
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "README.md")); err != nil {
		t.Fatalf("README.md not on disk: %v", err)
	}
}

func TestCmdAdd_NameInference(t *testing.T) {
	t.Run("from path", func(t *testing.T) {
		o, err := parseAddArgs([]string{"user/repo", "skills/anysearch"})
		if err != nil {
			t.Fatal(err)
		}
		// The name is inferred in cmdAdd, not parseAddArgs
		// We test the path parsing here, the name inference happens in cmdAdd
		if o.Path != "skills/anysearch" {
			t.Fatalf("expected skills/anysearch, got %q", o.Path)
		}
	})
	t.Run("from repo basename", func(t *testing.T) {
		o, err := parseAddArgs([]string{"someone/repo.git"})
		if err != nil {
			t.Fatal(err)
		}
		if o.Repo != "someone/repo.git" {
			t.Fatalf("expected someone/repo.git, got %q", o.Repo)
		}
	})
}

func TestCmdAdd_NameInference_Integration(t *testing.T) {
	// Test that name inference from path works end-to-end
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "skills")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}

	fakeGitHub()
	defer restoreGitHub()

	// Use a path that exists in the mock tree: skills/test → name "test"
	opts := addOptions{
		Repo:   "someone/repo",
		Path:   "skills/test",
		Ref:    "main",
		Target: "shared",
	}

	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, false)
	quiet = oldQ

	// Name should be inferred from path: "test"
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 || m2.Skills[0].Name != "test" {
		t.Fatalf("expected name 'test', got %q", m2.Skills[0].Name)
	}
}

func TestCmdAdd_NameInference_FromRepo(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "skills")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}

	// Use fakeGitHubRoot because path is "." and needs root-level SKILL.md
	fakeGitHubRoot()
	defer restoreGitHub()

	opts := addOptions{
		Repo:   "someone/repo.git",
		Path:   ".",
		Ref:    "main",
		Target: "shared",
	}

	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, false)
	quiet = oldQ

	// Name should be inferred from repo: "repo"
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 || m2.Skills[0].Name != "repo" {
		t.Fatalf("expected name 'repo', got %q", m2.Skills[0].Name)
	}
}

// ── SourceHash tests ──────────────────────────────────────────────────

func TestSourceHash_Computation(t *testing.T) {
	// Empty type + path
	h1 := computeSourceHash(SourceEntry{Type: "", Path: "skills/test"})
	if h1 == "" {
		t.Fatal("expected non-empty hash")
	}
	// Different path → different hash
	h2 := computeSourceHash(SourceEntry{Type: "", Path: "skills/other"})
	if h1 == h2 {
		t.Fatal("different paths should produce different hashes")
	}
	// With files map
	h3 := computeSourceHash(SourceEntry{
		Type:  "github-files",
		Path:  ".",
		Files: map[string]string{"SKILL.md": "docs/SKILL.md"},
	})
	if h3 == "" {
		t.Fatal("expected non-empty hash for files type")
	}
	// Different files → different hash
	h4 := computeSourceHash(SourceEntry{
		Type:  "github-files",
		Path:  ".",
		Files: map[string]string{"SKILL.md": "other/SKILL.md"},
	})
	if h3 == h4 {
		t.Fatal("different file mappings should produce different hashes")
	}
	// Deterministic
	h5 := computeSourceHash(SourceEntry{Type: "", Path: "skills/test"})
	if h1 != h5 {
		t.Fatal("computeSourceHash should be deterministic")
	}
}

func TestSourceHash_ChangeTriggersReinstall(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	os.MkdirAll(sharedDir, 0o755)

	skillDir := filepath.Join(sharedDir, "test-skill")
	lockPath := filepath.Join(dir, ".lock.json")

	fakeGitHub()
	defer restoreGitHub()

	source1 := SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/test"}
	source2 := SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/new-path"}

	// Install with source1
	InstallSkill(SkillEntry{Name: "test-skill", Target: "shared", Source: source1}, skillDir, "")
	// Write matching commit marker so the lock check passes
	os.WriteFile(filepath.Join(skillDir, ".skills-commit"), []byte("fakecommit1234567890123456789012345678901234\n"), 0o644)

	// Create lock with SourceHash from source1
	lock := &LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test-skill": {
				Commit:     "fakecommit1234567890123456789012345678901234",
				Path:       "skills/test",
				SourceHash: computeSourceHash(source1),
			},
		},
	}

	// First call — should skip (SourceHash matches)
	writeJSON(t, lockPath, lock)
	lock2, _ := readLock(lockPath)
	r1, _ := installOneSkill(
		SkillEntry{Name: "test-skill", Target: "shared", Source: source1},
		lock2,
		[]DirEntry{{Name: "shared", Path: sharedDir}},
	)
	if r1.Action != "ok" || r1.Error != "already installed" {
		t.Fatalf("expected skip, got %+v", r1)
	}

	// Update lock with SourceHash from different source (simulating config change)
	lock3 := &LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test-skill": {
				Commit:     "fakecommit1234567890123456789012345678901234",
				Path:       "skills/test",              // path in lock still points to skills/test
				SourceHash: computeSourceHash(source2), // but SourceHash is from skills/new-path
			},
		},
	}
	writeJSON(t, lockPath, lock3)
	lock4, _ := readLock(lockPath)

	// Second call — SourceHash mismatch should trigger reinstall
	r2, ls2 := installOneSkill(
		SkillEntry{Name: "test-skill", Target: "shared", Source: source2},
		lock4,
		[]DirEntry{{Name: "shared", Path: sharedDir}},
	)
	if r2.Action != "ok" {
		t.Fatalf("expected install, got %+v", r2)
	}
	if ls2 == nil || ls2.SourceHash == "" {
		t.Fatal("expected updated lock with SourceHash")
	}
}

// ── add + remove end-to-end ──────────────────────────────────────────

func TestCmdAdd_Remove_NoResidue(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{},
	})

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := getLockPath(manifestPath)
	lock, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	fakeGitHub()
	defer restoreGitHub()

	// Add a skill
	opts := addOptions{
		Repo:   "user/repo",
		Path:   "skills/test",
		Ref:    "main",
		Target: "shared",
	}

	oldQ := quiet
	quiet = true
	cmdAdd(m, lock, manifestPath, opts, false)
	quiet = oldQ

	// Verify it was added
	if len(m.Skills) != 1 {
		t.Fatal("skill was not added")
	}

	// Re-read
	m, _ = readManifest(manifestPath)
	lock, _ = readLock(lockPath)

	// Now remove it
	quiet = true
	cmdRemove(m, lock, manifestPath, []string{"test"}, false, false)
	quiet = oldQ

	// Manifest should be empty
	m3, _ := readManifest(manifestPath)
	if len(m3.Skills) != 0 {
		t.Fatal("remove did not clear manifest")
	}

	// Lock should be empty
	lock3, _ := readLock(lockPath)
	if len(lock3.Skills) != 0 {
		t.Fatal("remove did not clear lock")
	}

	// Disk should be gone
	if _, err := os.Stat(filepath.Join(sharedDir, "test", "SKILL.md")); err == nil {
		t.Fatal("disk skill directory still exists")
	}

	// Mirror symlink should be gone
	if _, err := os.Lstat(filepath.Join(claudeDir, "test")); err == nil {
		t.Fatal("mirror symlink still exists")
	}
}

// ── installSkillFiles unit test ──────────────────────────────────────

func TestInstallSkillFiles_Success(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, "output")

	fakeGitHub()
	defer restoreGitHub()

	// Create a skill that uses github-files type
	skill := SkillEntry{
		Name:   "test-files",
		Target: "shared",
		Source: SourceEntry{
			Type: "github-files",
			Repo: "user/repo",
			Ref:  "main",
			Path: ".",
			Files: map[string]string{
				"SKILL.md":  "docs/guide/SKILL.md",
				"README.md": "README.md",
			},
		},
	}

	result := InstallSkill(skill, destDir, "")
	if result.Action == "failed" {
		t.Fatalf("install failed: %s", result.Error)
	}

	// Verify SKILL.md and README.md exist
	if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "README.md")); err != nil {
		t.Fatalf("README.md missing: %v", err)
	}

	// Verify content — downloadFileFn returns "# " + basename
	data, _ := os.ReadFile(filepath.Join(destDir, "SKILL.md"))
	if string(data) != "# SKILL.md" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestInstallSkillFiles_MissingSKILLMD(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, "output")

	fakeGitHub()
	defer restoreGitHub()

	skill := SkillEntry{
		Name:   "test-files",
		Target: "shared",
		Source: SourceEntry{
			Type: "github-files",
			Repo: "user/repo",
			Ref:  "main",
			Path: ".",
			Files: map[string]string{
				"README.md": "README.md",
			},
		},
	}

	result := InstallSkill(skill, destDir, "")
	if result.Action != "failed" {
		t.Fatal("expected failure due to missing SKILL.md")
	}
}

func TestInstallSkill_UnknownSourceType(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, "output")

	skill := SkillEntry{
		Name:   "test-unknown",
		Target: "shared",
		Source: SourceEntry{
			Type: "gitlab-dir",
			Repo: "user/repo",
			Ref:  "main",
			Path: ".",
		},
	}

	result := InstallSkill(skill, destDir, "")
	if result.Action != "failed" || !strings.Contains(result.Error, "unknown source type") {
		t.Fatalf("expected failure for unknown type, got %+v", result)
	}
}

func TestSourceHash_EmptyLockFallback(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	skillDir := filepath.Join(sharedDir, "test-skill")

	fakeGitHub()
	defer restoreGitHub()

	source := SourceEntry{Repo: "a/b", Ref: "main", Path: "skills/test"}

	// Install once
	InstallSkill(SkillEntry{Name: "test-skill", Target: "shared", Source: source}, skillDir, "")
	// Remove commit marker to test the "no marker" fallback path for old locks
	os.Remove(filepath.Join(skillDir, ".skills-commit"))

	// Lock without SourceHash (old format)
	lock := &LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"test-skill": {
				Commit: "fakecommit1234567890123456789012345678901234",
				Path:   "skills/test",
				// no SourceHash — old lock format
			},
		},
	}

	// Should skip using old logic (no SourceHash check)
	r, _ := installOneSkill(
		SkillEntry{Name: "test-skill", Target: "shared", Source: source},
		lock,
		[]DirEntry{{Name: "shared", Path: sharedDir}},
	)
	if r.Action != "ok" || r.Error != "already installed" {
		t.Fatalf("expected skip with old lock, got %+v", r)
	}
}

// ── Fuzzing ────────────────────────────────────────────────────────────

func FuzzParseAddArgs(f *testing.F) {
	seeds := [][4]string{
		{"owner/repo", "", "", ""},
		{"owner/repo", "skills/test", "", ""},
		{"owner/repo", "--name", "test", ""},
		{"owner/repo", "--ref=main", "--target=shared", ""},
		{"owner/repo", "--files=SKILL.md=SKILL.md", "", ""},
		{"owner/repo", "--name=foo", "--ref=dev", "--target=codex"},
		{"owner/repo", "--no-install", "", ""},
		{"x/y", "--name=", "", ""},
		{"a/b", "--files", ",=,", ""},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1], s[2], s[3])
	}
	f.Fuzz(func(t *testing.T, a1, a2, a3, a4 string) {
		args := []string{a1, a2, a3, a4}
		// Trim trailing empty args from fuzzer
		for len(args) > 0 && args[len(args)-1] == "" {
			args = args[:len(args)-1]
		}
		if len(args) == 0 {
			return
		}
		o, err := parseAddArgs(args)
		if err == nil && o.Repo == "" {
			t.Errorf("successful parse but empty repo: %+v", o)
		}
	})
}

func FuzzValidateSkillName(f *testing.F) {
	seeds := []string{"drawio", "my-skill", "a", "", "/etc/passwd", "../escape", "name.with.dots", "name with spaces"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		// Should never panic
		validateSkillName(name)
	})
}

func FuzzComputeSourceHashDeterminism(f *testing.F) {
	seeds := []struct {
		typ, path string
	}{
		{"", "skills/test"},
		{"github-dir", "skills/other"},
		{"github-files", "."},
	}
	for _, s := range seeds {
		f.Add(s.typ, s.path)
	}
	f.Fuzz(func(t *testing.T, typ, path string) {
		// Determinism: same input twice → same hash
		src := SourceEntry{Type: typ, Path: path}
		h1 := computeSourceHash(src)
		h2 := computeSourceHash(src)
		if h1 != h2 {
			t.Errorf("computeSourceHash not deterministic: %s != %s", h1, h2)
		}
	})
}

// ── Move, Sync, and Add-conflict Redesign Tests ───────────────────────

func TestCmdMove_MovesSkillAndReconcilesFiles(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, "codex")
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")
	manifestPath := filepath.Join(dir, ".manifest.json")

	os.MkdirAll(codexDir, 0o755)
	os.MkdirAll(sharedDir, 0o755)
	os.MkdirAll(claudeDir, 0o755)

	oldDestDir := filepath.Join(codexDir, "caveman")
	os.MkdirAll(oldDestDir, 0o755)
	writeFile(t, filepath.Join(oldDestDir, "SKILL.md"), "# caveman")
	writeFile(t, filepath.Join(oldDestDir, ".skills-commit"), "fakecommit123")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "codex", Path: codexDir},
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{
			{From: "shared", To: "claude"},
		},
		Skills: []SkillEntry{
			{
				Name:   "caveman",
				Target: "codex",
				Source: SourceEntry{
					Repo: "cagedbird/caveman",
					Ref:  "main",
					Path: "skills/caveman",
					Files: map[string]string{
						"SKILL.md": "README.md",
					},
				},
				Note: "caveman note",
			},
		},
	})

	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"caveman": {
				Commit:     "fakecommit123",
				Path:       "skills/caveman",
				SourceHash: "fakehash123",
			},
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

	oldQ := quiet
	quiet = true
	cmdMove(m, lock, manifestPath, "caveman", "shared", false)
	quiet = oldQ

	// Verify manifest target was updated to shared
	m2, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Skills) != 1 || m2.Skills[0].Target != "shared" {
		t.Fatalf("expected target 'shared', got: %+v", m2.Skills)
	}

	// Verify metadata preserved
	if m2.Skills[0].Source.Repo != "cagedbird/caveman" || m2.Skills[0].Source.Path != "skills/caveman" {
		t.Fatalf("source metadata lost: %+v", m2.Skills[0].Source)
	}

	// Verify lock entry preserved
	lock2, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lock2.Skills["caveman"].Commit != "fakecommit123" {
		t.Fatalf("lock entry lost/corrupted: %+v", lock2.Skills)
	}

	// Verify old path is gone
	if _, err := os.Stat(oldDestDir); err == nil {
		t.Fatalf("old destination directory %s still exists", oldDestDir)
	}

	// Verify new path exists and has files
	newDestDir := filepath.Join(sharedDir, "caveman")
	if _, err := os.Stat(filepath.Join(newDestDir, "SKILL.md")); err != nil {
		t.Fatalf("moved skill missing SKILL.md under %s: %v", newDestDir, err)
	}

	// Verify mirror symlink created in claude targeting shared/caveman
	mirrorLink := filepath.Join(claudeDir, "caveman")
	target, err := os.Readlink(mirrorLink)
	if err != nil {
		t.Fatalf("mirror symlink missing: %v", err)
	}
	if filepath.Clean(target) != filepath.Clean(newDestDir) {
		t.Fatalf("mirror symlink points to wrong destination: expected %s, got %s", newDestDir, target)
	}
}

func TestCmdMove_DryRun(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, "codex")
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")

	os.MkdirAll(codexDir, 0o755)
	os.MkdirAll(sharedDir, 0o755)

	oldDestDir := filepath.Join(codexDir, "caveman")
	os.MkdirAll(oldDestDir, 0o755)
	writeFile(t, filepath.Join(oldDestDir, "SKILL.md"), "# caveman")

	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "codex", Path: codexDir},
			{Name: "shared", Path: sharedDir},
		},
		Skills: []SkillEntry{
			{
				Name:   "caveman",
				Target: "codex",
				Source: SourceEntry{Repo: "cagedbird/caveman", Ref: "main", Path: "skills/caveman"},
			},
		},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"caveman": {Commit: "fakecommit123", Path: "skills/caveman"},
		},
	})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	cmdMove(m, lock, manifestPath, "caveman", "shared", true)
	quiet = oldQ

	// Manifest target should STILL be codex
	m2, _ := readManifest(manifestPath)
	if m2.Skills[0].Target != "codex" {
		t.Fatalf("dry-run mutated manifest target: got %s, want codex", m2.Skills[0].Target)
	}

	// Directory should STILL be in codexDir and NOT in sharedDir
	if _, err := os.Stat(filepath.Join(codexDir, "caveman", "SKILL.md")); err != nil {
		t.Fatalf("dry-run removed source directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sharedDir, "caveman")); err == nil {
		t.Fatalf("dry-run created destination directory")
	}
}

func TestCmdMove_SkillNotFound_Fails(t *testing.T) {
	if os.Getenv("BE_CRASHER") == "1" {
		dir := t.TempDir()
		manifestPath := filepath.Join(dir, ".manifest.json")
		writeJSON(t, manifestPath, Manifest{
			Version: 1,
			Directories: []DirEntry{
				{Name: "shared", Path: filepath.Join(dir, "shared")},
			},
			Skills: []SkillEntry{},
		})
		m, _ := readManifest(manifestPath)
		lock, _ := readLock(getLockPath(manifestPath))
		cmdMove(m, lock, manifestPath, "nonexistent", "shared", false)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCmdMove_SkillNotFound_Fails")
	cmd.Env = append(os.Environ(), "BE_CRASHER=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("process ran successfully, want exit status 1")
	}
	output := stderr.String()
	if !strings.Contains(output, `skill "nonexistent" not found in manifest`) {
		t.Fatalf("expected error message about skill not found, got: %q", output)
	}
}

func TestCmdMove_TargetNotFound_Fails(t *testing.T) {
	if os.Getenv("BE_CRASHER") == "1" {
		dir := t.TempDir()
		manifestPath := filepath.Join(dir, ".manifest.json")
		writeJSON(t, manifestPath, Manifest{
			Version: 1,
			Directories: []DirEntry{
				{Name: "codex", Path: filepath.Join(dir, "codex")},
			},
			Skills: []SkillEntry{
				{Name: "caveman", Target: "codex"},
			},
		})
		m, _ := readManifest(manifestPath)
		lock, _ := readLock(getLockPath(manifestPath))
		cmdMove(m, lock, manifestPath, "caveman", "shared", false)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCmdMove_TargetNotFound_Fails")
	cmd.Env = append(os.Environ(), "BE_CRASHER=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("process ran successfully, want exit status 1")
	}
	output := stderr.String()
	if !strings.Contains(output, `target "shared" not found in manifest directories or reserved targets`) {
		t.Fatalf("expected error message about target not found, got: %q", output)
	}
}

func TestCmdSyncAndInstallCompatibility(t *testing.T) {
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
				Name: "caveman", Target: "shared",
				Source: SourceEntry{Repo: "cagedbird/caveman", Ref: "main", Path: "skills/test"},
			},
		},
	})
	lockPath := getLockPath(manifestPath)
	writeJSON(t, lockPath, LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"caveman": {Commit: "fakecommit1234567890123456789012345678901234", Path: "skills/test"},
		},
	})

	m, _ := readManifest(manifestPath)
	lock, _ := readLock(lockPath)

	oldQ := quiet
	quiet = true
	// Verify cmdSync with "sync" verb works
	cmdSync("sync", m, lock, manifestPath, "", false)
	quiet = oldQ

	// Verify skill installed to disk
	if _, err := os.Stat(filepath.Join(sharedDir, "caveman", "SKILL.md")); err != nil {
		t.Fatalf("cmdSync did not install skill: %v", err)
	}

	// Clean up disk
	os.RemoveAll(sharedDir)

	// Verify cmdSync with "install" verb works
	oldQ = quiet
	quiet = true
	cmdSync("install", m, lock, manifestPath, "", false)
	quiet = oldQ

	if _, err := os.Stat(filepath.Join(sharedDir, "caveman", "SKILL.md")); err != nil {
		t.Fatalf("cmdSync as install did not install skill: %v", err)
	}
}

func TestCmdAdd_DuplicateName_ConflictSuggestsMigration(t *testing.T) {
	if os.Getenv("BE_CRASHER") == "1" {
		dir := t.TempDir()
		sharedDir := filepath.Join(dir, "skills")
		manifestPath := filepath.Join(dir, ".manifest.json")
		writeJSON(t, manifestPath, Manifest{
			Version: 1,
			Directories: []DirEntry{
				{Name: "shared", Path: sharedDir},
			},
			Skills: []SkillEntry{
				{Name: "caveman", Target: "shared"},
			},
		})
		m, _ := readManifest(manifestPath)
		lockPath := getLockPath(manifestPath)
		lock, _ := readLock(lockPath)
		opts := addOptions{
			Repo:   "cagedbird/caveman",
			Target: "shared",
		}
		cmdAdd(m, lock, manifestPath, opts, false)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCmdAdd_DuplicateName_ConflictSuggestsMigration")
	cmd.Env = append(os.Environ(), "BE_CRASHER=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("process ran successfully, want exit status 1")
	}

	// Verify the stderr output has the migration suggestion
	output := stderr.String()
	expected := `use 'skills move caveman <target>' to retarget`
	if !strings.Contains(output, expected) {
		t.Fatalf("expected stderr to contain %q, got: %q", expected, output)
	}
}

func TestCmdDoctor_ReportsCommonDriftStates(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{{From: "shared", To: "claude"}},
		Skills: []SkillEntry{
			{Name: "caveman", Target: "shared", Source: SourceEntry{Repo: "r/c", Ref: "main", Path: "skills/caveman"}},
			{Name: "docx", Target: "shared", Source: SourceEntry{Repo: "r/d", Ref: "main", Path: "skills/docx"}},
			{Name: "pdf", Target: "shared", Source: SourceEntry{Repo: "r/p", Ref: "main", Path: "skills/pdf"}},
			{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "r/drawio", Ref: "main", Path: "skills/new-drawio"}},
		},
	})
	writeJSON(t, getLockPath(manifestPath), LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"caveman": {Commit: "aaaaaaaaaaaaaaaa", Path: "skills/caveman"},
			"pdf":     {Commit: "bbbbbbbbbbbbbbbb", Path: "skills/pdf"},
			"drawio":  {Commit: "cccccccccccccccc", Path: "skills/old-drawio"},
			"ghost":   {Commit: "dddddddddddddddd", Path: "skills/ghost"},
		},
	})

	writeFile(t, filepath.Join(sharedDir, "caveman", "SKILL.md"), "# caveman\n")
	writeFile(t, filepath.Join(sharedDir, "drawio", "SKILL.md"), "# drawio\n")
	writeFile(t, filepath.Join(sharedDir, "orphan", "SKILL.md"), "# orphan\n")
	writeFile(t, filepath.Join(claudeDir, "caveman", "SKILL.md"), "# not-a-symlink\n")

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmdDoctor(m, lock, manifestPath, false, false)
	})

	for _, want := range []string{
		manifestPath,
		getLockPath(manifestPath),
		"Doctor:",
		"docx",
		"uninstalled",
		"pdf",
		"missing",
		"drawio",
		"path-changed",
		"ghost",
		"stale",
		"orphan",
		"mirror-conflict",
		"claude/caveman",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected doctor output to contain %q, got:\n%s", want, out)
		}
	}

	lockAfter, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lockAfter.Skills["ghost"]; !ok {
		t.Fatalf("doctor must be read-only; stale lock entry was removed")
	}
}

func TestCmdDoctor_CleanState(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	claudeDir := filepath.Join(dir, "claude")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version: 1,
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
			{Name: "claude", Path: claudeDir},
		},
		Mirrors: []MirrorEntry{{From: "shared", To: "claude"}},
		Skills:  []SkillEntry{{Name: "anysearch", Target: "shared", Source: SourceEntry{Repo: "r/a", Ref: "main", Path: "skills/anysearch"}}},
	})
	writeJSON(t, getLockPath(manifestPath), LockFile{
		Version: 1,
		Skills: map[string]LockSkill{
			"anysearch": {Commit: "aaaaaaaaaaaaaaaa", Path: "skills/anysearch"},
		},
	})

	writeFile(t, filepath.Join(sharedDir, "anysearch", "SKILL.md"), "# anysearch\n")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sharedDir, "anysearch"), filepath.Join(claudeDir, "anysearch")); err != nil {
		t.Fatal(err)
	}

	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmdDoctor(m, lock, manifestPath, false, false)
	})
	if !strings.Contains(out, "no drift") {
		t.Fatalf("expected clean doctor output, got:\n%s", out)
	}
}

// doctorTmpFixture builds a manifest with one healthy skill plus a hand-placed
// orphan and two interrupted-install leftovers in the shared directory.
func doctorTmpFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	manifestPath := filepath.Join(dir, ".manifest.json")
	writeJSON(t, manifestPath, Manifest{
		Version:     1,
		Directories: []DirEntry{{Name: "shared", Path: sharedDir}},
		Skills:      []SkillEntry{{Name: "anysearch", Target: "shared", Source: SourceEntry{Repo: "r/a", Ref: "main", Path: "skills/anysearch"}}},
	})
	writeJSON(t, getLockPath(manifestPath), LockFile{
		Version: 1,
		Skills:  map[string]LockSkill{"anysearch": {Commit: "aaaaaaaaaaaaaaaa", Path: "skills/anysearch"}},
	})

	writeFile(t, filepath.Join(sharedDir, "anysearch", "SKILL.md"), "# anysearch\n")
	writeFile(t, filepath.Join(sharedDir, "lark-im", "SKILL.md"), "# hand-placed\n")
	// Died mid-download: no SKILL.md yet.
	if err := os.MkdirAll(filepath.Join(sharedDir, ".ui-aesthetics.tmp-1240448978"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Died mid-swap: previous version parked as backup.
	writeFile(t, filepath.Join(sharedDir, ".docx.old-99", "SKILL.md"), "# backup\n")
	return manifestPath, sharedDir
}

func loadDoctorFixture(t *testing.T, manifestPath string) (*Manifest, *LockFile) {
	t.Helper()
	m, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	return m, lock
}

func TestCmdDoctor_SeparatesTmpLeftoversFromOrphans(t *testing.T) {
	manifestPath, sharedDir := doctorTmpFixture(t)
	m, lock := loadDoctorFixture(t, manifestPath)

	out := captureStdout(t, func() {
		cmdDoctor(m, lock, manifestPath, false, false)
	})

	for _, want := range []string{
		"tmp-leftover",
		".ui-aesthetics.tmp-1240448978",
		".docx.old-99",
		"safe to delete",
		"lark-im",
		"orphan",
		"not managed by skills",
		"--prune-tmp",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected doctor output to contain %q, got:\n%s", want, out)
		}
	}

	// Leftovers must not be reported as orphans, and the hand-placed skill must
	// not be reported as a leftover.
	items := collectDoctorItems(m, lock)
	statusByName := make(map[string]string, len(items))
	for _, item := range items {
		statusByName[item.Name] = item.Status
	}
	for name, want := range map[string]string{
		".ui-aesthetics.tmp-1240448978": statusTmpLeftover,
		".docx.old-99":                  statusTmpLeftover,
		"lark-im":                       "orphan",
		"anysearch":                     "ok",
	} {
		if got := statusByName[name]; got != want {
			t.Fatalf("status for %s = %q, want %q", name, got, want)
		}
	}

	// Report-only mode must not touch disk.
	if _, err := os.Stat(filepath.Join(sharedDir, ".ui-aesthetics.tmp-1240448978")); err != nil {
		t.Fatalf("doctor without --prune-tmp must be read-only: %v", err)
	}
}

func TestCmdDoctor_PruneTmpRemovesOnlyLeftovers(t *testing.T) {
	manifestPath, sharedDir := doctorTmpFixture(t)
	m, lock := loadDoctorFixture(t, manifestPath)

	out := captureStdout(t, func() {
		cmdDoctor(m, lock, manifestPath, true, false)
	})
	if !strings.Contains(out, "removed") {
		t.Fatalf("expected prune output, got:\n%s", out)
	}

	for _, gone := range []string{".ui-aesthetics.tmp-1240448978", ".docx.old-99"} {
		if _, err := os.Stat(filepath.Join(sharedDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be pruned, stat err = %v", gone, err)
		}
	}
	for _, kept := range []string{"anysearch", "lark-im"} {
		if _, err := os.Stat(filepath.Join(sharedDir, kept, "SKILL.md")); err != nil {
			t.Fatalf("prune must not touch %s: %v", kept, err)
		}
	}

	// Manifest and lock are untouched by pruning.
	lockAfter, err := readLock(getLockPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lockAfter.Skills["anysearch"]; !ok {
		t.Fatal("prune must not rewrite the lock")
	}
}

func TestCmdDoctor_PruneTmpDryRunKeepsDisk(t *testing.T) {
	manifestPath, sharedDir := doctorTmpFixture(t)
	m, lock := loadDoctorFixture(t, manifestPath)

	out := captureStdout(t, func() {
		cmdDoctor(m, lock, manifestPath, true, true)
	})
	if !strings.Contains(out, "would remove") {
		t.Fatalf("expected dry-run prune output, got:\n%s", out)
	}
	if strings.Contains(out, "removed  ") {
		t.Fatalf("dry-run must not report removals, got:\n%s", out)
	}
	for _, kept := range []string{".ui-aesthetics.tmp-1240448978", ".docx.old-99"} {
		if _, err := os.Stat(filepath.Join(sharedDir, kept)); err != nil {
			t.Fatalf("dry-run must keep %s: %v", kept, err)
		}
	}
}

func TestIsTmpLeftoverName(t *testing.T) {
	for name, want := range map[string]bool{
		".ui-aesthetics.tmp-1240448978": true,
		".docx.old-99":                  true,
		"anysearch":                     false,
		"lark-im":                       false,
		".hidden-skill":                 false,
		"docx.tmp-1":                    false, // managed leftovers are always dot-prefixed
	} {
		if got := isTmpLeftoverName(name); got != want {
			t.Fatalf("isTmpLeftoverName(%q) = %v, want %v", name, got, want)
		}
	}
}
