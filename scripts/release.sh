#!/usr/bin/env bash
# Versioning and releasing for this multi-module repository. The Makefile is a
# thin wrapper over the subcommands below; everything that needs more than a
# line of shell lives here, where it can be read without make's escaping.
#
# Reads MOD, VERSION, FORCE, ORIGIN and GO from the environment — the Makefile
# passes them through, so `make release MOD=radio VERSION=v0.1.0` and
# `MOD=radio VERSION=v0.1.0 scripts/release.sh release` behave the same.
set -euo pipefail

cd "$(dirname "$0")/.."

GO=${GO:-go}
ORIGIN=${ORIGIN:-origin}
MOD=${MOD:-}
VERSION=${VERSION:-}
FORCE=${FORCE:-}

die() {
	echo "$@" >&2
	exit 1
}

# Every directory with a go.mod is a command and its own module, so the module
# list is discovered rather than maintained by hand.
modules() {
	local m
	for m in */go.mod; do
		[ -f "$m" ] && echo "${m%/go.mod}"
	done
}

require_mod() {
	[ -n "$MOD" ] || die "this target needs MOD=<command>; one of: $(modules | xargs)"
	[ -f "$MOD/go.mod" ] || die "no such command: $MOD; one of: $(modules | xargs)"
}

# latest prints the newest release tag of $1 as a bare vX.Y.Z, or nothing at
# all when the command has never been released. Tags carry the module directory
# as a prefix (radio/v0.1.0), because that is the only form
# `go install github.com/assanoff/cmd/radio@v0.1.0` can resolve in a
# multi-module repository. Prereleases and build suffixes are filtered out so
# they never become the base of the next version.
# `|| true` because a command with no matching tag is the unreleased case, not
# an error — grep's empty-result exit status would end the script under -e.
latest() {
	git tag --list "$1/v*" | sed "s|^$1/||" |
		grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1 || true
}

# split sets MAJ, MIN and PAT from a vX.Y.Z string.
split() {
	local v=${1#v} rest
	MAJ=${v%%.*}
	rest=${v#*.}
	MIN=${rest%%.*}
	PAT=${rest##*.}
}

# next echoes the version one $2 step (patch|minor|major) above $1.
next() {
	split "$1"
	case "$2" in
	patch) echo "v$MAJ.$MIN.$((PAT + 1))" ;;
	minor) echo "v$MAJ.$((MIN + 1)).0" ;;
	major) echo "v$((MAJ + 1)).0.0" ;;
	esac
}

# commits_since lists the commits touching $MOD since its tag $1.
commits_since() {
	git log --oneline "$MOD/$1"..HEAD -- "$MOD"
}

# ------------------------------------------------------------------ the checks

# A release is cut from the committed tree, so the tag points at exactly what
# was verified — not at a working copy nobody else will ever see.
check_clean() {
	[ -z "$(git status --porcelain)" ] || {
		echo "working tree is dirty — commit (or stash) changes before releasing" >&2
		git status --short >&2
		exit 1
	}
}

# check_version enforces that a hand-picked VERSION is exactly one increment
# above the module's latest tag — a patch, minor, or major step. That rejects a
# version lower than, equal to, or skipping ahead of the current one, which is
# the whole failure mode of tagging by hand in a repository with seven
# independent version lines.
check_version() {
	require_mod
	[ -n "$VERSION" ] ||
		die "usage: make release MOD=$MOD VERSION=vX.Y.Z (or: make release-patch MOD=$MOD)"
	echo "$VERSION" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
		die "VERSION must be vX.Y.Z (no prerelease/build suffix): got $VERSION"

	local cur np nm nj
	cur=$(latest "$MOD")
	if [ -z "$cur" ]; then
		echo ">> first release of $MOD ($VERSION); no prior tag to compare against"
	else
		np=$(next "$cur" patch)
		nm=$(next "$cur" minor)
		nj=$(next "$cur" major)
		case "$VERSION" in
		"$np" | "$nm" | "$nj") echo ">> $VERSION is exactly one step above $MOD/$cur" ;;
		*)
			echo "ERROR: $VERSION must be exactly one step above $MOD/$cur" >&2
			die "       allowed: $np (patch) | $nm (minor) | $nj (major)"
			;;
		esac
	fi

	! git rev-parse -q --verify "refs/tags/$MOD/$VERSION" >/dev/null ||
		die "ERROR: tag $MOD/$VERSION already exists"

	# From v2 on, the import path has to carry the major version.
	split "$VERSION"
	if [ "$MAJ" -ge 2 ]; then
		local have
		have=$(sed -n 's/^module[[:space:]]*//p' "$MOD/go.mod")
		case "$have" in
		*"/v$MAJ") echo ">> module path $have carries the v$MAJ suffix" ;;
		*)
			echo "ERROR: v$MAJ requires the module path to end in /v$MAJ — $MOD/go.mod says $have" >&2
			die "       edit go.mod to 'module $have/v$MAJ', fix the imports, commit, then release"
			;;
		esac
	fi
}

