#!/usr/bin/env bash
# The schema a migration namespace BUILDS, as a comparable artifact.
#
# WHY THIS EXISTS. `backend/migrations/core/` is a baseline plus whatever has
# landed since, and the tracking table records a version, not a checksum — so
# nothing in the tree says what schema the namespace is supposed to produce.
# That gap is what let a consolidation be unreviewable: a squashed baseline is
# only trustworthy if "the baseline builds the same schema the history built" is
# a statement somebody can run, and it is the same statement as "this migration
# changed the schema in exactly this way".
#
#   verify REF build one database from REF's migrations and one from this
#              tree's, and diff the two schemas. Empty is the only pass. This is
#              what makes a baseline consolidation reviewable; it is also how
#              the NEXT consolidation gets checked.
#
# For the everyday question — did the migration I just wrote change head, and
# how — the gate is TestMigrationsBuildTheCommittedSchema against
# backend/migrations/testdata/head_catalog.txt, which needs only a database
# connection. This script is the heavier tool, for comparing two migration
# HISTORIES rather than a history against a committed expectation.
#
# pg_dump runs INSIDE the Postgres container, never on the host. A host pg_dump
# is whatever the developer's package manager installed — 18.2 against a 16.14
# server here — and a newer pg_dump emits SQL shaped for the newer server, so
# the artifact would differ per machine while describing one schema.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

CONTAINER="${MARGINCE_PG_CONTAINER:-margince-poc-v1-postgres-1}"
OWNER="${MARGINCE_PG_OWNER:-margince_owner}"

# Tracking tables are excluded from every dump: they record which migrations ran,
# which is the one thing two namespaces that build the same schema must be
# allowed to disagree about — a baseline has 26 rows where a history has 341.
EXCLUDE=(--exclude-table='schema_migrations_*' --exclude-table-data='schema_migrations_*')

# NO --schema RESTRICTION, deliberately. `--schema=public` looks like the right
# narrowing and silently drops three things core builds: the four
# CREATE EXTENSION statements, the `ext` schema every extension unit's tables
# live in, and that schema's GRANT and COMMENT. pg_dump documents this — with -n
# it makes no attempt to dump objects the selected schema depends upon — and the
# failure is invisible, because a dump that omits an extension still restores
# fine against a database that already has it. The database holds only `public`
# and `ext`; pg_dump never emits system schemas, so there is nothing to narrow.

die() { echo "migration-baseline: $*" >&2; exit 1; }

require_container() {
  docker exec "$CONTAINER" true 2>/dev/null ||
    die "container '$CONTAINER' is not running — start it with 'make db-up' (override with MARGINCE_PG_CONTAINER)"
}

psql_owner() { docker exec -i "$CONTAINER" psql -U "$OWNER" -v ON_ERROR_STOP=1 -q "$@"; }

recreate_db() {
  local db="$1"
  psql_owner -d postgres -c "DROP DATABASE IF EXISTS $db" >/dev/null
  psql_owner -d postgres -c "CREATE DATABASE $db" >/dev/null
}

