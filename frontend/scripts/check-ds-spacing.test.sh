#!/usr/bin/env bash
# The spacing gate's own test — it gates the gate's CENSUS, not its verdict.
#
# check-ds-spacing.sh reports success both when it inspected every file it
# should have and when it inspected none of them, so a pathspec that quietly
# stops matching is indistinguishable from a clean tree. That is exactly what
# happened: 'extensions/*/frontend/**/*.tsx' was the only pattern for the unit
# tier, a git pathspec `**/` requires an intermediate directory, and every unit
# screen sits DIRECTLY at extensions/<unit>/frontend/screen.tsx — so three
# shipped units were exempt from the rule while the gate said PASS.
#
# Three things have to hold, and each is checked separately, because any one of
# them alone can be true while the gate collects nothing:
#
#   1. the pathspecs match every depth of every tree they name;
#   2. the collectors actually EXPAND those pathspecs, and reach for no others;
#   3. what they collect in THIS repo is every file of that suffix in it.
#
# The pathspec lists are read out of the gate itself rather than restated here.
# A test that spells its own copy of the thing under test passes against the
# copy, not against production.
#
# Usage: bash frontend/scripts/check-ds-spacing.test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE="$SCRIPT_DIR/check-ds-spacing.sh"

# The gate is deliberately fail-open outside a git checkout (a source export, a
# container that refuses a dubious-ownership repo). Its test has to skip on the
# same terms rather than turn `make fe-ds-gates` red for a reason that has
# nothing to do with spacing.
ROOT="$(cd "$SCRIPT_DIR" && git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$ROOT" ]]; then
  echo "==> DS spacing census: not a git checkout — skipped"
  exit 0
fi

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
# 1. The property, on a synthetic tree: each list must match every depth of
#    every tree it names — a file directly in it and a file nested under it.
#    Synthetic because the real repo ships no unit CSS, so the *.css half of the
#    defect is unreachable here and a census over real files would prove nothing
#    about it. `git ls-files` is the pathspec engine `git diff` and
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
# 2. The collectors reach for those lists and for nothing else. Reading the
#    arrays proves what they say, not what is used: re-inlining one call site
#    back to a literal leaves every other assertion here green while new unit
#    screens go uninspected again. Four call sites — tracked and untracked,
#    tsx and css — and one authority between them.
# ---------------------------------------------------------------------------
for var in TSX_PATHSPEC CSS_PATHSPEC; do
  uses="$(grep -c -F "\"\${${var}[@]}\"" "$GATE" || true)"
  if [[ "$uses" -ne 2 ]]; then
    fail "\$$var is expanded at $uses call site(s) in check-ds-spacing.sh, expected 2 (the tracked diff and the untracked listing)"
  fi
done

# A quoted *.tsx / *.css glob on any CODE line but the two declarations is a
# second authority — the shape this gate already regressed into once. Comment
# lines are dropped first: the pathspec trap is explained in prose right above
# the declarations, and prose is not a call site.
STRAY="$(grep -nE "'[^']*\*\.(tsx|css)'" "$GATE" \
  | grep -vE '^[0-9]+:[[:space:]]*#' \
  | grep -vE '^[0-9]+:(TSX|CSS)_PATHSPEC=' || true)"
if [[ -n "$STRAY" ]]; then
  fail "check-ds-spacing.sh names a *.tsx/*.css pathspec outside TSX_PATHSPEC/CSS_PATHSPEC:"
  printf '%s\n' "$STRAY" >&2
fi

# ---------------------------------------------------------------------------
# 3. The census against THIS repo: the pathspecs must collect every tracked
#    file of that suffix under the two trees the gate says it gates. The
#    expected set is derived from the trees rather than from the patterns, so
#    the two cannot agree by both being wrong — and it is spelled with the same
#    depth-spanning semantics a git pathspec `*` has, so a unit that vendors a
#    sub-tree is expected on both sides rather than failing as a surplus.
# ---------------------------------------------------------------------------
check_repo_census() {
  local kind="$1"
  shift
  local want got
  want="$(git -C "$ROOT" ls-files -- frontend/src extensions \
    | grep -E "^(frontend/src/|extensions/.+/frontend/).*\.$kind\$" | sort || true)"
  got="$(git -C "$ROOT" ls-files -- "$@" | sort)"
  if [[ "$got" != "$want" ]]; then
    fail "the *.$kind pathspecs do not collect exactly the tracked *.$kind under frontend/src and extensions/*/frontend:"
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") >&2 || true
  fi
}

check_repo_census tsx "${TSX_PATHSPEC[@]}"
check_repo_census css "${CSS_PATHSPEC[@]}"

# A count, not a verdict — but only where there is something to count.
# extensions/ is the fork-owned tier: a downstream shipping only backend units
# has no unit frontend at all, and "the gate inspects nothing there" is then a
# true statement about an empty tree rather than a broken pathspec.
EXT_TSX="$(git -C "$ROOT" ls-files -- "${TSX_PATHSPEC[@]}" | grep -c '^extensions/' || true)"
EXT_SCREENS="$(git -C "$ROOT" ls-files -- 'extensions/*/frontend/*' | grep -c . || true)"
if [[ "$EXT_SCREENS" -gt 0 && "$EXT_TSX" -eq 0 ]]; then
  fail "the unit tier ships $EXT_SCREENS frontend file(s) and the *.tsx pathspecs collect 0 of them — the gate is inspecting nothing there"
fi

if [[ "$FAILURES" -ne 0 ]]; then
  echo "" >&2
  echo "check-ds-spacing.sh's collectors are no longer reaching every file it" >&2
  echo "reports on. Note that a git pathspec is not :(glob): its '*' spans" >&2
  echo "directory separators, so ONE '<tree>/*.tsx' covers every depth, while" >&2
  echo "'<tree>/**/*.tsx' requires an intermediate directory and misses the" >&2
  echo "files sitting directly in <tree>." >&2
  exit 1
fi

echo "==> DS spacing census: the *.tsx and *.css pathspecs reach every depth of every gated tree, from both collectors ($EXT_TSX unit-tier *.tsx collected)"
