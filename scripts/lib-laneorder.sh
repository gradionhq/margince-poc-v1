#!/usr/bin/env bash
# Shared helper for the parallel integration lane: decide which packages the
# dispatcher starts first. Source this; don't execute it.
#
# Wall clock is set by when the LONGEST package finishes, so it should start at
# t=0. Dispatched in discovery order it does not: measured, this lane's long
# pole waited 16s of a 252s run for a slot while shorter packages held all
# eight. That is the classic longest-processing-time-first result.
#
# The durations are read as an ORDER, never as a prediction. They are wall-clock
# seconds from one machine under one load, so the numbers themselves carry
# little across hosts; what survives the move is which package is the long pole.
#
# Lives in its own file so `scripts/test-laneorder.sh` can exercise the two
# properties that decide whether the ordering helps or quietly hurts — a named
# package sorts by its duration, and an unnamed one keeps the order it would
# have had with no hint at all — without standing up a database.

# resolve_order_hint MEASURED BASELINE — print the hint file to dispatch by, or
# nothing when neither is usable.
#
# MEASURED is what the last full run on THIS machine recorded and wins whenever
# it exists: a machine's own durations beat anybody else's. BASELINE is the
# committed fallback, so a fresh clone — which has no .tmp/ at all — still
# dispatches longest-first on its very first lane. The committed file is never
# written by a run; see the publish step in test-integration-parallel.sh for why
# a shard's slice measurements must not become anyone's baseline.
resolve_order_hint() {
  local measured="$1" baseline="$2"
  if [[ -s "$measured" ]]; then
    printf '%s' "$measured"
  elif [[ -s "$baseline" ]]; then
    printf '%s' "$baseline"
  fi
}

# order_by_hint GROUPED HINT — print GROUPED's lines longest-package-first.
#
# GROUPED lines are `moduledir|package|run-regex`; HINT lines are
# `package|seconds`. Only fields 1 and 2 of each are read, and the GROUPED line
# is reprinted whole, because its third field is a -run regex containing its own
# pipes.
#
# A HINT line naming something that is not a package in GROUPED is simply never
# looked up, which is what makes the committed file's human-readable header
# harmless: it becomes an entry keyed on its own text that nothing asks for.
#
# A package with no USABLE time sorts FIRST, not last. The unknown is usually a
# new small package, where the cost of guessing wrong is one slot occupied early;
# guessing wrong the other way puts a new heavy package last and pays its whole
# duration after everything else has finished.
#
# "Usable" and not merely "present", because awk reads a non-numeric field as
# ZERO — which sorts that package LAST, the exact schedule this ordering exists
# to avoid, and it is reachable: the lane's own report has a branch for a package
# that "recorded no test duration", and such a line reaches the hint as
# `package|` with an empty second field. A duration that is not a positive number
# is therefore treated as no duration at all.
#
# -s (stable) is what makes an unnamed package degrade to the order it would
# have had with no hint at all. Every unnamed package shares one sort key, and
# without -s sort breaks those ties on the whole line — under -r, in REVERSE
# input order. A stale baseline must hand the packages it does not name back to
# the order they arrived in, never to an order the sort invented from them.
order_by_hint() {
  local grouped="$1" hint="$2"
  awk -F'|' -v hint="$hint" '
    BEGIN {
      while ((getline l < hint) > 0) {
        split(l, f, "|")
        if (f[2] ~ /^[0-9]+(\.[0-9]+)?$/ && f[2] + 0 > 0) secs[f[1]] = f[2] + 0
      }
    }
    { printf "%012.3f|%s\n", ($2 in secs ? secs[$2] : 999999), $0 }
  ' "$grouped" | LC_ALL=C sort -s -t'|' -k1,1 -rn | cut -d'|' -f2-
}
