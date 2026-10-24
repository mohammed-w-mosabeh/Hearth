# Hearth — build, lint, test and packaging entry points.
# Targets marked "stub" are filled in by the ROADMAP task named in their message.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

MODULE    := github.com/mohammed-w-mosabeh/hearth
GO        ?= go
GO_VERSION := 1.27.3

# Pinned dev tools, installed into .tools/bin by `make tools`.
# Bump deliberately: update the version here and re-run `make tools`.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.9.0
GOSEC_VERSION         := v2.30.0
NFPM_VERSION          := v2.48.0

TOOLS_BIN     := $(CURDIR)/.tools/bin
GOLANGCI_LINT := $(TOOLS_BIN)/golangci-lint
GOVULNCHECK   := $(TOOLS_BIN)/govulncheck
GOSEC         := $(TOOLS_BIN)/gosec
NFPM          := $(TOOLS_BIN)/nfpm

# Reproducible, static builds (SECURITY S25).
export CGO_ENABLED := 0
GO_BUILD_FLAGS := -trimpath -buildvcs=true

GO_PKGS = $(shell $(GO) list ./... 2>/dev/null)

WEB_SRC = $(shell find web/src web/index.html web/vite.config.ts web/svelte.config.js -type f 2>/dev/null)

stub = @echo "make $@: not implemented yet ($(1))"

.PHONY: help
help: ## List targets
	@grep -E '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

.PHONY: tools
tools: ## Install pinned dev tools into .tools/bin
	@$(GO) version | grep -q 'go$(GO_VERSION) ' || { echo "expected go$(GO_VERSION), got: $$($(GO) version)"; exit 1; }
	GOBIN=$(TOOLS_BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOBIN=$(TOOLS_BIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	GOBIN=$(TOOLS_BIN) $(GO) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	GOBIN=$(TOOLS_BIN) $(GO) install github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)

# The Go build embeds web/dist, so every Go step needs the UI built first.
web/node_modules/.package-lock.json: web/package.json web/package-lock.json
	cd web && npm ci

web/dist/index.html: web/node_modules/.package-lock.json $(WEB_SRC)
	cd web && npm run build

.PHONY: web
web: web/dist/index.html ## Build the UI into web/dist

.PHONY: dev
dev: ## Run hearthd with the Vite dev server proxy (127.0.0.1 only)
	$(call stub,ROADMAP 0.7/0.8)

.PHONY: build
build: ## Build web + both binaries
	$(call stub,ROADMAP 0.7/0.8)

.PHONY: test
test: web ## go test + vitest
	$(GO) test ./...
	cd web && npm run -s test

FUZZTIME ?= 60s

.PHONY: fuzz
fuzz: web ## Run every Fuzz* target for FUZZTIME each (default 60s)
	@for pkg in $(GO_PKGS); do \
		for f in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "== $$pkg $$f"; \
			$(GO) test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

.PHONY: lint
lint: web ## golangci-lint (incl. gosec, depguard), gosec, forbidden patterns, svelte-check, eslint
	@out=$$(git ls-files -z '*.go' | xargs -0 -r gofmt -l); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	./scripts/check-forbidden.sh
	$(GO) vet ./...
	$(GOLANGCI_LINT) run ./...
	$(GOSEC) -quiet -exclude-dir=web/node_modules ./...
	cd web && npm run -s check && npm run -s lint

.PHONY: vuln
vuln: web ## govulncheck + npm audit
	$(GOVULNCHECK) ./...
	cd web && npm audit --audit-level=high

.PHONY: check
check: lint test vuln ## lint + test + vuln

.PHONY: itest
itest: ## Integration tests in a Debian 13 container (systemd + Docker)
	$(call stub,ROADMAP 0.9)

.PHONY: bench
bench: ## Measure hearthd idle RSS/CPU over 60 s
	$(call stub,ROADMAP 0.10)

.PHONY: deb
deb: ## Build the .deb with nfpm
	$(call stub,ROADMAP 0.9)

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist web/dist web/node_modules *.deb
