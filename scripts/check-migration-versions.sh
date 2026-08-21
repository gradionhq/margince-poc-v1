#!/usr/bin/env bash
# Migration-version gate: a migration this branch adds claims a version no
# migration on the base ref has already claimed, and sorts after all of them.
#
# WHY A TREE-LOCAL TEST CANNOT CATCH THIS. `TestEmbeddedMigrationNamespacesLoad`
# fails on a duplicate version, and it passed on both PRs that produced this
# outage: each numbered its migration against a `main` that did not yet carry
# the other, so each tree was individually valid and the COLLISION only existed
# in the merge. `main` then could not load its own sequence
# (`pgmigrate: duplicate version 0240`) — which is not a partially-migrated
# database but no migration at all, because the loader rejects the whole
# namespace before applying anything. It happened twice in two days (0240, then
# 0248), so it is a property of numbering against a moving base, not bad luck.
#
# It also stayed invisible: `main`'s own CI reports green whenever its head
# commit is docs-only, because the change classifier skips the backend lane for
# it. The collision surfaced on an unrelated frontend PR whose `live-boot` boots
# the stack.
#
# WHY "AFTER", NOT MERELY "NOT EQUAL". A version BELOW the base's highest is not
# a collision, and is worse than one, but not because it is skipped: pgmigrate
# tests membership (`done[version]`), not a watermark, so a lower version IS
# applied. It is applied in the WRONG PLACE. On a fresh database it runs before
# everything above it; on a database already past that number it runs after,
# since those are recorded and skipped. The same set of migrations then produces
# two schemas whenever anything in it is order-dependent — a later ALTER of the
# table this one creates, a CHECK this one widens, a backfill reading a column
# added above it — and nothing reports the difference. `down` diverges too: it
# reverts by VERSION order, so on the second database `migrate down --steps 1`
# reverts the highest version, which is not the migration last applied. The fix
# in both cases is the same and is cheap: re-stamp above the base and rebase.
#
# WHY THE GATE OUTLIVED THE NUMBERING IT WAS BUILT FOR. core/ now names a
# migration for the unix second it was written, so two branches no longer pick
# one version — but stamping the clock only removes the COLLISION, never the
# ordering obligation above, which is a property of what a database already
# applied and not of how the name was chosen. A branch that sat while another
# migration merged still sorts below the base and still has to re-stamp.
#
# The namespace list is derived from the tree (backend/migrations/*/), not
# hand-maintained, so a third namespace is gated the day it appears. Every
# shape in the tree works: custom's YYYYMMDDHHMMSS stamp, core's unix seconds,
# and core's zero-padded baseline version — which the ten-digit stamps sort above
# — all compare as strings, the same ordering pgmigrate itself applies.
#
# Usage: check-migration-versions.sh [base-ref]   (default: origin/main)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BASE_REF="${1:-origin/main}"
MIGRATIONS_DIR="backend/migrations"

# A migration is identified by its VERSION and named by its file, and the gate
# needs both: the outage was two DIFFERENT migrations claiming one version, which
# a list of versions alone cannot show. Each helper below emits
# "<version> <name>" per migration, keyed on `.up.sql` so the down file does not
# double every row.

# migrations_in_tree NAMESPACE — what the working tree declares.
migrations_in_tree() {
  local ns="$1"
  find "$MIGRATIONS_DIR/$ns" -maxdepth 1 -name '*.up.sql' -exec basename {} .up.sql \; 2>/dev/null |
    sed 's/^\([^_]*\)_\(.*\)$/\1 \2/' | sort
}

# migrations_at_base NAMESPACE — the same, read out of the base ref rather than
# the worktree. `git ls-tree` and not `git diff`: what matters is every version
# the base ALREADY carries, including the ones this branch never touched.
migrations_at_base() {
  local ns="$1"
  git ls-tree --name-only "$BASE_REF" "$MIGRATIONS_DIR/$ns/" 2>/dev/null |
    grep '\.up\.sql$' | xargs -n1 basename 2>/dev/null | sed 's/\.up\.sql$//' |
    sed 's/^\([^_]*\)_\(.*\)$/\1 \2/' | sort
}

# A shallow clone has no base to compare against. CI sets REQUIRE_BASE so a
# broken checkout fails loudly there instead of quietly disarming the gate,
# which is the contract-breaking gate's convention and the same hazard.
if ! git rev-parse --verify -q "$BASE_REF" >/dev/null; then
  if [ "${MIGRATION_VERSIONS_REQUIRE_BASE:-}" = "1" ]; then
    echo "check-migration-versions: base ref '$BASE_REF' not found and MIGRATION_VERSIONS_REQUIRE_BASE=1 — fetch the base ref (checkout fetch-depth: 0)" >&2
    exit 1
  fi
  echo "skip check-migration-versions: base ref '$BASE_REF' not found (nothing to compare against)"
  exit 0
