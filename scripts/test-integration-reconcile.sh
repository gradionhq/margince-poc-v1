#!/usr/bin/env bash
# Fan-in for the sharded CI integration lane.
#
# Each `integration shard (k/N)` matrix job runs a deterministic slice of the
# integration tests (scripts/test-integration-parallel.sh, INTEGRATION_SHARD)
# and uploads its manifests + binary coverage pods as an artifact. A green
# shard only proves ITS slice ran — this script proves the lane as a whole:
# every shard reported, every shard sliced the same discovery, and the slices
# add up to exactly that discovery (complete + disjoint). Without this check a
# slicing bug — a lost shard artifact, a matrix/total mismatch, divergent
# discoveries — would quietly run fewer tests everywhere while every job stays
# green, which for this lane means RLS/erasure gates silently not running.
#
# Layout under <artifacts-root> (the actions/download-artifact target dir):
#   integration-shard-<k>/     discovery.txt assigned.txt ran.txt meta.txt
#                              covdata/<slot>/ (binary pods)
#   integration-covdata-unit/  binary pods from the unit `-cover` pass
#
# Usage: test-integration-reconcile.sh <artifacts-root> [cover-out]
#   cover-out: merge every binary pod (shards + unit) into this Go text
#   profile — the single file sonar-project.properties points SonarCloud at.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ARTIFACTS="${1:?usage: test-integration-reconcile.sh <artifacts-root> [cover-out]}"
COVER_OUT="${2:-}"
case "$ARTIFACTS" in /*) ;; *) ARTIFACTS="$ROOT_DIR/$ARTIFACTS";; esac
[[ -z "$COVER_OUT" ]] || case "$COVER_OUT" in /*) ;; *) COVER_OUT="$ROOT_DIR/$COVER_OUT";; esac

fail() { echo "FAIL: $*"; exit 1; }

# The expected shard count comes from the shards' own meta (INTEGRATION_SHARD's
# N, i.e. the CI matrix size) — no second copy of the number to drift. Any
# present shard works as the source; 1..N must then all be present and agree.
first_meta="$(find "$ARTIFACTS" -mindepth 2 -maxdepth 2 -path '*/integration-shard-*/meta.txt' | head -n1)"
[[ -n "$first_meta" ]] || fail "no integration-shard-*/meta.txt under $ARTIFACTS — no shard artifact was downloaded"
TOTAL="$(sed -n 's/^total=//p' "$first_meta")"
[[ "$TOTAL" =~ ^[0-9]+$ && "$TOTAL" -ge 1 ]] || fail "unparsable total= in $first_meta"

REF_DISCOVERY="$ARTIFACTS/integration-shard-1/discovery.txt"
UNION="$(mktemp)"
trap 'rm -f "$UNION"' EXIT

for k in $(seq 1 "$TOTAL"); do
  d="$ARTIFACTS/integration-shard-$k"
  for f in meta.txt discovery.txt assigned.txt ran.txt; do
    [[ -f "$d/$f" ]] || fail "shard $k/$TOTAL is missing $f — its job reported green without a complete artifact"
  done
  grep -qx "shard=$k" "$d/meta.txt" || fail "shard $k artifact carries $(grep '^shard=' "$d/meta.txt") — artifact/matrix mix-up"
  grep -qx "total=$TOTAL" "$d/meta.txt" || fail "shard $k ran with $(grep '^total=' "$d/meta.txt"), expected total=$TOTAL — the matrix and INTEGRATION_SHARD disagree"
  cmp -s "$REF_DISCOVERY" "$d/discovery.txt" || fail "shard $k discovered a different test set than shard 1 — shards did not run the same code"
  diff "$d/assigned.txt" "$d/ran.txt" > /dev/null || fail "shard $k ran a different test set than assigned (its own gate should have caught this)"
  cat "$d/assigned.txt" >> "$UNION"
done

# Complete + disjoint in one comparison: the concatenated slices, sorted, must
# be exactly the discovery (a gap shrinks the union, an overlap grows it).
LC_ALL=C sort -o "$UNION" "$UNION"
if ! diff "$REF_DISCOVERY" "$UNION" > /dev/null; then
  echo "FAIL: the $TOTAL shard slices do not add up to the discovered test set:"
  diff "$REF_DISCOVERY" "$UNION" | sed -n 's/^< /  discovered but in no slice: /p; s/^> /  extra slice entry (duplicate or not discovered): /p' || true
  exit 1
fi
NTESTS="$(wc -l < "$REF_DISCOVERY" | tr -d ' ')"

if [[ -n "$COVER_OUT" ]]; then
  unit_dir="$ARTIFACTS/integration-covdata-unit"
  [[ -d "$unit_dir" ]] || fail "unit coverage pods missing ($unit_dir) — SonarCloud would see unit-only packages at a false 0%"
  pods="$(find "$ARTIFACTS" -name 'covmeta.*' -exec dirname {} \; | LC_ALL=C sort -u)"
  printf '%s\n' "$pods" | grep -qxF "$unit_dir" || fail "no covmeta pods in $unit_dir — the unit pass produced no coverage"
  dirs="$(printf '%s\n' "$pods" | paste -sd, -)"
  ( cd backend && go tool covdata textfmt -i="$dirs" -o="$COVER_OUT" )
  echo "coverage: merged $(printf '%s\n' "$pods" | grep -c .) pods (shards + unit) → $COVER_OUT"
fi

echo "OK: integration reconciled — $TOTAL shards cover all $NTESTS tests, complete and disjoint"
