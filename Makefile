# splat — Makefile
#
# Cross-platform: works under GNU Make on Windows (Git Bash, WSL, msys2),
# macOS, and Linux. The only assumed shell is bash; the only assumed external
# tools are go and docker. Optional tools (golangci-lint, gofumpt) are
# detected at runtime and the relevant target degrades gracefully when absent.
#
# Targets (run `make help` for the live list):
#
#   build          — compile cmd/splat to ./bin/splat[.exe]
#   run            — go run ./cmd/splat with config.yaml
#   test           — go test ./... -race -count=1
#   test-short     — go test ./... -short
#   cover          — write coverage.out and print summary
#   cover-html     — open coverage.html in a browser (best-effort)
#   vet            — go vet ./...
#   lint           — golangci-lint run (skipped with a note if not installed)
#   fmt            — gofmt -w (and gofumpt if installed)
#   tidy           — go mod tidy + verify
#   clean          — remove ./bin and coverage artifacts
#   docker-build   — docker build -t splat:dev .
#   docker-run     — docker run with the three documented mount points
#   ci             — vet + test + lint, the same checks CI runs
#   help           — print the target list

# ----------------------------------------------------------------------------
# Cross-platform shell setup.
#
# `SHELL := bash` works on Linux/macOS as long as bash is on PATH; on Windows
# the user is expected to run make from Git Bash, WSL, or msys2 (which is also
# what most Windows Go contributors already do). We prefer this to per-target
# OS detection because every target stays a single recipe rather than a Windows
# branch + a Unix branch.
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

# Paths and naming.
PKG          := github.com/jo-hoe/splat
BIN_DIR      := bin
BIN_NAME     := splat$(shell go env GOEXE)
BIN          := $(BIN_DIR)/$(BIN_NAME)

CONFIG       ?= config.yaml
PHOTOS_DIR   ?= ./photos
CACHE_DIR    ?= ./cache

DOCKER_IMAGE ?= splat:dev

# Build flags. -trimpath strips local paths from the binary; -s -w strips the
# symbol/DWARF tables. CGO is disabled so distroless can run the binary.
LDFLAGS      := -s -w
GO_BUILDFLAGS := -trimpath -ldflags='$(LDFLAGS)'

# ----------------------------------------------------------------------------
# Default target.

.DEFAULT_GOAL := help

# ----------------------------------------------------------------------------
# Phony declaration (every target here has no on-disk artifact tracked by make).

.PHONY: help build run test test-short cover cover-html vet lint fmt tidy clean \
        docker-build docker-run ci tools

help: ## Print this help.
	@awk 'BEGIN { FS = ":.*##"; printf "Targets:\n" } /^[a-zA-Z_-]+:.*##/ { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ----------------------------------------------------------------------------
# Build & run.

build: ## Compile the splat binary into ./bin
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build $(GO_BUILDFLAGS) -o $(BIN) ./cmd/splat
	@echo "built $(BIN)"

run: ## Run splat from source against $(CONFIG) (default: config.yaml)
	go run ./cmd/splat --config $(CONFIG)

# ----------------------------------------------------------------------------
# Test, coverage, lint.

test: ## Run all tests with the race detector
	go test ./... -race -count=1

test-short: ## Run tests with -short (skip integration where supported)
	go test ./... -short -count=1

cover: ## Produce coverage.out and print the summary
	go test ./... -coverprofile=coverage.out -covermode=atomic -count=1
	go tool cover -func=coverage.out | tail -n 20

cover-html: cover ## Generate coverage.html (open it manually)
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

vet: ## go vet ./...
	go vet ./...

lint: ## Run golangci-lint if installed; print a note otherwise
	@if command -v golangci-lint >/dev/null 2>&1 ; then \
	    golangci-lint run ; \
	else \
	    echo "golangci-lint not installed; skipping (install: https://golangci-lint.run/usage/install/)" ; \
	fi

fmt: ## gofmt -w; also gofumpt if installed
	gofmt -w .
	@if command -v gofumpt >/dev/null 2>&1 ; then \
	    gofumpt -w . ; \
	else \
	    echo "gofumpt not installed; skipping (install: go install mvdan.cc/gofumpt@latest)" ; \
	fi

tidy: ## go mod tidy && go mod verify
	go mod tidy
	go mod verify

# ----------------------------------------------------------------------------
# Aggregates.

ci: vet test lint ## Same checks CI runs (vet + race tests + lint)

# ----------------------------------------------------------------------------
# Docker.

docker-build: ## Build the production container image
	docker build -t $(DOCKER_IMAGE) .

docker-run: ## Run the container with the documented mounts
	@mkdir -p $(PHOTOS_DIR) $(CACHE_DIR)
	docker run --rm -p 8080:8080 \
	    -v "$(CURDIR)/$(PHOTOS_DIR):/data:rw" \
	    -v "$(CURDIR)/$(CACHE_DIR):/cache:rw" \
	    -v "$(CURDIR)/$(CONFIG):/etc/splat/config.yaml:ro" \
	    $(DOCKER_IMAGE)

# ----------------------------------------------------------------------------
# Cleanup.

clean: ## Remove build outputs and coverage artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html

# ----------------------------------------------------------------------------
# Optional dev tools (informational).

tools: ## Print install commands for optional dev tools
	@echo "go install mvdan.cc/gofumpt@latest"
	@echo "see https://golangci-lint.run/usage/install/ for golangci-lint"
