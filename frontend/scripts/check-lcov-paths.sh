#!/usr/bin/env bash
# lcov path gate: every record in the frontend coverage report names a file the
# SonarCloud scanner can actually find.
#
# The scanner resolves a relative `SF:` path against ITS base directory, which
# is the repo root. Vitest's root is frontend/, so an unconfigured report writes
# `SF:src/App.tsx` — a path that exists under frontend/ and nowhere the scanner
# looks. Unresolvable records are dropped without a word: the scan succeeds, the
# project reports backend coverage as though it were the whole measurement, and
# a frontend PR with no tests scores exactly what a fully covered one scores.
# That held from the day the report was first handed to the scanner (#38) until
# #1541, because nothing between vitest and the scan ever read the file. This is
# the read.
#
# Usage: frontend/scripts/check-lcov-paths.sh <lcov file>
#        (wired into `make fe-unit`, on the FE_COVERAGE runs that write one)

set -euo pipefail

REPORT="${1:-}"
if [[ -z "$REPORT" ]]; then
  echo "usage: $0 <lcov file>" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ ! -f "$REPORT" ]]; then
  echo "FAIL: no coverage report at $REPORT — the sonarcloud job would scan with none" >&2
  exit 1
fi

TOTAL=0
MISSING=0
PRODUCT=0
FIRST_MISSING=""

while IFS= read -r rel; do
  TOTAL=$((TOTAL + 1))
  if [[ -f "$REPO_ROOT/$rel" ]]; then
    case "$rel" in
      frontend/src/*) PRODUCT=$((PRODUCT + 1)) ;;
    esac
  else
    MISSING=$((MISSING + 1))
    [[ -n "$FIRST_MISSING" ]] || FIRST_MISSING="$rel"
  fi
done < <(sed -n 's/^SF://p' "$REPORT")

echo "==> lcov path check ($TOTAL records in $REPORT)"

if [[ "$TOTAL" -eq 0 ]]; then
  echo "FAIL: $REPORT carries no SF: records at all — the scan would measure nothing" >&2
  exit 1
fi

if [[ "$MISSING" -gt 0 ]]; then
  echo "FAIL: $MISSING of $TOTAL records name no file under the repo root" >&2
  echo "      repo root: $REPO_ROOT" >&2
  echo "      first:     SF:$FIRST_MISSING" >&2
  echo "" >&2
  echo "A path is written relative to vite.config.ts's coverage.reporter" >&2
  echo "projectRoot, and the scanner reads it relative to the repo root. Those" >&2
  echo "two have to be the same directory, or every record is silently dropped." >&2
  exit 1
fi

if [[ "$PRODUCT" -eq 0 ]]; then
  echo "FAIL: no record under frontend/src — the product code is outside the report" >&2
  echo "      ($TOTAL records, all of them config or tooling files)" >&2
  exit 1
fi

echo "PASS — every path resolves from the repo root, $PRODUCT of them under frontend/src"
