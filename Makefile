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

# From git describe: 1.0.0 on the tag, 1.0.0+git1.505fb84 after it.
VERSION ?= $(shell ./scripts/version.sh)
DEB      = dist/hearth_$(VERSION)_amd64.deb

# Reproducible, static builds (SECURITY S25).
export CGO_ENABLED := 0
GO_BUILD_FLAGS = -trimpath -buildvcs=true -ldflags '-X main.buildVersion=$(VERSION)'

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

DEV_PORT ?= 7381
DEV_ENV   = XDG_CONFIG_HOME=$(CURDIR)/.dev/config XDG_DATA_HOME=$(CURDIR)/.dev/data

.PHONY: dev
dev: web ## Run hearthd (isolated .dev/ config) + Vite dev server, both on 127.0.0.1
	@mkdir -p -m 700 .dev/config/hearth .dev/data
	@test -f .dev/config/hearth/config.json || echo '{"port": $(DEV_PORT)}' > .dev/config/hearth/config.json
	@echo "open: http://127.0.0.1:5173/auth?t=$$(cat .dev/config/hearth/token 2>/dev/null || echo '<token appears after start>')"
	@trap 'kill 0' EXIT; \
		$(DEV_ENV) $(GO) run ./cmd/hearthd & \
		cd web && HEARTH_PORT=$(DEV_PORT) npx vite --port 5173

.PHONY: build
build: web ## Build web + binaries into bin/
	$(GO) build $(GO_BUILD_FLAGS) -o bin/hearthd ./cmd/hearthd
	$(GO) build $(GO_BUILD_FLAGS) -o bin/hearth-broker ./cmd/hearth-broker

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
itest: deb ## Install/uninstall the .deb in a Debian 13 container with systemd
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -o dist/itest-fakedocker ./test/itest/fakedocker
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -o dist/brokerprobe ./test/pentest/brokerprobe
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -o dist/itest-termprobe ./test/itest/termprobe
	./test/itest/run.sh $(DEB) dist/itest-fakedocker dist/brokerprobe dist/itest-termprobe

.PHONY: bench
bench: build ## Measure hearthd idle RSS/CPU over BENCH_SECONDS (default 60) against the budget
	./scripts/bench.sh bin/hearthd

.PHONY: deb
deb: build ## Build dist/hearth_<version>_amd64.deb with nfpm (unsigned for now)
	@mkdir -p dist
	VERSION='$(VERSION)' $(NFPM) package --config packaging/nfpm.yaml --packager deb --target $(DEB)

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist .dev web/dist web/node_modules *.deb
