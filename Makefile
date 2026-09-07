.DEFAULT_GOAL := help

GO      ?= go
ORIGIN  ?= origin

# NOT named LINT: GNU make defines LINT = lint among its built-in variables, and
# `?=` only assigns when a variable is undefined — so `LINT ?= golangci-lint`
# silently kept make's value and the lint target ran a program named "lint" that
# does not exist here. The gate reported "command not found" and `make check`
# had never actually linted anything.
LINT_BIN ?= golangci-lint

# The version and release logic lives in a script: it is ordinary shell there,
# not shell escaped through make. The script reads MOD, VERSION, FORCE, ORIGIN
# and GO from the environment; MOD and friends are exported automatically
# because they come from the command line, the defaults above are not.
RELEASE := scripts/release.sh
export GO
export ORIGIN

# Every directory with a go.mod is a command and its own module, so the module
# list is discovered rather than maintained by hand.
MODULES := $(sort $(patsubst %/go.mod,%,$(wildcard */go.mod)))

# MOD narrows every target to one command: `make test MOD=radio`. Left empty,
# the per-module targets run over all of them. The release targets require it —
# versions are per command, so there is nothing sensible to release "for all".
MODS := $(if $(MOD),$(MOD),$(MODULES))

# Dev tool versions (override to pin, e.g. GOFUMPT=mvdan.cc/gofumpt@v0.6.0).
GOLANGCI ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
GOFUMPT  ?= mvdan.cc/gofumpt@latest
TPARSE   ?= github.com/mfridman/tparse@latest

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Most targets take MOD=<command> to work on one module:"
	@echo "    make test MOD=radio"
	@echo "  The release targets require it:"
	@echo "    make release MOD=radio VERSION=v0.1.0"

.PHONY: list
list: ## List the commands in this repository
	@for m in $(MODULES); do echo $$m; done

# ---------------------------------------------------------------- development

.PHONY: tidy
tidy: ## Run go mod tidy
	@for m in $(MODS); do echo ">> tidy $$m"; (cd $$m && $(GO) mod tidy); done

.PHONY: build
build: ## Build each command's binary into ./bin (gitignored)
	@for m in $(MODS); do echo ">> build $$m"; (cd $$m && $(GO) build -o ../bin/ .); done

.PHONY: install
install: ## go install each command into GOBIN
	@for m in $(MODS); do echo ">> install $$m"; (cd $$m && $(GO) install .); done

.PHONY: vet
vet: ## go vet
	@for m in $(MODS); do echo ">> vet $$m"; (cd $$m && $(GO) vet ./...); done

.PHONY: test
test: ## Run tests (short, race, per-package coverage)
	@for m in $(MODS); do echo ">> test $$m"; (cd $$m && $(GO) test -race -short -cover ./...); done

.PHONY: test-json
test-json: ## Tests with a pretty pass/fail + coverage summary (tparse)
	@for m in $(MODS); do echo ">> test $$m"; \
		(cd $$m && bash -o pipefail -c '$(GO) test -short -race -cover ./... -json | $(GO) run $(TPARSE) -all'); done

.PHONY: cover
cover: ## Write a coverage profile per module and render the HTML
	@for m in $(MODS); do echo ">> cover $$m"; \
		(cd $$m && mkdir -p coverdata \
			&& $(GO) test -short -covermode=atomic -coverprofile=coverdata/coverage.out ./... \
			&& $(GO) tool cover -func=coverdata/coverage.out | tail -n1 \
			&& $(GO) tool cover -html=coverdata/coverage.out -o coverdata/coverage.html \
			&& echo ">> wrote $$m/coverdata/coverage.html"); done

.PHONY: lint
lint: ## Run golangci-lint
	@for m in $(MODS); do echo ">> lint $$m"; (cd $$m && $(LINT_BIN) run); done

.PHONY: fmt
fmt: ## Format code
	@for m in $(MODS); do (cd $$m && $(GO) run $(GOFUMPT) -w . 2>/dev/null || gofmt -w .); done

.PHONY: fmt-check
fmt-check: ## Fail if any file is unformatted (CI gate)
	@for m in $(MODS); do \
		out=$$(cd $$m && $(GO) run $(GOFUMPT) -l . 2>/dev/null || gofmt -l .); \
		if [ -n "$$out" ]; then echo "unformatted in $$m:"; echo "$$out"; exit 1; fi; done
	@echo ">> formatting OK"

.PHONY: check
check: fmt-check vet lint test ## Everything a release must pass

.PHONY: tools
tools: ## Install the dev tools (golangci-lint, gofumpt, tparse)
	$(GO) install $(GOLANGCI)
	$(GO) install $(GOFUMPT)
	$(GO) install $(TPARSE)
	@echo "installed: golangci-lint, gofumpt, tparse"

.PHONY: clean
clean: ## Remove built binaries and coverage data
	@for m in $(MODS); do rm -f bin/$$m bin/$$m.exe; rm -rf $$m/coverdata; done
	@rmdir bin 2>/dev/null || true
	@echo ">> cleaned"

# ------------------------------------------------------- versions & releasing
#
# Thin wrappers over scripts/release.sh — run it directly for the same result:
#   MOD=radio scripts/release.sh changes

.PHONY: versions
versions: ## Print the current released version of every command
	@$(RELEASE) versions

.PHONY: version
version: ## Print the current released version of MOD
	@$(RELEASE) version

.PHONY: changes
changes: ## Show the commits touching MOD since its last release
	@$(RELEASE) changes

.PHONY: release-suggest
release-suggest: ## Print the version the commits since the last tag call for
	@$(RELEASE) suggest

.PHONY: release
release: ## Tag & push a release: make release MOD=radio VERSION=v0.1.0
	@$(RELEASE) release

.PHONY: release-auto
release-auto: ## Tag & push the version the commit messages call for
	@$(RELEASE) auto

.PHONY: release-patch
release-patch: ## Release the next patch version of MOD
	@$(RELEASE) bump patch

.PHONY: release-minor
release-minor: ## Release the next minor version of MOD
	@$(RELEASE) bump minor

.PHONY: release-major
release-major: ## Release the next major version of MOD (needs a /vN module path)
	@$(RELEASE) bump major

# The individual release gates, runnable on their own to see what a release
# would say before cutting one.
.PHONY: require-mod check-clean check-version check-changes
require-mod check-clean check-version check-changes:
	@$(RELEASE) $@
