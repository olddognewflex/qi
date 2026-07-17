.PHONY: test run tidy build install

PREFIX ?= $(HOME)/.local
BINDIR  = $(PREFIX)/bin

# Optional codesigning identity for installed binaries (issue #47). macOS TCC
# keys ad-hoc binaries by cdhash, so every rebuild of qid orphans its
# Files-and-Folders/Full Disk Access grant and the launchd watcher silently
# degrades until the grant is redone. Signing with a stable identity gives TCC
# a designated requirement that survives rebuilds. One-time setup: create a
# self-signed code-signing certificate in Keychain Access (Certificate
# Assistant > Create a Certificate > Code Signing), then:
#   make install QI_SIGN_IDENTITY="<certificate name>"
QI_SIGN_IDENTITY ?=

tidy:
	go mod tidy

test:
	go test ./...

run:
	go run ./cmd/qi

build:
	go build -o bin/qi ./cmd/qi
	go build -o bin/qid ./cmd/qid
	go build -o bin/qi-mcp ./cmd/qi-mcp

install: build
	mkdir -p $(BINDIR)
	install bin/qi bin/qid bin/qi-mcp $(BINDIR)/
ifneq ($(QI_SIGN_IDENTITY),)
	codesign --force --sign "$(QI_SIGN_IDENTITY)" $(BINDIR)/qi $(BINDIR)/qid $(BINDIR)/qi-mcp
else
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "note: unsigned install — if qid runs under launchd and the vault is in"; \
		echo "~/Documents, its TCC grant breaks on every rebuild. Set QI_SIGN_IDENTITY"; \
		echo "to sign with a stable identity (see Makefile comment / issue #47)."; \
	fi
endif
