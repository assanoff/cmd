.DEFAULT_GOAL := help

GO      ?= go
LINT    ?= golangci-lint
ORIGIN  ?= origin

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

# LATEST prints the newest release tag of $(MOD) as a bare vX.Y.Z. Tags carry
# the module directory as a prefix (radio/v0.1.0), because that is the only
# form `go install github.com/assanoff/cmd/radio@v0.1.0` can resolve in a
# multi-module repository. Prereleases and build suffixes are filtered out so
# they never become the base of the next version.
LATEST = git tag --list '$(MOD)/v*' | sed 's|^$(MOD)/||' | \
         grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | sort -V | tail -n1

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
build: ## Build each command's binary in its own directory (gitignored)
	@for m in $(MODS); do echo ">> build $$m"; (cd $$m && $(GO) build .); done

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
	@for m in $(MODS); do echo ">> lint $$m"; (cd $$m && $(LINT) run); done

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
	@for m in $(MODS); do rm -f $$m/$$m; rm -rf $$m/coverdata; done
	@echo ">> cleaned"

# -------------------------------------------------------------------- version

# require-mod guards the targets that only make sense for one command.
.PHONY: require-mod
require-mod:
	@test -n "$(MOD)" || { echo "this target needs MOD=<command>; one of: $(MODULES)"; exit 1; }
	@test -f "$(MOD)/go.mod" || { echo "no such command: $(MOD); one of: $(MODULES)"; exit 1; }

.PHONY: versions
versions: ## Print the current released version of every command
	@for m in $(MODULES); do \
		v=$$(git tag --list "$$m/v*" | sed "s|^$$m/||" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | sort -V | tail -n1); \
		printf "  %-10s %s\n" "$$m" "$${v:-(unreleased)}"; done

.PHONY: version
version: require-mod ## Print the current released version of MOD
	@v=$$($(LATEST)); echo "$${v:-(unreleased)}"

.PHONY: changes
changes: require-mod ## Show the commits touching MOD since its last release
	@cur=$$($(LATEST)); \
	if [ -z "$$cur" ]; then \
		echo ">> $(MOD) has no release yet; all commits touching it:"; \
		git log --oneline -- $(MOD); \
	else \
		echo ">> commits touching $(MOD) since $$cur:"; \
		git log --oneline "$(MOD)/$$cur"..HEAD -- $(MOD); \
	fi

# A release is cut from the committed tree, so the tag points at exactly what
# was verified — not at a working copy nobody else will ever see.
.PHONY: check-clean
check-clean:
	@test -z "$$(git status --porcelain)" || \
		{ echo "working tree is dirty — commit (or stash) changes before releasing"; \
		  git status --short; exit 1; }

# check-version enforces that a hand-picked VERSION is exactly one increment
# above the module's latest tag — a patch, minor, or major step. That rejects a
# version lower than, equal to, or skipping ahead of the current one, which is
# the whole failure mode of tagging by hand in a repository with seven
# independent version lines.
.PHONY: check-version
check-version: require-mod
	@test -n "$(VERSION)" || { echo "usage: make release MOD=$(MOD) VERSION=vX.Y.Z (or: make release-patch MOD=$(MOD))"; exit 1; }
	@echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$' || \
		{ echo "VERSION must be vX.Y.Z (no prerelease/build suffix): got $(VERSION)"; exit 1; }
	@cur=$$($(LATEST)); \
	if [ -z "$$cur" ]; then \
		echo ">> first release of $(MOD) ($(VERSION)); no prior tag to compare against"; \
	else \
		cv=$${cur#v}; cM=$${cv%%.*}; cr=$${cv#*.}; cm=$${cr%%.*}; cp=$${cr##*.}; \
		np="v$$cM.$$cm.$$((cp + 1))"; nm="v$$cM.$$((cm + 1)).0"; nj="v$$((cM + 1)).0.0"; \
		case "$(VERSION)" in \
			"$$np"|"$$nm"|"$$nj") echo ">> $(VERSION) is exactly one step above $(MOD)/$$cur" ;; \
			*) echo "ERROR: $(VERSION) must be exactly one step above $(MOD)/$$cur"; \
			   echo "       allowed: $$np (patch) | $$nm (minor) | $$nj (major)"; exit 1 ;; \
		esac; \
	fi
	@git rev-parse -q --verify "refs/tags/$(MOD)/$(VERSION)" >/dev/null && \
		{ echo "ERROR: tag $(MOD)/$(VERSION) already exists"; exit 1; } || true
	@maj=$$(echo "$(VERSION)" | sed 's/^v\([0-9]*\)\..*/\1/'); \
	if [ "$$maj" -ge 2 ]; then \
		have=$$(sed -n 's/^module[[:space:]]*//p' $(MOD)/go.mod); \
		case "$$have" in \
			*"/v$$maj") echo ">> module path $$have carries the v$$maj suffix" ;; \
			*) echo "ERROR: v$$maj requires the module path to end in /v$$maj — $(MOD)/go.mod says $$have"; \
			   echo "       edit go.mod to 'module $$have/v$$maj', fix the imports, commit, then release"; \
			   exit 1 ;; \
		esac; \
	fi

