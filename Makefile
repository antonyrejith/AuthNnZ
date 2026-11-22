# Portun build entry point. Every quality gate runs through these targets,
# locally and in CI. Versions are pinned (decision D-15).
SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

# Go runs exactly this toolchain. Locally, the go command on PATH downloads it
# from the module proxy (checksum-verified); CI installs it and sets
# GOTOOLCHAIN=local, which this file respects.
GO_VERSION := go1.27.4
export GOTOOLCHAIN ?= $(GO_VERSION)
export GOFLAGS := -mod=readonly

# Put the pinned toolchain first on PATH, so tools that call "go" themselves
# (govulncheck, golangci-lint) load packages with it and not with whatever
# older go is installed. Computed once. GNU Make 4.3 does not pass exported
# variables to $(shell), hence the explicit GOTOOLCHAIN.
GO_ROOT := $(shell GOTOOLCHAIN=$(GOTOOLCHAIN) go env GOROOT)
ifeq ($(GO_ROOT),)
$(error cannot find the Go toolchain $(GO_VERSION); install Go and retry)
endif
export PATH := $(GO_ROOT)/bin:$(PATH)
GO_FOUND := $(shell GOTOOLCHAIN=$(GOTOOLCHAIN) $(GO_ROOT)/bin/go env GOVERSION)
ifneq ($(GO_FOUND),$(GO_VERSION))
$(error Go $(GO_VERSION) is required, found $(GO_FOUND) (GOTOOLCHAIN=$(GOTOOLCHAIN)))
endif

BIN := $(CURDIR)/bin
GOTEST_FLAGS := -race -shuffle=on -count=1

# Testcontainers v0.45.0 pulls its reaper (Ryuk) by tag only, which D-15
# forbids; test kits remove their containers themselves (see dbtest).
INTEGRATION_ENV := TESTCONTAINERS_RYUK_DISABLED=true

# Coverage gate (ADR-0017): every package at least COVER_MIN, the
# security-critical packages at least COVER_STRICT_MIN. Test kits are
# helpers for other packages' tests and are not measured on their own.
COVER_MIN := 80
COVER_STRICT_MIN := 90
COVER_STRICT := internal/platform/crypto,internal/platform/kms,internal/platform/db,internal/platform/secret
COVER_EXCLUDE := internal/platform/db/dbtest,internal/platform/kms/kmstest,internal/modules/audit/internal/store/storetest,internal/modules/audit/internal/checkpoint/tsatest,internal/modules/audit/audittest,internal/modules/tenancy/tenancytest,internal/modules/config/configtest

GATES := pincheck lint build test test-integration tidy-check vuln secrets cover-check

.PHONY: help tools pincheck lint fmt vuln tidy-check build test test-integration test-all \
	cover cover-check generate generate-check secrets secrets-history hooks gates

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

tools: ## Build pinned developer tools into ./bin
	GOBIN=$(BIN) go -C tools install tool
	GOBIN=$(BIN) go -C tools/gitleaks install tool
	go -C tools build -o $(BIN)/pincheck ./pincheck
	go -C tools build -o $(BIN)/covercheck ./covercheck
	go -C tools build -o $(BIN)/layercheck ./layercheck

gates: $(GATES) ## Every gate a change must pass (CI runs the same list)

pincheck: tools ## Fail on any unpinned version
	$(BIN)/pincheck .

lint: tools ## Static analysis and module layer rules (zero warnings)
	$(BIN)/golangci-lint run ./...
	$(BIN)/layercheck ./...
	cd tools && $(BIN)/golangci-lint run ./...

fmt: tools ## Format code
	$(BIN)/golangci-lint fmt ./...
	cd tools && $(BIN)/golangci-lint fmt ./...

vuln: tools ## Known-vulnerability scan of reachable code (VULN_FLAGS="-db file:///mirror" for an offline mirror)
	$(BIN)/govulncheck $(VULN_FLAGS) ./...

tidy-check: ## go.mod and go.sum must already be tidy
	go mod tidy -diff
	go -C tools mod tidy -diff
	go -C tools/gitleaks mod tidy -diff

secrets: tools ## Secret scan of the working tree (tracked and untracked, not ignored)
	$(BIN)/gitleaks dir --no-banner --redact --config .gitleaks.toml .

secrets-history: tools ## Secret scan of the whole git history (CI; needs a full clone)
	$(BIN)/gitleaks git --no-banner --redact --config .gitleaks.toml .

build: ## Build every package
	CGO_ENABLED=0 go build -trimpath ./...

test: ## Unit tests
	go test $(GOTEST_FLAGS) ./...
	go -C tools test $(GOTEST_FLAGS) ./...

test-integration: ## Integration tests (needs Docker)
	$(INTEGRATION_ENV) go test $(GOTEST_FLAGS) -tags integration ./...

test-all: test test-integration ## Unit and integration tests

cover: ## Coverage report for unit and integration tests
	$(INTEGRATION_ENV) go test -count=1 -tags integration -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

cover-check: tools ## Per-package coverage gate (needs Docker)
	$(INTEGRATION_ENV) go test -count=1 -tags integration -cover ./... \
		| $(BIN)/covercheck -min $(COVER_MIN) -strict-min $(COVER_STRICT_MIN) \
			-strict $(COVER_STRICT) -exclude $(COVER_EXCLUDE)

generate: tools ## Regenerate code (sqlc, OpenAPI)
	go generate ./...

generate-check: ## Regenerating code must not change the tree
	@before="$$(git status --porcelain=v1 --untracked-files=all; git diff)"; \
	$(MAKE) --no-print-directory generate; \
	after="$$(git status --porcelain=v1 --untracked-files=all; git diff)"; \
	if [ "$$before" != "$$after" ]; then \
		echo "generate-check: generated files are stale; run make generate and commit the result" >&2; \
		git status --short >&2; exit 1; \
	fi

hooks: tools ## Install the git pre-commit hooks (lefthook.yml)
	$(BIN)/lefthook install
