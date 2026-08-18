#!/usr/bin/env bash
# The frontend-contract-drift gate's own test — it gates the gate's SKIP PATH,
# which is the half that can quietly stop working.
#
# check-contract-frontend-drift.sh has to be skippable, because `make
# check-backend` must run on a bare Go checkout with no Node toolchain. That is
# also the whole risk: Wave 1's #1637 was a pathspec that matched nothing and
# reported PASS, and a pnpm-absent skip that nobody notices is the same failure
# wearing a different coat. So the rules that hold the skip shut are asserted
# here rather than trusted:
#
#   1. Without pnpm it skips — LOUDLY, on stderr, naming the leg, the reason and
#      the artifacts it did not check.
#   2. The skip's ONLY trigger is pnpm's absence. Every other early success is a
#      path that reports green having compared nothing, and this checks for the
#      class rather than for one spelling of it — the first version matched a
#      bare `exit 0` on its own line and let `[[ -d node_modules ]] || exit 0`
#      straight through.
#   3. The census runs BEFORE any exit, so an emptied artifact list cannot exit
#      0 down the skip path saying it checked nothing.
#
# Not asserted here, because it is not this file's job: whether CI meets the
# check at all. CI's deterministic-gates job has no pnpm and takes the skip; the
# pull-request path is covered by fe-quality's `make fe-drift`, and that routing
# is pinned by TestTheContractReachesTheFrontendLane in the backend suite.
#
# Usage: bash scripts/check-contract-frontend-drift.test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE="$SCRIPT_DIR/check-contract-frontend-drift.sh"
FAILURES=0

fail() { echo "FAIL: $*" >&2; FAILURES=$((FAILURES + 1)); }

# A PATH holding ONLY the tools the gate needs, and deliberately not /usr/bin:
# a host with a system-wide pnpm would otherwise resolve it and this test would
# assert nothing while reporting success. Every tool is linked in explicitly, so
# a missing one is named rather than surfacing as a confusing gate failure.
TMPBIN="$(mktemp -d)"
trap 'rm -rf "$TMPBIN"' EXIT
for tool in bash git find mktemp touch sed awk cat rm dirname basename cd pwd; do
  p="$(command -v "$tool" 2>/dev/null)" || continue
  ln -sf "$p" "$TMPBIN/$tool"
done
for required in bash git find mktemp; do
  [[ -x "$TMPBIN/$required" ]] || { echo "FAIL: $required is not on PATH, so this test cannot build a pnpm-free environment" >&2; exit 1; }
done
NOPNPM_PATH="$TMPBIN"
if PATH="$NOPNPM_PATH" command -v pnpm >/dev/null 2>&1; then
  echo "FAIL: this test's pnpm-free PATH still resolves pnpm, so case 1 would" >&2
  echo "      assert nothing. NOPNPM_PATH must contain no pnpm." >&2
  exit 1
fi

# ---- 1. no pnpm: skips, exit 0, and says all of it out loud on stderr --------
out="$(PATH="$NOPNPM_PATH" bash "$GATE" 2>&1)"
rc=$?
if (( rc != 0 )); then
  fail "without pnpm the gate exited $rc; a bare Go checkout must still be able to run \`make check-backend\`"
fi
for phrase in "SKIPPED" "pnpm" "NOT checked" "schema.d.ts"; do
  case "$out" in
    *"$phrase"*) ;;
    *) fail "the skip message does not mention '$phrase' — a skip nobody can read is a skip nobody notices:
$out" ;;
  esac
done

stdout_only="$(PATH="$NOPNPM_PATH" bash "$GATE" 2>/dev/null)"
if [[ -n "$stdout_only" ]]; then
  fail "the skip printed to stdout ('$stdout_only'); it must go to stderr so a quiet caller cannot swallow it"
fi

# ---- 2. pnpm's absence is the ONLY early success ----------------------------
# Read from the gate itself. Comments are stripped first and the match is by
# SUBSTRING, because the mutation that escaped the first version of this check
# was `[[ -d node_modules ]] || exit 0` — the shape a well-meaning "be tolerant
# of an uninstalled tree" edit takes — and an anchored whole-line pattern read it
# as absent. `exit` with no status and `exit $?` are counted too: both exit 0
# when the preceding command succeeded.
# Only WHOLE-LINE comments are dropped. A blanket `s/#.*$//` also eats the `#`
# in `${#ARTIFACTS[@]}`, which silently removed the census line this file then
# reported as missing — a stripper that damages code is a reader that cannot be
# trusted about what the code does.
stripped="$(sed '/^[[:space:]]*#/d' "$GATE")"

# The pnpm-absent branch is located by its own two anchors, and the range is
# required to CLOSE. An unterminated range would swallow the rest of the file and
# report zero early exits — a census that passes by seeing nothing, which is the
# defect this whole gate is about.
branch_lines="$(printf '%s\n' "$stripped" | awk '/^if ! command -v pnpm/,/^fi$/ {n++} END {print n+0}')"
total_lines="$(printf '%s\n' "$stripped" | wc -l | tr -d ' ')"
if (( branch_lines == 0 )); then
  fail "could not find the pnpm-absent branch in $GATE (its \`if ! command -v pnpm\` … \`fi\` anchors); this check is reading something else"
elif (( branch_lines >= total_lines / 2 )); then
  fail "the pnpm-absent branch spans $branch_lines of $total_lines lines in $GATE — the range did not close, so the early-exit census below would exempt most of the file"
fi

early_exits="$(printf '%s\n' "$stripped" \
  | awk '/^if ! command -v pnpm/,/^fi$/ {next} /(^|[^_[:alnum:]])exit([[:space:]]+(0|\$\?))?[[:space:]]*($|;|&|\|)/ {n++} END {print n+0}')"
if [[ "$early_exits" != "0" ]]; then
  fail "$GATE has $early_exits success-exit(s) outside the pnpm-absent branch — every one of them is a path that reports green without running the diff"
fi

# ---- 3. the census precedes every exit --------------------------------------
# An empty artifact list must fail, and it must fail on the skip path too — that
# is the path a bare checkout takes, where nobody is watching.
census_line="$(printf '%s\n' "$stripped" | grep -n 'ARTIFACTS\[@\]} -eq 0' | head -1 | cut -d: -f1)"
skip_line="$(printf '%s\n' "$stripped" | grep -n '^if ! command -v pnpm' | head -1 | cut -d: -f1)"
if [[ -z "$census_line" ]]; then
  fail "$GATE no longer refuses an empty artifact list; an emptied list would report success having compared nothing"
elif [[ -z "$skip_line" ]]; then
  fail "$GATE no longer has a pnpm-absent branch, so this test cannot order it against the census"
elif (( census_line > skip_line )); then
  fail "$GATE checks the artifact list at line $census_line, AFTER the skip at line $skip_line — on a bare checkout an empty list would print '0 artifact(s) NOT checked' and exit 0"
fi

if (( FAILURES > 0 )); then
  echo "check-contract-frontend-drift.test: $FAILURES failure(s)" >&2
  exit 1
fi
echo "check-contract-frontend-drift.test: OK — the skip is loud, stderr-bound, the only early exit, and the census precedes it"