# check-changes refuses to spend a version on a command nobody touched. In a
# monorepo that is an easy mistake: `git log` shows plenty of activity, all of
# it in a sibling module. Override with FORCE=1 to re-release the same tree.
.PHONY: check-changes
check-changes: require-mod
	@cur=$$($(LATEST)); \
	if [ -n "$$cur" ] && [ -z "$$(git log --oneline "$(MOD)/$$cur"..HEAD -- $(MOD))" ]; then \
		if [ -n "$(FORCE)" ]; then \
			echo ">> no changes in $(MOD) since $$cur (FORCE=1, continuing)"; \
		else \
			echo "ERROR: nothing in $(MOD) changed since $(MOD)/$$cur — nothing to release"; \
			echo "       (FORCE=1 to release anyway)"; exit 1; \
		fi; \
	fi

# ------------------------------------------------------------------- releasing

# release-suggest reads the conventional-commit subjects since the last tag and
# names the step they call for: a `!` marker or BREAKING CHANGE is breaking,
# `feat:` is a feature, anything else is a fix. Below v1 a breaking change
# becomes a minor bump, which is what semver reserves the 0.x line for.
.PHONY: release-suggest
release-suggest: require-mod ## Print the version the commits since the last tag call for
	@cur=$$($(LATEST)); \
	if [ -z "$$cur" ]; then echo "v0.1.0"; exit 0; fi; \
	log=$$(git log --format='%s%n%b' "$(MOD)/$$cur"..HEAD -- $(MOD)); \
	if [ -z "$$log" ]; then echo "$$cur (no changes)"; exit 0; fi; \
	cv=$${cur#v}; cM=$${cv%%.*}; cr=$${cv#*.}; cm=$${cr%%.*}; cp=$${cr##*.}; \
	if echo "$$log" | grep -qE '^[a-z]+(\(.+\))?!:|^BREAKING[ -]CHANGE'; then \
		if [ "$$cM" = 0 ]; then echo "v0.$$((cm + 1)).0"; else echo "v$$((cM + 1)).0.0"; fi; \
	elif echo "$$log" | grep -qE '^feat(\(.+\))?:'; then \
		echo "v$$cM.$$((cm + 1)).0"; \
	else \
		echo "v$$cM.$$cm.$$((cp + 1))"; \
	fi

.PHONY: release
release: check-clean check-version check-changes ## Tag & push a release: make release MOD=radio VERSION=v0.1.0
	@echo ">> verifying $(MOD)"; (cd $(MOD) && $(GO) build ./... && $(GO) test -short ./...)
	@echo ">> tagging $(MOD)/$(VERSION)"; \
		git tag -a "$(MOD)/$(VERSION)" -m "$(MOD) $(VERSION)"
	@echo ">> pushing $(MOD)/$(VERSION)"; git push $(ORIGIN) "$(MOD)/$(VERSION)"
	@echo ">> released: go install github.com/assanoff/cmd/$(MOD)@$(VERSION)"

.PHONY: release-auto
release-auto: require-mod check-clean ## Tag & push the version the commit messages call for
	@v=$$($(MAKE) --no-print-directory release-suggest MOD=$(MOD)); \
	case "$$v" in \
		*"no changes"*) echo "nothing in $(MOD) changed since its last release"; exit 1 ;; \
	esac; \
	echo ">> commits since the last tag suggest $$v"; \
	$(MAKE) release MOD=$(MOD) VERSION=$$v

# The explicit bumps, for when the commit messages are not the story: a patch
# that fixes what a `feat:` commit broke before anyone installed it, say.
.PHONY: release-patch
release-patch: require-mod check-clean ## Release the next patch version of MOD
	@$(MAKE) --no-print-directory release MOD=$(MOD) VERSION=$$(cur=$$($(LATEST)); \
		if [ -z "$$cur" ]; then echo v0.0.1; else \
		cv=$${cur#v}; cM=$${cv%%.*}; cr=$${cv#*.}; echo "v$$cM.$${cr%%.*}.$$(($${cr##*.} + 1))"; fi)

.PHONY: release-minor
release-minor: require-mod check-clean ## Release the next minor version of MOD
	@$(MAKE) --no-print-directory release MOD=$(MOD) VERSION=$$(cur=$$($(LATEST)); \
		if [ -z "$$cur" ]; then echo v0.1.0; else \
		cv=$${cur#v}; cM=$${cv%%.*}; cr=$${cv#*.}; echo "v$$cM.$$(($${cr%%.*} + 1)).0"; fi)

.PHONY: release-major
release-major: require-mod check-clean ## Release the next major version of MOD (needs a /vN module path)
	@$(MAKE) --no-print-directory release MOD=$(MOD) VERSION=$$(cur=$$($(LATEST)); \
		if [ -z "$$cur" ]; then echo v1.0.0; else \
		cv=$${cur#v}; echo "v$$(($${cv%%.*} + 1)).0.0"; fi)
