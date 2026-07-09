#!/usr/bin/env bash
# Contract-drift gate, breaking-change half (ported from the foundation
# skeleton). Severity-classifies every change to backend/api/crm.yaml since the
# base ref and fails on ERR-level (breaking) changes; WARN/INFO-level changes
# (additive, deprecation) pass.
#
# Contract-first cuts both ways: the whole-file drift gate (`make drift`)
# proves the generated Go matches the contract, but nothing proved the
# contract itself stayed compatible. This gate does. A deliberate breaking
# re-sync from the spec repo runs with CONTRACT_STABILITY=pre-live, which
# still prints every breaking change but does not block.
#
# Usage: check-contract-breaking.sh [base-ref]   (default: origin/main)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

CRM_YAML="${CRM_YAML:-backend/api/crm.yaml}"
BASE_REF="${1:-origin/main}"

# The pinned tool invocation. `go run` at an exact version keeps the gate
# reproducible without a PATH install; `make tools` also installs the same
# version as a binary for direct use — bump both pins together.
OASDIFF="${OASDIFF:-go run github.com/oasdiff/oasdiff@v1.22.0}"

# Stance: 'stable' (default) blocks the merge on any breaking change;
# 'pre-live' prints them but passes (for a deliberate spec re-sync).
CONTRACT_STABILITY="${CONTRACT_STABILITY:-stable}"
case "$CONTRACT_STABILITY" in
  stable)   FAIL_ON="ERR" ;;
  pre-live) FAIL_ON="NONE" ;;
  *) echo "check-contract-breaking: unknown CONTRACT_STABILITY='$CONTRACT_STABILITY' (want stable|pre-live)" >&2; exit 2 ;;
esac

if [ ! -f "$CRM_YAML" ]; then
  echo "check-contract-breaking: contract not found at $CRM_YAML" >&2
  exit 2
fi
if ! git rev-parse --verify -q "$BASE_REF" >/dev/null; then
  # A shallow clone has no base to diff against. CI sets REQUIRE_BASE so a
  # dropped fetch-depth surfaces as a red gate instead of a silent skip.
  if [ "${CONTRACT_BREAKING_REQUIRE_BASE:-}" = "1" ]; then
    echo "check-contract-breaking: base ref '$BASE_REF' not found and CONTRACT_BREAKING_REQUIRE_BASE=1 — fetch the base ref (checkout fetch-depth)" >&2
    exit 1
  fi
  echo "skip contract-breaking-check: base ref '$BASE_REF' not found (nothing to diff against)"
  exit 0
fi
if ! git cat-file -e "$BASE_REF:$CRM_YAML" 2>/dev/null; then
  echo "skip contract-breaking-check: contract did not exist at $BASE_REF (nothing to diff)"
  exit 0
fi

if $OASDIFF breaking "$BASE_REF:$CRM_YAML" "$CRM_YAML" --fail-on "$FAIL_ON" -f text; then
  if [ "$CONTRACT_STABILITY" = pre-live ]; then
    echo "contract-breaking-check: advisory (CONTRACT_STABILITY=pre-live) — breaking change(s) printed above, if any, are deliberately permitted for this run"
  else
    echo "contract-breaking-check: no breaking API changes since $BASE_REF"
  fi
else
  echo "contract-breaking-check: breaking API change(s) since $BASE_REF — deprecate instead of removing, or run a deliberate re-sync with CONTRACT_STABILITY=pre-live" >&2
  exit 1
fi