fi

failed=0
checked=0

# A BASELINE CONSOLIDATION is the one change this gate cannot judge on version
# numbers alone.
#
# Squashing a namespace into a baseline reuses its lowest versions under new
# names — that is the point, because a database whose ledger records the old name
# at that version must be STOPPED rather than migrated forward, and
# dbmigrate.assertLedgerMatches is what stops it. To this gate that is
# indistinguishable from the outage it exists to report: version 0001 claimed by
# two different migrations.
#
# A consolidation therefore DECLARES itself with MIGRATION_VERSIONS_BASELINE_RESET=1.
# The declaration is not taken at its word.
#
# WHY IT IS CHECKED, AND WHY THAT MATTERS MORE HERE THAN USUALLY. A declaration
# that is merely believed has to be REMOVED once the consolidation lands, and
# whoever forgets has left this gate reporting findings it no longer enforces —
# permanently, silently, and looking green. CI's environment is a committed file,
# so "set it for one PR" is not a thing the environment can express. So the
# declaration is honored only in the situation it describes, and it goes inert by
# itself: after the consolidation merges, the base IS the baseline, every version
# keeps its name, and the check below refuses the declaration on its own.
#
# THE CHECK, in two parts, both required. A consolidation (a) replaces a
# namespace wholesale, so the tree and the base share NO (version, name) pair in
# it, and (b) COLLAPSES, ending with strictly fewer migrations than the base had.
# A broken rebase — the case that would otherwise ride in on the same
# declaration — leaves surviving migrations at the versions and names they
# already had, so it fails (a); and renaming a small namespace's files one for
# one shares nothing but fails (b). Part (b) is also what makes the declaration
# inert after the merge regardless of how few files a namespace has: the base
# then holds exactly what the tree does, so the count cannot fall.
#
# Per namespace, because a branch may consolidate one and not the other.
baseline_reset="${MIGRATION_VERSIONS_BASELINE_RESET:-}"

# reset_admitted NS TREE_ROWS BASE_ROWS — is a declared reset true of this
# namespace? Only when it collapses AND shares no "<version> <name>" line.
reset_admitted() {
  local ns="$1" tree_rows="$2" base_rows="$3" shared
  [ "$baseline_reset" = "1" ] || return 1

  # FAIL CLOSED from here down. Admitting is what disarms the gate, so anything
  # this function is not sure of has to be "no". An empty row set or a comparison
  # that did not run would otherwise yield zero shared migrations — which is
  # exactly the shape of a true consolidation.
  if [ -z "$tree_rows" ] || [ -z "$base_rows" ]; then
    echo "note: $ns declares a baseline reset but one side has no migrations to compare — refusing the declaration rather than reading that as 'nothing in common'" >&2
    return 1
  fi
  # A consolidation COLLAPSES: it must end with strictly fewer migrations than
  # the base had. Sharing no version is necessary and not sufficient — renaming
  # a one-file namespace's single file also shares nothing, and that is a broken
  # rebase, not a consolidation. Requiring a collapse is also what keeps this
  # declaration inert after the merge for a namespace of ANY size: the base then
  # has exactly what the tree has, so the count never falls.
  local tree_n base_n
  tree_n="$(echo "$tree_rows" | grep -c . || true)"
  base_n="$(echo "$base_rows" | grep -c . || true)"
  if [ "${tree_n:-0}" -ge "${base_n:-0}" ]; then
    echo "note: $ns declares a baseline reset but does not collapse ($tree_n migration(s) here, $base_n on $BASE_REF) — a consolidation ends with fewer, so the findings below are enforced" >&2
    return 1
  fi

  # wc -l, not grep -c: grep exits 1 when it counts zero, which is the ADMITTING
  # case, so a failure and a true consolidation would be the same exit status.
  shared="$(comm -12 <(echo "$tree_rows") <(echo "$base_rows") | wc -l | tr -d '[:space:]')"
  case "$shared" in
    ''|*[!0-9]*)
      echo "note: $ns declares a baseline reset but the comparison produced '$shared' instead of a count — refusing the declaration" >&2
      return 1 ;;
  esac
  if [ "$shared" -ne 0 ]; then
    echo "note: $ns declares a baseline reset but keeps $shared migration(s) at the version AND name the base has — that is not a consolidation, so the findings below are enforced" >&2
    return 1
  fi
  echo "note: $ns is a declared baseline reset and shares no version with $BASE_REF — findings are reported, not enforced"
  return 0
}

