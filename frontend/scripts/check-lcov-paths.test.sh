#!/usr/bin/env bash
# The lcov path gate's own test. Every case is a SYNTHETIC report — the real one
# is written only on an FE_COVERAGE run and is supposed to pass, and a gate
# proven by "today's report is fine" keeps passing after it stops working.
#
# The reports name REAL repo paths on purpose: the gate resolves against its own
# location, so the only honest way to exercise it is to hand it paths that do
# and do not exist where it looks.
#
# Usage: bash frontend/scripts/check-lcov-paths.test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE="$SCRIPT_DIR/check-lcov-paths.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAILURES=0

# One lcov record. The gate reads SF: alone, but the surrounding lines are
# written anyway so a case cannot pass by being unparseable.
record() {
  printf 'TN:\nSF:%s\nDA:1,1\nLF:1\nLH:1\nend_of_record\n' "$1"
}

write_report() {
  local name="$1"
  shift
  : >"$TMP/$name"
  local path
  for path in "$@"; do
    record "$path" >>"$TMP/$name"
  done
  echo "$TMP/$name"
}

expect_refusal() {
  local name="$1" report="$2" want="$3"
  local out status=0
  out="$(bash "$GATE" "$report" 2>&1)" || status=$?
  if [[ "$status" -eq 0 ]]; then
    echo "FAIL: the gate accepted $name" >&2
    FAILURES=$((FAILURES + 1))
  elif ! grep -q -- "$want" <<<"$out"; then
    echo "FAIL: $name refused, but the message does not mention '$want':" >&2
    echo "$out" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

expect_accepted() {
  local name="$1" report="$2"
  local out status=0
  out="$(bash "$GATE" "$report" 2>&1)" || status=$?
  if [[ "$status" -ne 0 ]]; then
    echo "FAIL: the gate refused $name:" >&2
    echo "$out" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

# THE defect, spelled exactly as vitest wrote it until #1541: paths relative to
# frontend/ rather than to the repo root the scanner reads them from.
expect_refusal "a report written relative to frontend/" \
  "$(write_report frontend-relative.info "src/App.tsx" "vite.config.ts")" \
  "name no file under the repo root"

# The same paths from the repo root — the shape the fix produces.
expect_accepted "a report written relative to the repo root" \
  "$(write_report repo-relative.info "frontend/src/App.tsx" "frontend/vite.config.ts")"

# An absolute path resolves on the machine that wrote it and nowhere else. The
# scanner would take it; a report that only works from one checkout directory is
# not a report this lane can hand between two jobs.
expect_refusal "a report of absolute paths" \
  "$(write_report absolute.info "/nonexistent/checkout/frontend/src/App.tsx")" \
  "name no file under the repo root"

# A file that has since been deleted or renamed — the same silent drop, arriving
# through the source tree rather than through the reporter config.
expect_refusal "a report naming a file that no longer exists" \
  "$(write_report stale.info "frontend/src/App.tsx" "frontend/src/deleted-screen.tsx")" \
  "name no file under the repo root"

# Coverage narrowed to tooling: every path resolves, and the product is still
# unmeasured. Path resolution alone would call this healthy.
expect_refusal "a report holding no frontend/src file" \
  "$(write_report tooling-only.info "frontend/vite.config.ts")" \
  "no record under frontend/src"

# An empty report is what a failed instrumentation run leaves behind, and it is
# indistinguishable downstream from a suite that covers nothing.
: >"$TMP/empty.info"
expect_refusal "an empty report" "$TMP/empty.info" "carries no SF: records"

# No report at all — the case where the producer never ran.
expect_refusal "a report that was never written" "$TMP/absent.info" \
  "no coverage report at"

if [[ "$FAILURES" -ne 0 ]]; then
  echo "" >&2
  echo "check-lcov-paths.test.sh: $FAILURES case(s) failed" >&2
  exit 1
fi

echo "PASS — check-lcov-paths.sh holds on all 7 cases"
