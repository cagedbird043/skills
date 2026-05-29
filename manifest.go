package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ── types ────────────────────────────────────────────────────────────

type Manifest struct {
	Version    int              `json:"version"`
	Directories []DirEntry      `json:"directories"`
	Symlinks   []SymlinkEntry   `json:"symlinks,omitempty"`
	Skills     []SkillEntry     `json:"skills"`
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
}

type SourceEntry struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir lock dir: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
