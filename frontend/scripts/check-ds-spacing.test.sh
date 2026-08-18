#!/usr/bin/env bash
# The spacing gate's own test — it gates the gate's CENSUS, not its verdict.
#
# check-ds-spacing.sh reports success both when it inspected every file it
# should have and when it inspected none of them, so a pathspec that quietly
# stops matching is indistinguishable from a clean tree. That is exactly what
# happened: `extensions/*/frontend/**/*.tsx` was the only pattern for the unit
# tier, a git pathspec `**/` requires an intermediate directory, and every unit
# screen sits DIRECTLY at extensions/<unit>/frontend/screen.tsx — so three
# shipped units were exempt from the rule while the gate said PASS.
#
# The pathspec lists are read out of the gate itself rather than restated here.
# A test that spells its own copy of the thing under test passes against the
# copy, not against production.
#
# Usage: bash frontend/scripts/check-ds-spacing.test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE="$SCRIPT_DIR/check-ds-spacing.sh"
ROOT="$(cd "$SCRIPT_DIR" && git rev-parse --show-toplevel)"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAILURES=0

fail() {
  echo "FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}

# The gate's own declarations, evaluated verbatim. Two lines, or the gate has
# been restructured and this test is reading something else.
DECLS="$(grep -E '^(TSX|CSS)_PATHSPEC=\(' "$GATE" || true)"
if [[ "$(printf '%s\n' "$DECLS" | grep -c .)" -ne 2 ]]; then
  echo "FAIL: could not read TSX_PATHSPEC and CSS_PATHSPEC out of $GATE — the" >&2
  echo "      gate no longer declares them on one line each, so this test is" >&2
  echo "      gating nothing. Re-point it at wherever the pathspecs now live." >&2
  exit 1
fi
eval "$DECLS"

# ---------------------------------------------------------------------------
# 1. The property, on a synthetic tree: every tree a pathspec list names must
#    be matched at BOTH depths — a file directly in it and a file nested under
#    it. Synthetic because the real repo ships no unit CSS, so the *.css half
#    of the defect is unreachable here and a census over real files would prove
#    nothing about it. `git ls-files` is the pathspec engine `git diff` and
#    `git ls-files --others` in the gate both match with.
# ---------------------------------------------------------------------------
REPO="$TMP/repo"
SHAPES=(
  frontend/src/App
  frontend/src/screens/deals/DealList
  extensions/probe/frontend/screen
  extensions/probe/frontend/views/panel
)
for shape in "${SHAPES[@]}"; do
  mkdir -p "$REPO/$(dirname "$shape")"
  : >"$REPO/$shape.tsx"
  : >"$REPO/$shape.css"
done
git -C "$REPO" init --quiet
for shape in "${SHAPES[@]}"; do
  git -C "$REPO" add -- "$shape.tsx" "$shape.css"
done

check_shape_census() {
  local kind="$1"
  shift
  local want got
  want="$(printf '%s\n' "${SHAPES[@]/%/.$kind}" | sort)"
  got="$(git -C "$REPO" ls-files -- "$@" | sort)"
  if [[ "$got" != "$want" ]]; then
    fail "the *.$kind pathspecs miss files the gate claims to inspect:"
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") >&2 || true
  fi
}

check_shape_census tsx "${TSX_PATHSPEC[@]}"
check_shape_census css "${CSS_PATHSPEC[@]}"

# ---------------------------------------------------------------------------
# 2. The census against THIS repo: the pathspecs must collect every file of
#    that suffix under the two trees the gate says it gates. The expected set
#    is derived from the trees rather than from the patterns, so the two cannot
#    agree by both being wrong.
# ---------------------------------------------------------------------------
check_repo_census() {
  local kind="$1"
  shift
  local want got
  want="$(git -C "$ROOT" ls-files -- frontend/src extensions \
    | grep -E "^(frontend/src/|extensions/[^/]+/frontend/).*\.$kind\$" | sort || true)"
  got="$(git -C "$ROOT" ls-files -- "$@" | sort)"
  if [[ "$got" != "$want" ]]; then
    fail "the *.$kind pathspecs do not collect every tracked *.$kind under frontend/src and extensions/*/frontend:"
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") >&2 || true
  fi
}

check_repo_census tsx "${TSX_PATHSPEC[@]}"
check_repo_census css "${CSS_PATHSPEC[@]}"

# A count, not a verdict: the unit tier ships screens today, so a collection
# that reaches none of them is a broken pathspec however green the gate reads.
EXT_TSX="$(git -C "$ROOT" ls-files -- "${TSX_PATHSPEC[@]}" | grep -c '^extensions/' || true)"
if [[ "$EXT_TSX" -eq 0 ]]; then
  fail "the *.tsx pathspecs collect 0 files under extensions/*/frontend, and the tier ships screens — the gate is inspecting nothing there"
fi

if [[ "$FAILURES" -ne 0 ]]; then
  echo "" >&2
  echo "check-ds-spacing.sh's collectors are no longer reaching every file it" >&2
  echo "reports on. Note that a git pathspec '**/' requires an intermediate" >&2
  echo "directory: a recursive pattern needs its direct-child sibling beside it" >&2
  echo "('<tree>/**/*.tsx' AND '<tree>/*.tsx')." >&2
  exit 1
fi

echo "==> DS spacing census: ${#TSX_PATHSPEC[@]} *.tsx and ${#CSS_PATHSPEC[@]} *.css pathspecs reach both depths of every gated tree ($EXT_TSX unit-tier *.tsx collected)"
