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
# THE 4-DIGIT SEQUENCE 0001-0284 IS CLOSED, NOT RENAMED. A version already
# recorded in a database's schema_migrations_core cannot be renamed without
# stranding that database — dbmigrate.assertLedgerMatches refuses to continue,
# by design, because the migration in that slot would otherwise be skipped as
# done forever. Ten-digit stamps sort after every four-digit one, so the two
# eras coexist in one namespace with no renumbering and no lost history.
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
if ! printf '%s' "$NAME" | grep -qE '^[a-z][a-z0-9_]*$'; then
  echo "new-migration: '$NAME' — a migration name is lower-case letters, digits and underscores, starting with a letter (e.g. add_renewal_risk)" >&2
  exit 1
fi

VERSION="$(date +%s)"
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
