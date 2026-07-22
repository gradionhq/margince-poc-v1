#!/usr/bin/env bash
# Shared helpers for the parallel integration lanes (test-integration-parallel.sh
# and test-integration-one.sh): parse this repo's owner + app DSNs, clone/drop a
# throwaway per-package database, and derive a per-slot MinIO bucket. Source
# this; don't execute it.
#
# This repo's clone-per-package test-DB shape:
#   - TWO roles, not one — MARGINCE_TEST_DSN (owner: migrates + seeds) and
#     MARGINCE_TEST_APP_DSN (the RLS-bound app role the stores connect as). A
#     clone must be reachable by both, so we swap the db segment of each.
#   - Clones are copied from a migrated template (margince_test), CREATE DATABASE
#     ... TEMPLATE — a fast file copy. This repo has two kinds of integration
#     package: the compose/e2e suites migrate the database themselves, but the
#     module suites (people, agents, consent, identity) assume an already-migrated
#     database and only seed their own rows. A migrated template satisfies both:
#     the module suites get their schema for free, and the self-migrating suites
#     rebuild it once per process (harness migrate-once) — either way correct. The
#     template's migrations grant the cluster-level margince_app role USAGE + table
#     privileges (migration 0015), which the clone inherits, so the app role can
#     connect and query without any per-clone GRANT.
#   - Redis is a single shared instance passed through unchanged: only the events
#     package touches Redis (its own logical db 15, flushed per test), so no two
#     parallel packages contend for it. If a second Redis-using package is ever
#     added, give each slot a private index here (and teach that test to read it).

# parse_test_dsn: split MARGINCE_TEST_DSN (owner) and MARGINCE_TEST_APP_DSN (app)
# into the reusable prefix/suffix each clone DSN is built from. Both DSNs point
# at the same template db in normal use; we only ever swap the db name segment,
# never the credentials/host.
parse_test_dsn() {
  local owner="${MARGINCE_TEST_DSN:-postgres://margince_owner:dev@localhost:55432/margince}"
  local app="${MARGINCE_TEST_APP_DSN:-postgres://margince_app:margince_app_dev@localhost:55432/margince}"

  # Owner: peel scheme://user:pass@host:port | /db?query
  local o_body="${owner#*://}"
  O_PREFIX="${owner%%/"${o_body#*/}"}"       # scheme://user:pass@host:port
  local o_tail="${o_body#*/}"                 # db?query  (or db)
  local o_db="${o_tail%%\?*}"
  O_QUERY=""; [[ "$o_tail" != "$o_db" ]] && O_QUERY="${o_tail#*\?}"
  TEMPLATE_DB="$o_db"

  # App: same peel; the app credentials/host are preserved, only the db swaps.
  local a_body="${app#*://}"
  A_PREFIX="${app%%/"${a_body#*/}"}"
  local a_tail="${a_body#*/}"
  A_QUERY=""; local a_db="${a_tail%%\?*}"; [[ "$a_tail" != "$a_db" ]] && A_QUERY="${a_tail#*\?}"

  export O_PREFIX O_QUERY A_PREFIX A_QUERY TEMPLATE_DB
}

# db_admin verb [flags…] — create/drop/probe databases through cmd/migrate's
# db verbs, over the SAME owner DSN the migrations and tests use. psql is NOT
# a host requirement (hosts need Go + Docker only), and an overridden
# MARGINCE_TEST_DSN targets one cluster for clone + migrate + test alike —
# there is no second admin connection path that could point elsewhere. The
# maintenance `postgres` db is the target: CREATE/DROP DATABASE never runs
# inside the database being dropped. Runs from the repo root, like build_template.
db_admin() {
  ( cd backend && go run ./cmd/migrate "$@" --dsn "${O_PREFIX}/postgres${O_QUERY:+?$O_QUERY}" )
}

# The migrated template every per-package clone is copied from. Exported so the
# xargs -P worker subshells (fresh bash processes) see it — make_clone reads it.
export TEMPLATE_NAME="${TEMPLATE_NAME:-margince_test}"

owner_clone_dsn() { local db="$1"; echo "${O_PREFIX}/${db}${O_QUERY:+?$O_QUERY}"; }
app_clone_dsn()   { local db="$1"; echo "${A_PREFIX}/${db}${A_QUERY:+?$A_QUERY}"; }

# build_template — (re)create margince_test and migrate it to head with the same
# embedded migration set the app uses (cmd/migrate → migrations.Core/Custom).
# Fresh each call so the template can never carry a stale schema. Runs from the
# repo root; the caller must have cd'd there (both scripts do).
build_template() {
  db_admin recreate-db --name "$TEMPLATE_NAME" >/dev/null
  ( cd backend && go run ./cmd/migrate up --dsn "$(owner_clone_dsn "$TEMPLATE_NAME")" >/dev/null )
}

# ensure_template — build the template only if it is missing (fast reuse for the
# single-package inner loop; the full lane rebuilds fresh via build_template).
# db-exists separates "absent" (prints false) from "could not ask" (non-zero
# exit) exactly so this caller can too: a failed probe propagates with its
# stderr instead of reading as "missing" and force-rebuilding a healthy
# template over a transient error.
ensure_template() {
  local exists
  if ! exists="$(db_admin db-exists --name "$TEMPLATE_NAME")"; then
    echo "FAIL: could not probe for template ${TEMPLATE_NAME} — fix the error above; a failed probe is not 'missing'" >&2
    return 1
  fi
  [[ "$exists" = "true" ]] || build_template
}

# make_clone db — drop any stale clone, then copy the migrated template (a fast
# file copy; no re-migration). CREATE ... TEMPLATE needs no session connected to
# the template, which holds: nothing connects to margince_test after build.
make_clone() {
  local db="$1"
  db_admin recreate-db --name "$db" --template "$TEMPLATE_NAME" >/dev/null
}

# drop_clone db — remove a throwaway clone. Failures propagate (stderr and
# status): a clone that cannot be dropped is a leaked database on the test
# cluster, and callers fold that into their exit status instead of reporting
# a green run. drop-db is WITH (FORCE), so a just-exited test process whose
# backends linger can never flake the teardown — a failure here is real.
drop_clone() { local db="$1"; db_admin drop-db --name "$db" >/dev/null; }

# bucket_for SLOT [BASE] — DNS-compliant private MinIO bucket per slot (the store
# auto-creates it). Hyphen, never underscore.
bucket_for() { echo "${2:-${MARGINCE_TEST_BLOBSTORE_BUCKET:-margince-test}}-p${1}"; }
