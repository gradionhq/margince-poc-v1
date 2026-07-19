#!/usr/bin/env bash
# Contract-drift gate, breaking-change half.
# Severity-classifies every change to backend/api/crm.yaml since the
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
  stable | pre-live) ;;
  *) echo "check-contract-breaking: unknown CONTRACT_STABILITY='$CONTRACT_STABILITY' (want stable|pre-live)" >&2; exit 2 ;;
esac

# A ratified pre-live resync can be recorded as one exact old/new contract
# blob pair. The exception cannot mask the next contract edit: either blob
# changing returns the gate to stable automatically.
ALLOWLIST="${CONTRACT_BREAKING_ALLOWLIST:-scripts/contract-breaking-allowlist.txt}"

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

ALLOWLISTED_RESYNC=0
if [ "$CONTRACT_STABILITY" = stable ] && [ -f "$ALLOWLIST" ]; then
  BASE_BLOB="$(git rev-parse "$BASE_REF:$CRM_YAML")"
  CURRENT_BLOB="$(git hash-object "$CRM_YAML")"
  while read -r old_blob new_blob reason; do
    case "$old_blob" in ''|'#'*) continue ;; esac
    if [ "$old_blob" = "$BASE_BLOB" ] && [ "$new_blob" = "$CURRENT_BLOB" ] && [ -n "${reason:-}" ]; then
      ALLOWLISTED_RESYNC=1
      break
    fi
  done < "$ALLOWLIST"
fi

if [ "$CONTRACT_STABILITY" = pre-live ] || [ "$ALLOWLISTED_RESYNC" = 1 ]; then
  # Advisory stance: print every change (oasdiff without --fail-on never
  # exits non-zero on findings) so the deliberate breaks stay visible in
  # the log, but do not block.
  $OASDIFF breaking "$BASE_REF:$CRM_YAML" "$CRM_YAML" -f text
  if [ "$ALLOWLISTED_RESYNC" = 1 ]; then
    echo "contract-breaking-check: advisory — this exact pre-live contract resync is ratified; any later contract edit restores the stable gate"
  else
    echo "contract-breaking-check: advisory (CONTRACT_STABILITY=pre-live) — breaking change(s) printed above, if any, are deliberately permitted for this run"
  fi
elif $OASDIFF breaking "$BASE_REF:$CRM_YAML" "$CRM_YAML" --fail-on ERR -f text; then
  echo "contract-breaking-check: no breaking API changes since $BASE_REF"
else
  echo "contract-breaking-check: breaking API change(s) since $BASE_REF — deprecate instead of removing, or run a deliberate re-sync with CONTRACT_STABILITY=pre-live" >&2
  exit 1
fi
