#!/usr/bin/env bash
# The extension migration gate (ADR-0069): every enabled unit's migrations are
# applied as its restricted ext_<name> role against a throwaway clone, and the
# resulting catalog is checked against the allowlist. See
# backend/tools/extmigrategate for what is asserted and why.
#
# This is the ONE gate in the extension tier that is not textual, and it is the
# only one that can refuse what a scanner cannot see (a CREATE TABLE inside a
# DO block, a policy that reads USING (true), an index colliding with another
# unit's table). gen-composition's pre-apply rule names it as the closing gate
# for exactly those.
#
# DATABASE POSTURE. check-backend is otherwise the hermetic lane, and this
# script keeps it that way for as long as no unit ships migrations: a tree
# where no extensions/*/migrations directory exists exits 0 before touching
# Postgres. The first unit that DOES ship one makes the lane need the test
# cluster, which is deliberate — a migration nobody applied is a migration
# nobody checked — and CI runs the deterministic gates with Postgres up.
set -euo pipefail
cd "$(dirname "$0")/.."

units=()
for layer in extensions/*/migrations; do
  [ -d "$layer" ] || continue
  units+=("$(basename "$(dirname "$layer")")")
done

if [ "${#units[@]}" -eq 0 ]; then
  echo "OK: check-ext-migrations — no extension declares a migrations/ layer"
  exit 0
fi

# shellcheck source=scripts/lib-testdb.sh
source "$(pwd)/scripts/lib-testdb.sh"
parse_test_dsn

# The root workspace, like every other backend/tools generator: the gate is in
# the separate tools module, so a caller-exported GOWORK would break resolution.
root="$(pwd)"

# A THROWAWAY database, never the dev or template database: the gate creates
# and drops a LOGIN role and applies unreviewed DDL. One database serves every
# unit — each owns a distinct ext_<name> role and the gate reverts its own
# migrations, so a later unit sees the same empty ext schema the first one did.
db="margince_extgate_$$"
db_admin recreate-db --name "$db" >/dev/null
trap 'st=$?; if ! drop_clone "$db"; then echo "FAIL: gate db $db was not dropped — leaked on the test cluster" >&2; if [[ "$st" -eq 0 ]]; then st=1; fi; fi; exit "$st"' EXIT

# Migrated here, VANILLA, rather than cloned from margince_test — and the
# GOWORK pin is the whole point, not tidiness.
#
# extmigrategate applies each unit's migrations itself and assumes it starts
# from an empty ext schema ("the database is thrown away after one run and
# always starts empty", tools/extmigrategate/main.go). Since cmd/migrate began
# applying the composed set, a database migrated under the COMPOSED workspace
# already holds ext.ext_<name>_* for every unit shipping a layer — the very
# units this gate is about to apply — and the gate would fail on
# `relation already exists`. The shared margince_test template is exactly such
# a database whenever `make test-db-up` or the integration lane touched it
# last, which makes the old clone-the-template posture depend on what ran
# before it. Migrating a fresh database under the ROOT workspace makes the
# gate's premise structurally true instead of incidentally true, and stops a
# `make check` run mutating the template as a side effect.
( cd backend && GOWORK="$root/go.work" go run ./cmd/migrate up --dsn "$(owner_clone_dsn "$db")" >/dev/null )
for unit in "${units[@]}"; do
  ( cd backend && GOWORK="$root/go.work" go run ./tools/extmigrategate \
      -unit "$unit" \
      -dir "../extensions/$unit/migrations" \
      -dsn "$(owner_clone_dsn "$db")" )
done

echo "OK: check-ext-migrations — ${#units[@]} unit(s) apply as their ext_<name> role and the catalog holds"
