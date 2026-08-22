#!/usr/bin/env bash
# Prove check-one-spelling.sh fires on each defect it names, stays silent on
# the lookalikes that are not defects, and honours its own waiver.
#
# A gate nobody has watched fail is a gate nobody knows the shape of. Each case
# below plants ONE instance in a throwaway file inside the scanned tree, runs
# the real script, and asserts on its verdict — so a regex edit that silently
# stops matching, or starts matching a bystander, fails here rather than in
# somebody's push six weeks later.
set -uo pipefail
cd "$(dirname "$0")/.."

GATE=./scripts/check-one-spelling.sh

# The probes live OUTSIDE the repository, and the gate is pointed at them with
# ONE_SPELLING_SCAN. Planting a deliberate defect in the real tree would make
# `make -j` a race: a concurrent one-spelling would report the probe, and
# gofmt, the license gate and craft would each see an unlicensed stray file.
PROBE_DIR="$(mktemp -d)"
PLANT="$PROBE_DIR/zz_one_spelling_probe.go"
trap 'rm -rf "$PROBE_DIR"' EXIT
fails=0

# plant <body> — write a compiling-shaped probe file carrying <body>.
plant() { printf '// SPDX-License-Identifier: BUSL-1.1\npackage storekit\n\n%s\n' "$1" > "$PLANT"; }

# expect <fires|silent> <name> <body>
expect() {
  local want="$1" name="$2" body="$3" out rc
  plant "$body"
  out="$(ONE_SPELLING_SCAN="$PROBE_DIR" $GATE 2>&1)"; rc=$?
  rm -f "$PLANT"
  if [[ "$want" == fires && $rc -eq 0 ]]; then
    echo "FAIL: $name — the gate passed over it"; echo "$out" | sed 's/^/    /'; fails=1; return
  fi
  if [[ "$want" == silent && $rc -ne 0 ]]; then
    echo "FAIL: $name — the gate refused it"; echo "$out" | sed 's/^/    /'; fails=1; return
  fi
  echo "ok: $name"
}

echo "== the tree as it stands =="
if ! $GATE >/dev/null 2>&1; then
  echo "FAIL: the gate does not pass on an unmodified tree — every case below is unreadable"
  $GATE 2>&1 | sed 's/^/    /'
  exit 1
fi
echo "ok: clean tree passes"

echo
echo "== each arm fires on its own defect =="
expect fires "SQLSTATE literal"        'func probe(c string) bool { return c == "23505" }'
expect fires "CHECK-to-422 re-spelling" 'const probeCode = "constraint_violated"'
expect fires "private ISO-4217 regexp"  'const probeShape = `^[A-Z]{3}$`'

echo
echo "== a SQLSTATE the gate was never told about, read from sqlstate.go =="
# 22001 is NOT in sqlstate.go, so it must NOT fire — the list is derived, and a
# hand-typed list would have had to guess which codes matter.
expect silent "an unrelated SQLSTATE-shaped literal" 'const probeLen = "22001"'

echo
echo "== the waiver, and what must stay silent without one =="
expect silent "a waived line" \
  'func probe(c string) bool { return c == "23505" } // one-spelling-exempt: probing the gate'

# The exception is the attack surface. A waiver must silence the LINE it is on
# and nothing else — a waiver that quiets the file would let the next defect in
# free, under a reason written about something else entirely.
expect fires "a waiver silences its own line only" \
  'func waived(c string) bool { return c == "23505" } // one-spelling-exempt: probing the gate
func notWaived(c string) bool { return c == "23503" }'
expect silent "the same token in a line comment" \
  '// A dedupe hit is "23505", named in sqlstate.go.'
expect silent "the same token in a block comment" \
  '/*
A dedupe hit is "23505" and the wire code was "constraint_violated".
*/'
expect silent "the same token in an inline block comment" \
  'const probeN = 1 /* not "23505" */'

echo
if [[ $fails -eq 1 ]]; then
  echo "FAIL: check-one-spelling.sh does not behave as its header claims"
  exit 1
fi
echo "OK: check-one-spelling.sh fires on each defect, stays silent on each lookalike"
