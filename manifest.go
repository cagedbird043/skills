package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── types ────────────────────────────────────────────────────────────

type MirrorEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Manifest struct {
	Version     int            `json:"version"`
	Comment     string         `json:"comment,omitempty"`
	Directories []DirEntry     `json:"directories"`
	Symlinks    []SymlinkEntry `json:"symlinks,omitempty"`
	Mirrors     []MirrorEntry  `json:"mirrors,omitempty"`
	Skills      []SkillEntry   `json:"skills"`
}

type DirEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Comment string `json:"comment,omitempty"`
}

type SymlinkEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type SkillEntry struct {
	Name   string       `json:"name"`
	Target string       `json:"target"`
	Source SourceEntry  `json:"source"`
	Note   string       `json:"note,omitempty"`
	extra  map[string]json.RawMessage
}
func (s *SkillEntry) UnmarshalJSON(data []byte) error {
	type alias SkillEntry
	if err := json.Unmarshal(data, (*alias)(s)); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		switch k {
		case "name", "target", "source", "note":
			continue
		default:
			if s.extra == nil {
				s.extra = make(map[string]json.RawMessage)
			}
			s.extra[k] = v
		}
	}
	return nil
}

func (s SkillEntry) MarshalJSON() ([]byte, error) {
	type alias SkillEntry
	base, err := json.Marshal(alias(s))
	if err != nil {
		return nil, err
	}
	if len(s.extra) == 0 {
		return base, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range s.extra {
		merged[k] = v
	}
	return json.Marshal(merged)
}

type SourceEntry struct {
	Type  string            `json:"type,omitempty"`
	Files map[string]string `json:"files,omitempty"`
	Repo  string            `json:"repo"`
	Ref   string            `json:"ref"`
	Path  string            `json:"path,omitempty"`
	extra map[string]json.RawMessage
}
func (s *SourceEntry) UnmarshalJSON(data []byte) error {
	type alias SourceEntry
	if err := json.Unmarshal(data, (*alias)(s)); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		switch k {
		case "type", "files", "repo", "ref", "path":
			continue
		default:
			if s.extra == nil {
				s.extra = make(map[string]json.RawMessage)
			}
			s.extra[k] = v
		}
	}
	return nil
}

func (s SourceEntry) MarshalJSON() ([]byte, error) {
	type alias SourceEntry
	base, err := json.Marshal(alias(s))
	if err != nil {
		return nil, err
	}
	if len(s.extra) == 0 {
		return base, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range s.extra {
		merged[k] = v
	}
	return json.Marshal(merged)
}

// ── lock ─────────────────────────────────────────────────────────────

type LockFile struct {
	Version  int                `json:"version"`
	Updated  string             `json:"updated_at"`
	Skills   map[string]LockSkill `json:"skills"`
}

type LockSkill struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
}

// ── path helpers ─────────────────────────────────────────────────────

func expandPath(p string) string {
	if len(p) > 1 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func resolveTargetPath(dirName string, dirs []DirEntry) string {
	for _, d := range dirs {
		if d.Name == dirName {
			return expandPath(d.Path)
		}
	}
	return ""
}

// ── I/O ──────────────────────────────────────────────────────────────

// atomicWriteFile writes data to path atomically by writing to a temp file
// in the same directory (same filesystem) and renaming.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp: %w", err)
	}
	f.Close()
	return os.Rename(tmpName, path)
}

func readManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Version == 0 {
		m.Version = 1
	}
	return &m, nil
}

// writeManifest serializes the manifest with deterministic formatting.
// Output is idempotent: writing the same data twice produces identical bytes.
func writeManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0644)
}

func readLock(path string) (*LockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &LockFile{Version: 1, Skills: make(map[string]LockSkill)}, nil
		}
		return nil, fmt.Errorf("read lock: %w", err)
	}
	var l LockFile
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse lock: %w", err)
	}
	if l.Skills == nil {
		l.Skills = make(map[string]LockSkill)
	}
	return &l, nil
}

func writeLock(path string, l *LockFile) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encode lock: %w", err)
	}
	return atomicWriteFile(path, data, 0644)
}

// validateSkillName checks that a skill name is safe to use as a directory name.
func validateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid skill name %q", name)
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "\x00") {
		return fmt.Errorf("skill name %q contains invalid characters", name)
	}
	return nil
}
