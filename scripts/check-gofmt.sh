#!/usr/bin/env bash
# gofmt gate over EVERY Go module in the repo, not just the product one.
#
# golangci-lint enforces gofmt (it is in .golangci.yml's `formatters:`), but it
# runs as `golangci-lint run ./...` from backend/, and `./...` stops at the
# module boundary. The repo has five Go trees — backend, backend/tools, cli/craft,
# extensions/<unit>, fixtures — and only the first is inside that pattern. The
# other four were reachable by exactly one thing: the pre-commit hook, and only
# for whoever happened to stage the file. So an unformatted file could sit on
# main with `make check` green, which is how backend/tools/gen-composition/
# extjobs.go did.
#
# Deriving the file list from `git ls-files` rather than naming the modules is
# the point: a sixth module, or a unit added under extensions/, is covered the
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
  echo "Run: gofmt -w <file>   (or 'make lint' for the backend module's full set)"
  exit 1
fi

echo "OK: gofmt — all $(echo "$files" | wc -l | tr -d ' ') hand-written Go files across every module are formatted"
