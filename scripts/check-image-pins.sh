#!/usr/bin/env bash
# check-image-pins.sh — fail unless every .github/workflows/*.yml|*.yaml
# `uses:` line is pinned to an immutable ref (supply-chain: a symbolic ref
# lets a compromised action ride into CI unreviewed).
# Allows:  @<40-char-hex>       (git commit SHA, e.g. actions/checkout@11bd71...)
#          @<64-char-hex>       (SHA-256 object id)
#          @sha256:<hex>        (image digest)
#          ./path               (local composite action — pinned by this repo)
# Rejects: everything else — tags (@v4, @v1.2.3-beta), branches (@main,
#          @develop), and an unpinned `uses:` with no @ at all. An allowlist,
#          not a denylist: a ref shape we didn't anticipate fails closed.
set -euo pipefail

fail=0
workflow_dir=".github/workflows"

if [ ! -d "$workflow_dir" ]; then
  echo "No $workflow_dir directory found — skipping image-pin check"
  exit 0
fi

while IFS= read -r line; do
  # Extract the part after `uses:` to check the pin
  pin=$(echo "$line" | sed 's/.*uses:[[:space:]]*//' | cut -d'#' -f1 | tr -d ' "'"'")
  case "$pin" in
    ./*) continue ;; # local composite action: versioned with the repo itself
  esac
  ref="${pin##*@}"
  if [ "$ref" = "$pin" ] || ! echo "$ref" | grep -qE '^([0-9a-f]{40}|[0-9a-f]{64}|sha256:[0-9a-f]{64})$'; then
    echo "UNPINNED REF: $line" >&2
    fail=1
  fi
done < <(grep -rn 'uses:' "$workflow_dir"/*.yml "$workflow_dir"/*.yaml 2>/dev/null || true)

if [ "$fail" -eq 0 ]; then
  echo "image pins OK"
fi
exit "$fail"
