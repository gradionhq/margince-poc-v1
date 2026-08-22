#!/usr/bin/env bash
# Prove check-money-scale.sh fires in BOTH languages, honours its waiver, and
# does not read a comment as code.
#
# Probes live outside the repository and the gate is pointed at them, so a
# planted defect can never collide with a concurrent `make -j` job.
set -uo pipefail
cd "$(dirname "$0")/.."

GATE=./scripts/check-money-scale.sh
PROBE="$(mktemp -d)"
trap 'rm -rf "$PROBE"' EXIT
fails=0

# expect <fires|silent> <go|ts> <name> <body>
expect() {
  local want="$1" lang="$2" name="$3" body="$4" out rc file
  rm -f "$PROBE"/probe.*
  if [[ "$lang" == go ]]; then
    file="$PROBE/probe.go"
    printf '// SPDX-License-Identifier: BUSL-1.1\npackage probe\n\n%s\n' "$body" > "$file"
  else
    file="$PROBE/probe.ts"
    printf '%s\n' "$body" > "$file"
  fi
  out="$(MONEY_SCALE_GO_SCAN="$PROBE" MONEY_SCALE_TS_SCAN="$PROBE" $GATE 2>&1)"; rc=$?
  rm -f "$file"
  # A non-zero exit is not the same as a DETECTION. The gate also exits 1 when
  # a scan root is missing or the script itself errors, and a case that reads
  # only the status would score that as the defect being caught — a probe
  # passing for a reason that has nothing to do with what it plants.
  if [[ "$want" == fires ]] && ! grep -q "scaled by a hard-coded power of ten" <<< "$out"; then
    echo "FAIL: $name — the gate exited $rc without reporting a scale finding, so this proves nothing"
    echo "$out" | sed 's/^/    /'; fails=1; return
  fi
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
echo "== Go: the shape that shipped four times =="
expect fires go "a divide"   'func probe(amountMinor int64) int64 { return amountMinor / 100 }'
expect fires go "a multiply, wrapped by the formatter" 'func probe(major int64) int64 {
	amountMinor := int64(
		major * 100,
	)
	return amountMinor
}'
expect fires go "a remainder for the cents half" 'func probe(minor int64) int64 { return minor % 100 }'

# Every power the detector claims, not just the euro's. A three-decimal
# currency scales by 1000 and a one-decimal by 10, and a probe set that only
# ever plants 100 would not notice the detector losing either.
expect fires go "a KWD-style 1000"     'func probe(amountMinor int64) int64 { return amountMinor / 1000 }'
expect fires go "a one-digit 10"       'func probe(amountMinor int64) int64 { return amountMinor / 10 }'
expect fires ts "a KWD-style 1000"     'export const toWire = (amount_minor: number) => amount_minor / 1000;'
expect fires ts "a one-digit 10"       'export const toWire = (amount_minor: number) => amount_minor * 10;'
expect fires ts "a remainder for the fractional half" \
  'export const cents = (valueMinor: number) => valueMinor % 100;'

# The identifier may be a Go or SQL-style constant.
expect fires go "an upper-case identifier" 'const AMOUNT_MINOR_SCALE = 1
func probe(v int64) int64 { return AMOUNT_MINOR_SCALE * v / 100 }'

echo
echo "== TypeScript: the write direction the Go-only gate could not see =="
expect fires ts "a multiply on the write path, wrapped" 'export const toWire = (amount: string) => ({
  amount_minor: Math.round(Number(amount) * 100),
});'
expect fires ts "a divide on the read path" 'export const seed = (valueMinor: number) => valueMinor / 100;'

echo
echo "== a lookalike that is not money =="
# The anchor is the identifier, so arithmetic on 100 with no minor amount near
# it is untouched — otherwise every percentage in the tree would be a finding.
expect silent go "a percentage"     'func probe(part, whole int64) int64 { return part * 100 / whole }'
expect silent ts "a progress width" 'export const pct = (done: number, total: number) => (done / total) * 100;'

echo
echo "== the waiver, and comments =="
expect silent go "a waived line" \
  'func probe(amountMinor int64) int64 { return amountMinor / 100 } // money-scale-exempt: probing the gate'
expect fires go "a waiver silences its own line only" \
  'func waived(amountMinor int64) int64 { return amountMinor / 100 } // money-scale-exempt: probing the gate
func notWaived(valueMinor int64) int64 { return valueMinor / 100 }'
expect silent go "the shape described in a line comment" \
  '// The old spelling was amountMinor / 100, which VND does not survive.'
expect silent go "the shape described in a block comment" \
  '/*
The old spelling was amountMinor / 100.
*/'
expect silent ts "the shape described in a TS comment" \
  '// Not `(valueMinor / 100).toFixed(2)` — the scale is the currency'"'"'s.'

echo
if [[ $fails -eq 1 ]]; then
  echo "FAIL: check-money-scale.sh does not behave as its header claims"
  exit 1
fi
echo "OK: check-money-scale.sh fires in both languages, and only on money"
