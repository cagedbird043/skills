# skills — agent skill manager

`skills` is a zero-dependency Go binary that installs agent skills from
GitHub subdirectories. It replaces `gh skill install` with something
that actually works.

## Install

### Go (if you have Go installed)

```bash
go install github.com/cagedbird043/skills@latest
```

### curl

```bash
curl -sfL https://cagedbird.cn/skills/install.sh | sh
```

### Build from source

```bash
git clone https://github.com/cagedbird043/skills.git
cd skills
make install
```

## Quick start

```bash
# List all skills defined in the manifest
skills list

# Install all skills
skills install

# Install a single skill
skills install drawio

# Check skill directory integrity
skills verify

# Show skill details
skills info drawio
```

## Manifest format

Create a `.manifest.json`:

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

Run `skills install`. A `.lock.json` will be created next to your manifest
recording the exact commit of each installed skill.

## Commands

| Command | Description |
|---------|-------------|
| `skills list` | List all skills with installation status |
| `skills install [name]` | Install from lock (zero API calls if locked) |
| `skills update [name]` | Check remote commits, update changed skills |
| `skills verify` | Check all skill directories exist on disk |
| `skills info <name>` | Show source, path, commit, and disk location |
| `skills completion <shell>` | Generate shell completion (zsh, bash) |

## Options

| Flag | Description |
|------|-------------|
| `-m, --manifest <path>` | Path to manifest file |
| `-q, --quiet` | Suppress normal output, errors only |
| `--version` | Print version |

## Environment

| Variable | Description |
|----------|-------------|
| `SKILLS_MANIFEST` | Default manifest path (alternative to `--manifest`) |
| `NO_COLOR` | Set to any value to disable colored output |

## Shell completion

```bash
# zsh
skills completion zsh > ~/.local/share/zsh/site-functions/_skills
# then add to .zshrc:
#   fpath=(~/.local/share/zsh/site-functions $fpath)

# bash
skills completion bash > ~/.local/share/bash-completion/completions/skills
```
