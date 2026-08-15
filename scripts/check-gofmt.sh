#!/usr/bin/env bash
# gofmt gate over EVERY tracked Go file in the repo, in every module.
#
# golangci-lint enforces gofmt (it is in .golangci.yml's `formatters:`), and
# `make lint-modules` now runs it over the modules `golangci-lint run ./...`
# from backend/ cannot reach. This gate is not that gate, and it is not
# redundant with it: golangci needs a type-checkable package, and the units
# under fixtures/ deliberately are not one — they import the product module
# while declaring no require for it, so they resolve only inside the harness
# that composes them. gofmt needs to parse a file and nothing more, so it is
# the one formatting check that reaches every module without exception.
#
# It is also the cheap floor. It runs in well under a second and holds even
# when a module is temporarily unlintable, which is what a floor is for.
#
# Deriving the file list from `git ls-files` rather than naming the modules is
# the point: a new module, or a unit added under extensions/, is covered the
# day it is committed. There is no list to keep in step.
#
# WHAT THIS CATCHES: a tracked, hand-written Go file that gofmt would rewrite.
#
# WHAT THIS DOES NOT CATCH, deliberately: gofumpt's stricter set. That bar is
# .golangci.strict.yml's, scoped to new code via new-from-merge-base; applying it
# tree-wide would be a burn-down, and this gate exists to hold a floor under
# every module, not to raise the ceiling on one.
#
# Generated files (*_gen.go / *.gen.go) are exempt: their generator owns their
# bytes and the drift gate proves it, so a finding here could only ask an
# engineer to hand-edit a file the repo forbids hand-editing.
set -euo pipefail
cd "$(dirname "$0")/.."

# gofmt lives next to the toolchain, which is not necessarily on PATH.
GOFMT="$(go env GOROOT)/bin/gofmt"
[[ -x "$GOFMT" ]] || GOFMT=gofmt

files="$(git ls-files '*.go' | grep -v -e '_gen\.go$' -e '\.gen\.go$' || true)"
if [[ -z "$files" ]]; then
  echo "FAIL: gofmt — no tracked Go files found; this gate cannot pass by scanning nothing"
  exit 1
fi

unformatted="$(echo "$files" | xargs "$GOFMT" -l)"

if [[ -n "$unformatted" ]]; then
  echo "FAIL — these Go files are not gofmt-clean:"
  echo "$unformatted" | sed 's/^/  /'
  echo
  echo "Run: gofmt -w <file>"
  exit 1
fi

echo "OK: gofmt — all $(echo "$files" | wc -l | tr -d ' ') hand-written Go files across every module are formatted"
