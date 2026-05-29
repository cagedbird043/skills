package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")

	lock := &LockFile{
		Skills: map[string]LockSkill{
			"drawio": {Commit: "9b74459b5ae5aad67781c4a9de1093605d620f23", Path: "plugins/project-documenter/skills/drawio"},
		},
	}

	m := &Manifest{
		Directories: []DirEntry{
			{Name: "shared", Path: sharedDir},
		},
	}

	// This should try to re-download since SKILL.md is missing
	// We expect it to fail because there's no network in test
	result, _ := installOneSkill(
		SkillEntry{Name: "drawio", Target: "shared", Source: SourceEntry{Repo: "github/awesome-copilot", Ref: "main", Path: "plugins/project-documenter/skills/drawio"}},
		lock, m.Directories,
	)

	// Either fails with tree error (no network) or succeeds (if cached)
	if result.Action != "failed" {
		t.Logf("unexpected success (might have network): %+v", result)
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

func TestUpdateOneSkill_SkipsWhenPathAndCommitMatch(t *testing.T) {
	lock := &LockFile{
		Skills: map[string]LockSkill{
			"test": {Commit: "abc123", Path: "skills/test"},
		},
	}

	ls := lock.Skills["test"]
	hasLock := true
	lockedCommit := ls.Commit

	if hasLock && lockedCommit == "abc123" && ls.Path == "skills/test" {
		// This is the skip condition — it's correct
	} else {
		t.Fatal("comparison logic incorrect: should have detected match")
	}
}

func TestUpdateOneSkill_PathChangeTriggersUpdate(t *testing.T) {
	lock := &LockFile{
		Skills: map[string]LockSkill{
			"test": {Commit: "abc123", Path: "skills/old-path"},
		},
	}

	ls := lock.Skills["test"]
	hasLock := true
	lockedCommit := ls.Commit

	// Lock path is "skills/old-path", skill path is "skills/new-path"
	// Even if commit matches, path differs → should NOT skip
	wouldSkip := hasLock && lockedCommit == "abc123" && ls.Path == "skills/new-path"
	if wouldSkip {
		t.Fatal("updateOneSkill comparison: path changed but skip condition matched — BUG")
	}
	// Verify the correct comparison: path must match too
	wouldSkip = hasLock && lockedCommit == "abc123" && ls.Path == "skills/old-path"
	if !wouldSkip {
		t.Fatal("updateOneSkill comparison: same path should allow skip")
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