# fail MESSAGE — report a finding, and fail unless this namespace is an admitted
# baseline reset.
fail() {
  if [ "$ns_reset" = "1" ]; then
    echo "baseline-reset (would FAIL): $1" >&2
    return
  fi
  echo "FAIL: $1" >&2
  failed=1
}

for dir in "$MIGRATIONS_DIR"/*/; do
  [ -d "$dir" ] || continue
  ns="$(basename "$dir")"
  # A namespace is a directory holding migrations, not merely a directory:
  # `testdata/` sits beside core/ and custom/ and is neither.
  compgen -G "$dir*.up.sql" >/dev/null || continue
  checked=$((checked + 1))

  tree_rows="$(migrations_in_tree "$ns")"

  # A duplicate inside one tree fails the loader at runtime; naming it here
  # gives the same answer without booting Postgres, and keeps this gate honest
  # when there is no base ref to compare against.
  dupes="$(echo "$tree_rows" | cut -d' ' -f1 | uniq -d)"
  if [ -n "$dupes" ]; then
    echo "FAIL: $ns declares one version twice — the namespace will not load:" >&2
    for v in $dupes; do
      echo "  $v: $(echo "$tree_rows" | awk -v v="$v" '$1==v {print $2}' | tr '\n' ' ')" >&2
    done
    failed=1
    continue
  fi

  base_rows="$(migrations_at_base "$ns")"
  if [ -z "$base_rows" ]; then
    continue # a namespace this branch introduces has nothing to sort after
  fi
  base_max="$(echo "$base_rows" | cut -d' ' -f1 | tail -n1)"

  # Decided per namespace and BEFORE the row loop, so every finding in it is
  # judged by one verdict rather than re-deriving it per migration.
  ns_reset=0
  if reset_admitted "$ns" "$tree_rows" "$base_rows"; then
    ns_reset=1
  fi

  while read -r version name; do
    [ -n "$version" ] || continue
    base_name="$(echo "$base_rows" | awk -v v="$version" '$1==v {print $2}')"

    # The base already carries this version. Same file: a migration this branch
    # did not touch, which is the overwhelming majority. Different file: TWO
    # migrations claiming one version — the outage, and the case a per-tree
    # loader test cannot see, because in this branch's tree the version is
    # unique and the conflict exists only against the base.
    if [ -n "$base_name" ]; then
      # The base carries this version MORE THAN ONCE, so the base is the thing
      # that is broken and this branch is the repair. A repair leaves one of the
      # colliding migrations at the version and renumbers the other, so the
      # branch's name being one of the base's is the shape of a fix rather than
      # of a new collision. Without this the gate refuses every repair of the
      # outage it exists to report, and the only way back to a loadable
      # namespace is to bypass the gate.
      if [ "$(echo "$base_name" | wc -l)" -gt 1 ] && echo "$base_name" | grep -qxF "$name"; then
        echo "note: $ns/$version is claimed twice on $BASE_REF ($(echo "$base_name" | tr '\n' ' ')); this branch keeps '$name' at it — repairing, not colliding"
        continue
      fi
      if [ "$base_name" != "$name" ]; then
        fail "$ns/$version is claimed by two different migrations — '$name' here, '$base_name' on $BASE_REF. Rename this one above $base_max and rebase (in core, re-stamp it with 'make migrate-create'). If this branch is a baseline consolidation, set MIGRATION_VERSIONS_BASELINE_RESET=1"
      fi
      continue
    fi

    # A version the base does not carry, at or below what it has already
    # reached: not a collision, and worse than one. It still applies — pgmigrate
    # skips only what the ledger already names — but it applies in a different
    # PLACE on a fresh database than on one already past $base_max.
    if [[ ! "$version" > "$base_max" ]]; then
      fail "$ns/$version ('$name') sorts at or below $base_max, the highest on $BASE_REF — it would still be applied, but in a different place on each installation: before $base_max on a fresh database, after it on one already past it. Anything order-dependent then leaves the two schemas different with nothing to report it. Rename it above $base_max and rebase (in core, re-stamp it with 'make migrate-create'). If this branch is a baseline consolidation, set MIGRATION_VERSIONS_BASELINE_RESET=1"
    fi
  done <<<"$tree_rows"
done

if [ "$failed" -ne 0 ]; then
  exit 1
fi

if [ "$baseline_reset" = "1" ]; then
  # Which namespaces the declaration actually covered is in the per-namespace
  # notes above. Saying only "a reset was declared" here would read as though it
  # applied everywhere, including to a namespace that refused it.
  echo "OK: check-migration-versions — $checked namespace(s) checked against $BASE_REF; a baseline reset was declared, and admitted only where the notes above say so"
  exit 0
fi

echo "OK: check-migration-versions — $checked namespace(s) sort after $BASE_REF"