# check_changes refuses to spend a version on a command nobody touched. In a
# monorepo that is an easy mistake: `git log` shows plenty of activity, all of
# it in a sibling module. Override with FORCE=1 to re-release the same tree.
check_changes() {
	require_mod
	local cur
	cur=$(latest "$MOD")
	if [ -n "$cur" ] && [ -z "$(commits_since "$cur")" ]; then
		if [ -n "$FORCE" ]; then
			echo ">> no changes in $MOD since $cur (FORCE=1, continuing)"
		else
			echo "ERROR: nothing in $MOD changed since $MOD/$cur — nothing to release" >&2
			die "       (FORCE=1 to release anyway)"
		fi
	fi
}

# ------------------------------------------------------------------- reporting

cmd_versions() {
	local m v
	while read -r m; do
		v=$(latest "$m")
		printf "  %-10s %s\n" "$m" "${v:-(unreleased)}"
	done < <(modules)
}

cmd_version() {
	require_mod
	local v
	v=$(latest "$MOD")
	echo "${v:-(unreleased)}"
}

cmd_changes() {
	require_mod
	local cur
	cur=$(latest "$MOD")
	if [ -z "$cur" ]; then
		echo ">> $MOD has no release yet; all commits touching it:"
		git log --oneline -- "$MOD"
	else
		echo ">> commits touching $MOD since $cur:"
		commits_since "$cur"
	fi
}

# cmd_suggest reads the conventional-commit subjects since the last tag and
# names the step they call for: a `!` marker or BREAKING CHANGE is breaking,
# `feat:` is a feature, anything else is a fix. Below v1 a breaking change
# becomes a minor bump, which is what semver reserves the 0.x line for.
cmd_suggest() {
	require_mod
	local cur log
	cur=$(latest "$MOD")
	[ -n "$cur" ] || {
		echo "v0.1.0"
		return
	}
	log=$(git log --format='%s%n%b' "$MOD/$cur"..HEAD -- "$MOD")
	[ -n "$log" ] || {
		echo "$cur (no changes)"
		return
	}
	split "$cur"
	if grep -qE '^[a-z]+(\(.+\))?!:|^BREAKING[ -]CHANGE' <<<"$log"; then
		if [ "$MAJ" = 0 ]; then next "$cur" minor; else next "$cur" major; fi
	elif grep -qE '^feat(\(.+\))?:' <<<"$log"; then
		next "$cur" minor
	else
		next "$cur" patch
	fi
}

# ------------------------------------------------------------------- releasing

cmd_release() {
	check_clean
	check_version
	check_changes
	echo ">> verifying $MOD"
	(cd "$MOD" && $GO build ./... && $GO test -short ./...)
	echo ">> tagging $MOD/$VERSION"
	git tag -a "$MOD/$VERSION" -m "$MOD $VERSION"
	echo ">> pushing $MOD/$VERSION"
	git push "$ORIGIN" "$MOD/$VERSION"
	echo ">> released: go install github.com/assanoff/cmd/$MOD@$VERSION"
}

# cmd_auto releases the version the commit messages call for.
cmd_auto() {
	require_mod
	check_clean
	local v
	v=$(cmd_suggest)
	case "$v" in
	*"no changes"*) die "nothing in $MOD changed since its last release" ;;
	esac
	echo ">> commits since the last tag suggest $v"
	VERSION=$v
	cmd_release
}

# cmd_bump releases the next patch, minor or major version — for when the
# commit messages are not the story: a patch that fixes what a `feat:` commit
# broke before anyone installed it, say.
cmd_bump() {
	require_mod
	check_clean
	local kind=$1 cur v
	cur=$(latest "$MOD")
	if [ -z "$cur" ]; then
		case "$kind" in
		patch) v=v0.0.1 ;;
		minor) v=v0.1.0 ;;
		major) v=v1.0.0 ;;
		esac
	else
		v=$(next "$cur" "$kind")
	fi
	VERSION=$v
	cmd_release
}

case "${1:-}" in
list) modules ;;
require-mod) require_mod ;;
check-clean) check_clean ;;
check-version) check_version ;;
check-changes) check_changes ;;
versions) cmd_versions ;;
version) cmd_version ;;
changes) cmd_changes ;;
suggest) cmd_suggest ;;
release) cmd_release ;;
auto) cmd_auto ;;
bump) cmd_bump "${2:?bump needs patch|minor|major}" ;;
*) die "usage: $0 {list|versions|version|changes|suggest|release|auto|bump <patch|minor|major>}" ;;
esac
