# Portun build entry point. Every quality gate runs through these targets,
# locally and in CI. Versions are pinned (decision D-15).
SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

# Go downloads exactly this toolchain from the module proxy (checksum-verified).
export GOTOOLCHAIN := go1.27.4
export GOFLAGS := -mod=readonly

BIN := $(CURDIR)/bin
GOTEST_FLAGS := -race -shuffle=on -count=1

.PHONY: help tools pincheck lint fmt vuln tidy-check build test test-integration test-all cover generate

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

tools: ## Build pinned developer tools into ./bin
	GOBIN=$(BIN) go -C tools install tool
	go -C tools build -o $(BIN)/pincheck ./pincheck

pincheck: tools ## Fail on any unpinned version
	$(BIN)/pincheck .

lint: tools ## Static analysis (zero warnings)
	$(BIN)/golangci-lint run ./...
	cd tools && $(BIN)/golangci-lint run ./...

fmt: tools ## Format code
	$(BIN)/golangci-lint fmt ./...
	cd tools && $(BIN)/golangci-lint fmt ./...

vuln: tools ## Known-vulnerability scan of reachable code
	$(BIN)/govulncheck ./...

tidy-check: ## go.mod and go.sum must already be tidy
	go mod tidy -diff
	go -C tools mod tidy -diff

build: ## Build every package
	CGO_ENABLED=0 go build -trimpath ./...

test: ## Unit tests
	go test $(GOTEST_FLAGS) ./...
	go -C tools test $(GOTEST_FLAGS) ./...

test-integration: ## Integration tests (needs Docker)
	go test $(GOTEST_FLAGS) -tags integration ./...

test-all: test test-integration ## Unit and integration tests

cover: ## Coverage report for unit and integration tests
	go test -count=1 -tags integration -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

generate: tools ## Regenerate code (sqlc, OpenAPI); CI fails on any diff
	go generate ./...
