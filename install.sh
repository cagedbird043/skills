#!/bin/sh
# install.sh — One-command installer for skills CLI
# Usage: curl -sfL https://cagedbird.cn/skills/install.sh | sh
set -eu

REPO="cagedbird043/skills"
BINARY="skills"

# Platform detection
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

if command -v go >/dev/null 2>&1; then
  echo "  -> installing via go install..."
  go install "github.com/${REPO}@latest"
  BIN_PATH="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin/$BINARY"
  echo "  OK installed to $BIN_PATH"
else
  BIN_PATH="${HOME}/.local/bin/${BINARY}"
  URL="https://github.com/${REPO}/releases/latest/download/${BINARY}-${OS}-${ARCH}"

  echo "  -> downloading ${BINARY} for ${OS}/${ARCH}..."
  mkdir -p "$(dirname "$BIN_PATH")"

  if command -v curl >/dev/null 2>&1; then
    curl -sfL "$URL" -o "$BIN_PATH"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$URL" -O "$BIN_PATH"
  else
    echo "  ERR: need curl or wget"
    exit 1
  fi
  chmod +x "$BIN_PATH"
  echo "  OK installed to $BIN_PATH"
fi

# Install zsh completion
COMP_DIR="${HOME}/.local/share/zsh/site-functions"
mkdir -p "$COMP_DIR"
if [ -x "$BIN_PATH" ]; then
  "$BIN_PATH" completion zsh > "$COMP_DIR/_$BINARY" 2>/dev/null && \
    echo "  OK zsh completion installed"
fi

echo ""
echo "  skills installed successfully!"
echo ""
echo "  Make sure this is in your PATH:"
echo "    export PATH=\"\$HOME/.local/bin:\$PATH\""
echo ""
echo "  Add to .zshrc for tab completion:"
echo "    fpath=(~/.local/share/zsh/site-functions \$fpath)"
echo ""
echo "  Restart your shell:"
echo "    exec zsh"
