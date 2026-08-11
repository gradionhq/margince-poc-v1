#!/usr/bin/env bash
# check-ci-doc-parity.sh — every path a workflow filters on is named in the prose
# that documents it.
#
# The same path lists live in two places: the workflow, which decides what runs,
# and the document a reader consults to decide where a change belongs. Nothing
# held them together, and both directions of drift have already happened here — a
# path added to a filter and left out of the table, and a whole passage describing
# a syft exclude list that had been deleted. The second is the reason this exists:
# a stale document does not merely fail to help, it argues confidently for the
# wrong thing, and a real gate finding read as impossible for exactly as long as
# someone believed it.
#
# WHAT THIS CATCHES: a path present in a workflow filter and absent from its
# document. That is the direction with teeth — an undocumented scope sends the
# next reader to the wrong place.
#
# WHAT THIS DOES NOT CATCH, deliberately: the reverse, a path removed from a
# filter while the document still lists it. Checking that means teaching this
# script every shorthand the prose is allowed to use, and a gate whose rules are
# fiddly is a gate that gets worked around rather than fixed. A leftover entry
# misleads a reader; a missing one hides a gate that silently never runs. This
# guards the worse of the two, and says so rather than implying it guards both.
set -euo pipefail

fail=0

# quoted_paths <file> <anchor> — the single- or double-quoted list entries under
# <anchor>, stopping at the first non-blank line indented no deeper than the
# anchor itself. Comment lines inside the block carry no quoted entry and so fall
# out on their own.
quoted_paths() {
  local file="$1" anchor="$2"
  awk -v anchor="$anchor" '
    !started && index($0, anchor) { started = 1; base = match($0, /[^ ]/); next }
    started {
      if ($0 ~ /^[[:space:]]*$/) next
      if (match($0, /[^ ]/) <= base) exit
      if (match($0, /-[[:space:]]*["'"'"'][^"'"'"']+["'"'"']/)) {
        entry = substr($0, RSTART, RLENGTH)
        gsub(/^-[[:space:]]*["'"'"']|["'"'"']$/, "", entry)
        print entry
      }
    }
  ' "$file"
}

# check <workflow> <anchor> <doc> — assert the doc names every extracted path.
check() {
  local workflow="$1" anchor="$2" doc="$3" paths count=0 missing=0
  paths="$(quoted_paths "$workflow" "$anchor")"
  # An extraction that silently yields nothing would pass while checking nothing,
  # which is the failure this file is written against.
  count="$(printf '%s\n' "$paths" | grep -c . || true)"
  if [ "$count" -eq 0 ]; then
    echo "FAIL: extracted no paths from $workflow under '$anchor' — the anchor moved or the block changed shape, so this gate was about to pass without comparing anything" >&2
    fail=1
    return
  fi
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    # Matched as a backticked literal: the prose is free to add commentary around
    # an entry, but the entry itself has to appear exactly as the workflow spells
    # it, or it is not the same path.
    grep -qF -- "\`$path\`" "$doc" || {
      echo "FAIL: $workflow filters on '$path' but $doc never names it" >&2
      missing=1
    }
  done <<< "$paths"
  [ "$missing" -eq 0 ] || fail=1
  echo "checked $count path(s) from $workflow against $doc"
}

check .github/workflows/ci.yml "filters: |" infra/ci-pipeline.md
check .github/workflows/sbom.yml "    paths:" docs/reference/supply-chain.md

if [ "$fail" -eq 0 ]; then
  echo "OK: every filtered path is documented"
fi
exit "$fail"
