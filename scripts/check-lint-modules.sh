#!/usr/bin/env bash
# golangci-lint over the Go modules the backend lint lane cannot reach.
#
# `make -C backend lint` runs `golangci-lint run ./...` from backend/, and
# `./...` stops at the module boundary. The repo has twelve Go modules; that
# pattern covers exactly one. The other eleven — backend/tools, cli/craft,
# composition, the units under extensions/, and the deliberately-broken
# fixtures/extensions/ — were linted by nothing. Not by CI, not by `make check`,
# not by the pre-push hook, which only sees the craft gate. That is how an
# unformatted file reached main with every gate green (extjobs.go), and the
# formatting was the visible half: the same blind spot was also hiding two
# swallowed errors, two misspellings and nine staticcheck findings.
#
# Every module is linted against the SAME backend/.golangci.yml. One config is
# the point: a per-module config would have to restate the baseline (golangci
# has no config inheritance), and a second copy of a shared bar drifts from it.
# Where a baseline rule genuinely does not fit a build-time generator, the
# waiver is `//nolint:<linter> // <reason>` on the line it applies to — which
# the config's own forbidigo comment establishes as the sanctioned escape hatch,
# and which keeps the justification next to the code instead of in a path list
# that outlives whatever it was written for.
#
# The module list is derived from tracked go.mod files, so a new module is
# covered the day it is committed. `backend` itself is the one exclusion, and
# not an arbitrary one: it is the module the backend lane already lints twice
# (baseline repo-wide, plus the strict new-code pass), so linting it again here
# would only slow the gate down to reprint the same findings.
set -euo pipefail
cd "$(dirname "$0")/.."

CONFIG="$PWD/backend/.golangci.yml"
COMPOSED_WORK="$PWD/build/composition/go.work"

# Prefer the version-pinned install from `make tools` over whatever is on PATH,
# for the reason backend/Makefile spells out: a Homebrew golangci-lint would
# silently lint at a version CI does not run.
PINNED="$(go env GOPATH)/bin/golangci-lint"
if [[ -x "$PINNED" ]]; then
  GOLANGCI="$PINNED"
elif command -v golangci-lint >/dev/null 2>&1; then
  GOLANGCI="$(command -v golangci-lint)"
else
  echo "FAIL: golangci-lint not found — run 'make tools'"
  exit 1
fi

# The composed workspace resolves the extension units against the product
# module. It is generated, so a caller who has never run a build lane has no
# composition yet; `make lint-modules` depends on the composition target, and
# this guard turns a direct invocation into an instruction rather than a
# confusing resolution error.
if [[ ! -f "$COMPOSED_WORK" ]]; then
  echo "FAIL: $COMPOSED_WORK is missing — run 'make composition' first"
  exit 1
fi

# Two exclusions, both for a stated reason rather than to make the gate pass.
#
# `backend` is the product module, which the backend lane already lints twice
# (baseline repo-wide + the strict new-code pass); repeating it here would only
# slow the gate down to reprint the same findings.
#
# `fixtures/` cannot be type-checked at all outside the harness that uses it.
# Those units import backend/pkg/extension while declaring no require for it —
# deliberately, because a fixture must not join the real build — so they resolve
# only inside the temporary workspace the tests compose for them. golangci needs
# a type-checkable package; it has nothing to say here. They are NOT unchecked
# code: the craft gate, the license header test and the tree-wide gofmt gate all
# cover fixtures/, and none of the three needs to typecheck to do its job.
modules="$(git ls-files '*go.mod' \
  | sed 's#/\?go\.mod$##' \
  | grep -v '^backend$' \
  | grep -v '^fixtures/' \
  | sort)"
if [[ -z "$modules" ]]; then
  echo "FAIL: lint-modules — found no modules to lint; this gate cannot pass by scanning nothing"
  exit 1
fi

# Which modules the composed workspace actually contains, read out of the
# generated go.work rather than assumed. A member (backend/tools, cli/craft, the
# extension units) MUST be linted inside the workspace, because that is what
# resolves its dependency on the product module. A non-member (the committed
# composition stub, the fixtures/extensions/ units, which are standalone by
# design so a broken fixture cannot break the real build) must be linted with
# GOWORK=off: inside a workspace that does not list it, every package fails to
# typecheck and golangci reports "0 issues" while exiting non-zero — a gate that
# looks like it passed and did not run.
members="$(sed -n 's#^[[:space:]]*\.\./\.\./##p' "$COMPOSED_WORK")"

failed=""
count=0
for mod in $modules; do
  count=$((count + 1))
  if grep -qxF "$mod" <<<"$members"; then
    work="$COMPOSED_WORK"
  else
    work="off"
  fi
  # Uncapped, and that is load-bearing. golangci defaults to max-same-issues=3,
  # so a repeated finding prints three times and hides the rest — while this gate
  # was being armed, fixing the three visible G304s revealed four more that the
  # cap had been suppressing all along. A gate that truncates reads exactly like
  # a gate that found everything.
  if ! (cd "$mod" && GOWORK="$work" "$GOLANGCI" run --config "$CONFIG" \
        --max-same-issues=0 --max-issues-per-linter=0 ./...); then
    failed="$failed $mod"
  fi
done

if [[ -n "$failed" ]]; then
  echo
  echo "FAIL — golangci-lint findings in:$failed"
  echo
  echo "These modules are outside 'golangci-lint run ./...' from backend/, so they are"
  echo "linted only here. Fix the finding, or waive a genuine false positive in source"
  echo "with '//nolint:<linter> // <reason>' on the line it applies to."
  exit 1
fi

echo "OK: lint-modules — $count module(s) outside backend/ lint clean"
