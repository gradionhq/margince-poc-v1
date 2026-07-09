#!/usr/bin/env bash
# check-image-pins.sh — fail if any .github/workflows/*.yml uses: line has a floating tag.
# Allows:  @sha256:<hex>        (digest-only)
#          @<40-char-hex>       (commit SHA, e.g. actions/checkout@11bd71...)
# Rejects: @vN, @vN.M, @vN.M.P (semver without digest)
#          @main, @master, @latest, @HEAD (branch/symbolic refs)
set -euo pipefail

fail=0
workflow_dir=".github/workflows"

if [ ! -d "$workflow_dir" ]; then
  echo "No $workflow_dir directory found — skipping image-pin check"
  exit 0
fi

while IFS= read -r line; do
  # Extract the part after `uses:` to check the pin
  pin=$(echo "$line" | sed 's/.*uses:[[:space:]]*//' | cut -d'#' -f1 | tr -d ' ')
  # Reject floating tags: @vN, @vN.M, @vN.M.P, @main, @master, @latest, @HEAD
  if echo "$pin" | grep -qE '@(v[0-9]+(\.[0-9]+)*|main|master|latest|HEAD)$'; then
    echo "FLOATING TAG: $line" >&2
    fail=1
  fi
done < <(grep -rn 'uses:' "$workflow_dir"/*.yml 2>/dev/null || true)

if [ "$fail" -eq 0 ]; then
  echo "image pins OK"
fi
exit "$fail"
