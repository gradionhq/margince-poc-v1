#!/usr/bin/env bash
# The frontend-contract-drift gate's own test — it gates the gate's SKIP PATH,
# which is the half that can quietly stop working.
#
# check-contract-frontend-drift.sh has to be skippable, because `make
# check-backend` must run on a bare Go checkout with no Node toolchain. That is
# also the whole risk: Wave 1's #1637 was a pathspec that matched nothing and
# reported PASS, and a pnpm-absent skip that nobody notices is the same failure
# wearing a different coat. So the three rules that hold the skip shut are
# asserted here rather than trusted:
#
#   1. Without pnpm, it skips — and says so LOUDLY, naming the leg, the reason
#      and the artifacts it did not check.
#   2. Without pnpm IN CI, it does not skip at all: it fails. CI has the frontend
#      toolchain, so a missing pnpm there means the toolchain moved, and the day
#      that happens the gate must report it instead of quietly stopping work.
#   3. The skip's only trigger is pnpm's absence. A gate that skips for any other
#      reason — an empty artifact list, a missing directory — is a gate that can
#      report success having compared nothing.
#
# Usage: bash scripts/check-contract-frontend-drift.test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE="$SCRIPT_DIR/check-contract-frontend-drift.sh"
FAILURES=0

fail() { echo "FAIL: $*" >&2; FAILURES=$((FAILURES + 1)); }

# A PATH with no pnpm on it, but with the ordinary tools the gate uses, so the
# absence under test is pnpm's alone and not the shell's.
TMPBIN="$(mktemp -d)"
trap 'rm -rf "$TMPBIN"' EXIT
for tool in bash git find mktemp touch dirname cd; do
  p="$(command -v "$tool" 2>/dev/null)" && ln -sf "$p" "$TMPBIN/$tool" 2>/dev/null
done
NOPNPM_PATH="$TMPBIN:/usr/bin:/bin:/usr/sbin:/sbin"
if PATH="$NOPNPM_PATH" command -v pnpm >/dev/null 2>&1; then
  echo "FAIL: this test's pnpm-free PATH still resolves pnpm, so cases 1 and 2 would" >&2
  echo "      assert nothing. Point NOPNPM_PATH somewhere pnpm is genuinely absent." >&2
  exit 1
fi

# ---- 1. no pnpm, not CI: skips, exit 0, and says all of it out loud ----------
out="$(env -u CI -u GITHUB_ACTIONS PATH="$NOPNPM_PATH" bash "$GATE" 2>&1)"
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

# The skip must be on stderr. A message on stdout is swallowed by the `@`-quiet
# recipe and by every caller that pipes the gate's output somewhere.
stdout_only="$(env -u CI -u GITHUB_ACTIONS PATH="$NOPNPM_PATH" bash "$GATE" 2>/dev/null)"
if [[ -n "$stdout_only" ]]; then
  fail "the skip printed to stdout ('$stdout_only'); it must go to stderr so a quiet caller cannot swallow it"
fi

# ---- 2. no pnpm, IN CI: refuses ---------------------------------------------
for var in CI GITHUB_ACTIONS; do
  out="$(env -u CI -u GITHUB_ACTIONS "$var=true" PATH="$NOPNPM_PATH" bash "$GATE" 2>&1)"
  rc=$?
  if (( rc == 0 )); then
    fail "with $var set and pnpm absent the gate exited 0 — CI would silently stop checking the frontend contract types, which is the exact failure this gate exists to prevent:
$out"
  fi
  case "$out" in
    *SKIPPED*) fail "with $var set the gate still reported a SKIP; in CI a missing pnpm is a broken toolchain, not an exemption" ;;
  esac
done

# ---- 3. pnpm's absence is the ONLY skip trigger ------------------------------
# Read from the gate itself: a second `exit 0` reachable before the diff is a
# second way to report success having compared nothing.
# Comments are stripped first, and the match is a SUBSTRING rather than a whole
# line: the mutation that got past the first version of this check was
# `[[ -d node_modules ]] || exit 0`, which is exactly the shape a well-meaning
# "be tolerant of an uninstalled tree" edit takes, and an anchored pattern read
# it as absent.
early_exits="$(sed 's/#.*$//' "$GATE" \
  | awk '/^if ! command -v pnpm/,/^fi$/ {next} /exit 0/ {n++} END {print n+0}')"
if [[ "$early_exits" != "0" ]]; then
  fail "$GATE has $early_exits \`exit 0\` outside the pnpm-absent branch — every one of them is a path that reports success without running the diff"
fi

if (( FAILURES > 0 )); then
  echo "check-contract-frontend-drift.test: $FAILURES failure(s)" >&2
  exit 1
fi
echo "check-contract-frontend-drift.test: OK — the skip is loud, stderr-bound, CI-proof, and the only one"
