#!/usr/bin/env bash
# Scaffold a core migration pair named for the current unix second.
#
# WHY THE CLOCK AND NOT THE NEXT NUMBER. A sequential name is picked against
# whatever `main` looked like when the branch opened, so two branches open at
# once pick the SAME number and `main` stops loading the moment both merge —
# `pgmigrate: duplicate version 0240`, which is not a partially-migrated
# database but no migration at all, because the loader rejects the whole
# namespace before applying anything. That happened twice in two days (0240,
# then 0248). A clock reading is unique without consulting a base ref, and it
# still sorts in the order the migrations were written, which is the order
# pgmigrate applies them.
#
# THE 4-DIGIT SEQUENCE 0001-0292 IS CLOSED, NOT RENAMED. A version already
# recorded in a database's schema_migrations_core cannot be renamed without
# stranding that database — dbmigrate.assertLedgerMatches refuses to continue,
# by design, because the migration in that slot would otherwise be skipped as
# done forever. The two eras coexist because every version in that sequence is
# ZERO-PADDED and so sorts below any ten-digit stamp — not because ten digits
# beat four, which is false ("9999" sorts above "1787000000").
#
# Stamping the clock removes collisions, not the ordering obligation: a
# migration must still sort after everything on the base ref, so a branch that
# sat while another migration merged re-runs this script and moves its SQL to
# the fresh pair. check-migration-versions.sh is what reports that.
#
# Usage: new-migration.sh <name>     e.g. new-migration.sh add_renewal_risk
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CORE_DIR="$ROOT/backend/migrations/core"

NAME="${1:-}"
if [ -z "$NAME" ]; then
  echo "new-migration: a name is required, e.g. 'make migrate-create NAME=add_renewal_risk'" >&2
  exit 1
fi

# The name becomes the second half of the filename, which the loader splits on
# the FIRST underscore and pgmigrate then records in schema_migrations_core.
# Restricting it here keeps that recorded name a stable identifier rather than
# whatever a shell happened to pass through.
#
# The match is bash's, against the WHOLE string. `grep -q` would accept a
# multi-line name on the strength of one good line, and the other line would
# then sit in a filename that check-migration-versions.sh reads one row per
# line — one file parsed as two rows, in the gate that protects apply order.
if ! [[ "$NAME" =~ ^[a-z][a-z0-9_]*$ ]]; then
  echo "new-migration: '$NAME' — a migration name is lower-case letters, digits and underscores, starting with a letter (e.g. add_renewal_risk)" >&2
  exit 1
fi

VERSION="$(date +%s)"

# A stamp is only useful if it sorts above what core/ already holds, and a
# machine whose clock runs BEHIND produces one that does not. That is not the
# base-ref gate's case — it compares against origin/main, where the highest
# version is still from the closed sequence until the first stamped migration
# merges, so a stamp from 2001 would sail past it. Compare against the tree.
#
# It fires the other way too: once a migration stamped by a FAST clock is in
# the tree, every correct stamp after it is refused here, at the moment it is
# written, rather than in a gate whose advice is to re-stamp — which would
# reproduce the same refused number.
HIGHEST="$(find "$CORE_DIR" -maxdepth 1 -name '*.up.sql' -exec basename {} \; | cut -d_ -f1 | sort | tail -n1)"
if [ -n "$HIGHEST" ] && ! [[ "$VERSION" > "$HIGHEST" ]]; then
  echo "new-migration: this machine's clock reads $VERSION, which does not sort above $HIGHEST, the highest version in backend/migrations/core — either the clock is behind, or a migration already in the tree was stamped by one that runs fast" >&2
  exit 1
fi

UP="$CORE_DIR/${VERSION}_${NAME}.up.sql"
DOWN="$CORE_DIR/${VERSION}_${NAME}.down.sql"

# Two invocations inside the same second would otherwise silently overwrite the
# first pair, which reads as the scaffold having done nothing.
for f in "$UP" "$DOWN"; do
  if [ -e "$f" ]; then
    echo "new-migration: $f already exists — wait a second and run again, or pick another name" >&2
    exit 1
  fi
done

# The header line is the house shape every core migration carries: the version,
# then what the migration is for. Both halves are written, because a pair
# missing its .down.sql fails the loader rather than the review.
printf -- '-- %s: %s.\n' "$VERSION" "$NAME" >"$UP"
printf -- '-- Reverses %s.\n' "$VERSION" >"$DOWN"

echo "created ${UP#"$ROOT"/}"
echo "created ${DOWN#"$ROOT"/}"