# apply_namespaces DB MIGRATIONS_DIR NS... — replay a namespace the way
# dbmigrate.Up does: one transaction per migration carrying the migration SQL
# and its tracking row together, ordered by the version prefix as a STRING.
#
# The tracking row is not bookkeeping. core's employment-notice-period migration
# reads schema_migrations_core.applied_at, so a replay that skips the ledger is
# not a replay of the same migrations.
apply_namespaces() {
  local db="$1" dir="$2"; shift 2
  docker exec "$CONTAINER" rm -rf /tmp/margince-mig
  docker cp "$dir" "$CONTAINER":/tmp/margince-mig >/dev/null

  local ns
  for ns in "$@"; do
    docker exec -e NS="$ns" -e DB="$db" -e OWNER="$OWNER" "$CONTAINER" bash -c '
      set -euo pipefail
      cd "/tmp/margince-mig/$NS"
      table="schema_migrations_${NS}"
      psql -U "$OWNER" -d "$DB" -v ON_ERROR_STOP=1 -q -c "
        CREATE TABLE IF NOT EXISTS ${table} (
          version    text PRIMARY KEY,
          name       text NOT NULL,
          applied_at timestamptz NOT NULL DEFAULT now()
        )"
      for f in $(LC_ALL=C ls *.up.sql | LC_ALL=C sort); do
        key="${f%.up.sql}"
        { cat "$f"
          printf "\nINSERT INTO %s (version, name) VALUES (%s, %s);\n" \
            "$table" "'"'"'${key%%_*}'"'"'" "'"'"'${key#*_}'"'"'"
        } | psql -U "$OWNER" -d "$DB" -v ON_ERROR_STOP=1 --single-transaction -q >/dev/null ||
          { echo "migration-baseline: $NS/$f failed to apply" >&2; exit 1; }
      done
    ' || die "replaying $ns into $db failed"
  done
}

# strip_preamble — the text pg_dump varies for reasons a migration never chose.
#
# `--` comment lines carry the server version and each object's owner. The
# SET/set_config preamble is session state. `\restrict`/`\unrestrict` wrap the
# dump in a psql guard whose token is REGENERATED AT RANDOM on every run, so a
# dump that keeps it never equals itself — this is the one entry here that is not
# merely noise but an outright correctness bug if left in.
strip_preamble() {
  sed -E '/^--/d; /^SET /d; /^SELECT pg_catalog\.set_config/d; /^\\(un)?restrict /d'
}

# dump_schema DB — the schema, normalized to what two databases built by
# equivalent migrations must agree on.
#
# Beyond strip_preamble, nothing is touched: column order, defaults, constraint
# and index names, trigger bodies and grants are all things a migration decides,
# so all of them are compared exactly.
dump_schema() {
  local db="$1"
  docker exec "$CONTAINER" pg_dump -U "$OWNER" -d "$db" \
      --schema-only "${EXCLUDE[@]}" |
    strip_preamble |
    cat -s
}

# dump_data DB — the reference rows the migrations insert.
#
# Every table, not a hand-kept list of the five that currently hold rows at head.
# A list would go stale the first time somebody seeds a sixth, and silently: the
# row would simply stop being compared. On a migrated-but-unbootstrapped database
# the only rows present ARE the ones migrations wrote.
#
# TWO VALUE KINDS ARE MASKED, and only these two: a `uuidv7()` primary key and a
# `now()` timestamp are generated per run, so comparing them literally compares
# the clock and the random source rather than the migrations. Everything that
# carries meaning — every key, label, sort order, flag and enum value — is
# compared exactly. Nothing is lost by masking the uuids here because no
# reference row references another BY uuid; they link on text keys
# (`field_mask.role_key = 'rep'`). A future reference row with a uuid foreign key
# would need that link compared some other way, and this comment is where to
# start.
dump_data() {
  local db="$1"
  docker exec "$CONTAINER" pg_dump -U "$OWNER" -d "$db" \
      --data-only --column-inserts "${EXCLUDE[@]}" |
    strip_preamble |
    sed -E "s/'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'/'<uuid>'/g;
            s/'[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?\+[0-9]{2}'/'<timestamp>'/g" |
    LC_ALL=C sort |
    cat -s
}

# capture DB OUT — schema then data, as one artifact.
#
# Data is sorted, schema is not: pg_dump emits objects in dependency order,
# which is information, while its row order is whatever the heap gave it and
# differs between two databases that hold identical rows.
capture() {
  local db="$1" out="$2"
  { echo "-- schema"; dump_schema "$db"; echo "-- data"; dump_data "$db"; } > "$out"
}

cmd_verify() {
  local ref="${1:-}"
  [ -n "$ref" ] || die "usage: migration-baseline.sh verify <base-ref>"
  require_container
  git rev-parse --verify -q "$ref" >/dev/null || die "base ref '$ref' not found — fetch it first"

  # The base ref's migrations have to come from a checkout of the base ref, and
  # this session's worktree is not free to switch branches underneath the run.
  local wt; wt="$(mktemp -d)"
  git worktree add --detach "$wt" "$ref" >/dev/null 2>&1 || die "could not check out $ref"
  # shellcheck disable=SC2064
  trap "git worktree remove --force '$wt' >/dev/null 2>&1 || true" EXIT

  local before after
  before="$(mktemp)"; after="$(mktemp)"

  echo "migration-baseline: building the schema $ref's migrations produce"
  recreate_db margince_baseline_before
  apply_namespaces margince_baseline_before "$wt/backend/migrations" core custom
  capture margince_baseline_before "$before"

  echo "migration-baseline: building the schema this tree's migrations produce"
  recreate_db margince_baseline_after
  apply_namespaces margince_baseline_after backend/migrations core custom
  capture margince_baseline_after "$after"

  psql_owner -d postgres -c "DROP DATABASE IF EXISTS margince_baseline_before" >/dev/null
  psql_owner -d postgres -c "DROP DATABASE IF EXISTS margince_baseline_after" >/dev/null

  if diff -u "$before" "$after" > /tmp/margince-baseline.diff; then
    local n_before n_after
    n_before="$(git ls-tree --name-only -r "$ref" backend/migrations/core backend/migrations/custom | grep -c '\.up\.sql$')"
    n_after="$(find backend/migrations/core backend/migrations/custom -name '*.up.sql' | wc -l | tr -d ' ')"
    echo "OK: $n_after migration(s) build the schema $ref's $n_before build — byte-identical"
    return 0
  fi
  echo "FAIL: this tree's migrations build a DIFFERENT schema than $ref's" >&2
  echo "      '-' is $ref, '+' is this tree:" >&2
  sed -n '1,200p' /tmp/margince-baseline.diff >&2
  echo "      (full diff: /tmp/margince-baseline.diff)" >&2
  return 1
}

case "${1:-}" in
  verify) shift; cmd_verify "$@" ;;
  *) die "usage: migration-baseline.sh verify <base-ref>" ;;
esac
