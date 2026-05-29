# skills — Agent Maintenance Guide

## Overview

Zero-dependency Go CLI that installs agent skills from GitHub subdirectories.
Reads a `.manifest.json`, downloads files via the GitHub Trees + Contents API,
and writes a `.lock.json` to pin installed versions.

## Architecture

```
main.go        — CLI entry, flag parsing, command dispatch
manifest.go    — Manifest/LockFile types, JSON I/O, path helpers
install.go     — GitHub API client, tree traversal, parallel installer
```

## Key commands

```
skills list              → cmdList()
skills install           → cmdInstall() → InstallAll() → parallel workers
skills install <name>    → cmdInstall() → processOneSkill()
skills verify            → cmdVerify()
skills info <name>       → cmdInfo()
skills completion zsh    → cmdCompletion()
```

## Data flow

1. `readManifest()` — parse `.manifest.json`
2. `readLock()` — parse `.lock.json` (or empty if absent)
3. For each skill:
   - `fetchLatestCommit()` — GET /repos/{repo}/commits/{ref}
   - Compare with lock → skip if unchanged and `SKILL.md` exists on disk
   - `fetchTree()` — GET /repos/{repo}/git/trees/{ref}?recursive=1
   - Filter entries under source.path
   - `downloadFile()` — GET /repos/{repo}/contents/{path}  (handles symlinks via `download_url`)
4. `writeLock()` — persist `.lock.json`

## Parallelism

`InstallAll()` uses a worker pool of 4 goroutines. Each worker processes one
skill independently. Results are collected via channels.

## Symlink handling

`downloadFile()` handles GitHub symlinks (mode `120000` in the tree API).
When the Contents API returns `"type": "symlink"`, it falls back to the
`download_url` field instead of base64-decoding.

## Rate limiting

The GitHub API is rate-limited to 60 requests/hour without authentication.
The tool automatically uses `gh auth token` or `GITHUB_TOKEN` when available.
With a token: 5000 requests/hour.

## Built-in files

| File | Purpose |
|------|---------|
| `go.mod` | Go module definition |
| `main.go` | CLI entry, flag parsing, all commands |
| `manifest.go` | Manifest/LockFile types, JSON I/O |
| `install.go` | GitHub API client, tree traversal, parallel installer |
| `Makefile` | build / install / clean targets |
| `README.md` | User documentation |
| `AGENTS.md` | This file |
| `install.sh` | curl-pipe install script |

## Manifest format

```json
{
  "version": 1,
  "directories": [
    { "name": "shared", "path": "~/.agents/skills" }
  ],
  "symlinks": [
    { "from": "~/.claude/skills", "to": "~/.agents/skills" }
  ],
  "skills": [
    {
      "name": "drawio",
      "target": "shared",
      "source": {
        "repo": "github/awesome-copilot",
        "ref": "main",
        "path": "plugins/project-documenter/skills/drawio"
      }
    }
  ]
}
```

## Lock format

```json
{
  "version": 1,
  "updated_at": "2026-05-30T03:00:00+08:00",
  "skills": {
    "drawio": {
      "commit": "9b74459b...",
      "path": "plugins/project-documenter/skills/drawio"
    }
  }
}
```

## Testing

```bash
go build -o skills .
./skills --manifest ./testdata/manifest.json list
./skills --manifest ./testdata/manifest.json install
./skills --manifest ./testdata/manifest.json verify
```
