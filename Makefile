BINARY = skills
VERSION = $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

.PHONY: all build install clean

all: build

LDFLAGS = -s -w -X main.version=$(VERSION)

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

install: build
	mkdir -p $(HOME)/.local/bin
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "✓ installed to $(HOME)/.local/bin/$(BINARY)"
	@echo "  run 'skills completion zsh > ~/.local/share/zsh/site-functions/_skills' for completions"

clean:
	rm -f $(BINARY)
	@echo "✓ cleaned"

# Cross-compilation helpers
build-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 .

build-macos:
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 .
