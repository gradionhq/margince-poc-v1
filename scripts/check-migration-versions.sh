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
# a collision, and is worse than one: pgmigrate records what it applied, so a
# database already past that number never runs the new migration and never says
# so. Two installations then differ by a migration neither can see is missing —
# the same silent divergence the root CLAUDE.md's "additive migrations only"
# rule exists to prevent. The fix in both cases is the same and is cheap:
# renumber above the base and rebase.
#
# The namespace list is derived from the tree (backend/migrations/*/), not
# hand-maintained, so a third namespace is gated the day it appears. Both
# shapes work: core's 4-digit sequence and custom's YYYYMMDDHHMMSS stamp both
# sort correctly as fixed-width strings, which is the same ordering pgmigrate
# itself applies.
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
        echo "FAIL: $ns/$version is claimed by two different migrations — '$name' here, '$base_name' on $BASE_REF. Renumber this one above $base_max and rebase" >&2
        failed=1
      fi
      continue
    fi

    # A version the base does not carry, at or below what it has already
    # reached: not a collision, and worse than one. pgmigrate records what it
    # applied, so an installation past $base_max never runs this and never
    # reports it missing.
    if [[ ! "$version" > "$base_max" ]]; then
      echo "FAIL: $ns/$version ('$name') sorts at or below $base_max, the highest on $BASE_REF — an installation past $base_max would never apply it, and would not report it missing. Renumber above $base_max and rebase" >&2
      failed=1
    fi
  done <<<"$tree_rows"
done

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "OK: check-migration-versions — $checked namespace(s) sort after $BASE_REF"
