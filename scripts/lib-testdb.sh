#!/usr/bin/env bash
# Shared helpers for the parallel integration lanes (test-integration-parallel.sh
# and test-integration-one.sh): parse this repo's owner + app DSNs, clone/drop a
# throwaway per-package database, derive a per-slot MinIO bucket, and resolve the
# per-package test timeout both lanes answer to. Source this; don't execute it.
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

# migrate_template — apply any embedded migration the template has not recorded
# (cmd/migrate → migrations.Core/Custom + the composed extension set's
# namespaces, then River). Idempotent: the runner
# compares the tracking tables against the embedded set, so with nothing missing
# it applies nothing.
#
# It reports a heal and stays SILENT otherwise, and the asymmetry is the point.
# `migrate up` ends with "schema is at head", which is a stronger claim than it
# can make: dbmigrate.Up appends versions absent from the tracking table and
# skips every version already recorded, without checksumming what was applied.
# A template carrying an EDITED migration, or one whose migration no longer
# exists at this head, records the version either way — so that line would
# announce currency over exactly the stale schema this function exists to catch.
# Passing it through would re-create the silent-staleness bug one level up.
# What is printed instead says only what actually happened.
migrate_template() {
  # rc, not `status`: zsh makes that name read-only, and these helpers are
  # sourced from an interactive shell often enough to care.
  local out rc=0
  # stderr is deliberately NOT captured. `go run` writes build and
  # module-download diagnostics there, so a cold Go cache would put them in
  # front of the summary this classifies on and report a template at head as
  # behind. Left alone it goes to the terminal, which is where a real failure
  # belongs anyway.
  out="$( cd backend && go run ./cmd/migrate up --dsn "$(owner_clone_dsn "$TEMPLATE_NAME")" )" || rc=$?
  if (( rc != 0 )); then
    return "$rc"
  fi
  # The summary is the LAST line, matched as its own string rather than as a
  # prefix of the whole capture — same reason: anything printed ahead of it
  # must not decide this.
  # The prefix tracks cmd/migrate's upSummaryFormat exactly. It counts the
  # extension namespaces in the same total as core+custom, which is what keeps
  # a template missing an extension's migration reading as "was behind" rather
  # than passing this check on the core lane alone. Drift would make this cry
  # wolf on every run; TestUpSummaryMatchesTheShellMatcher (backend/cmd/migrate)
  # reads THIS line and fails when the two disagree, so edit both together.
  local summary="${out##*$'\n'}"
  if [[ "$summary" != "applied 0 core+custom+extension + 0 river"* ]]; then
    echo "test-db: template ${TEMPLATE_NAME} was behind — ${summary%%; *}"
  fi
}

# build_template — (re)create margince_test and migrate it to head. Fresh each
# call so the template can never carry a stale schema. Runs from the repo root;
# the caller must have cd'd there (both scripts do).
build_template() {
  # Stop here if the recreate failed. The PREVIOUS template survives such a
  # failure, so migrating on regardless would bring the old one to head and
  # return success — and `make test-db-up` would report a rebuild that never
  # happened, over a template whose contents nobody chose.
  db_admin recreate-db --name "$TEMPLATE_NAME" >/dev/null || return $?
  migrate_template >/dev/null
}

# ensure_template — the fast path for the single-package inner loop: reuse the
# template rather than rebuilding it, but never reuse it blindly.
#
# PRESENT IS NOT CURRENT. This probed only for existence, and reused whatever it
# found however old it was, so a template built before a migration landed handed
# every clone a schema behind head. The failure that produces is thoroughly
# misleading: tests fail inside the constraint or column the new migration adds,
# naming code that is correct, in a package that has nothing to do with the
# change you pulled. The full lane never shows it — that path calls
# build_template — so it bites exactly when you are iterating on one package.
#
# Migrating rather than rebuilding is the cheap fix: with nothing missing the
# runner applies nothing, and behind it applies only the delta. What this does
# NOT heal is a template that has DIVERGED rather than fallen behind — an edited
# migration, or a checkout that no longer carries one the template already
# applied. The tracking table records a version, not a checksum, so neither case
# is even detectable here; migrate_template's silence means "nothing was
# missing", never "the schema is correct". Migrations are additive by repo rule,
# so falling behind is the case that happens; for the other, `make test-db-up`
# rebuilds from scratch.
#
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
  if [[ "$exists" != "true" ]]; then
    build_template
    return
  fi
  migrate_template
}

# make_clone db — drop any stale clone, then copy the migrated template (a fast
# file copy; no re-migration).
#
# CREATE ... TEMPLATE refuses while ANY session is connected to the source, and
# one now can be: ensure_template migrates the template on every inner-loop run,
# so a second `make test-it` started alongside the first can reach its clone
# while that migration still holds the connection. Before, the inner loop only
# probed for existence over the maintenance database and never touched the
# template at all.
#
# Retried rather than locked, because the two callers want opposite things from
# a lock: the parallel lane clones 25 times at once and must not serialize,
# while the migration must exclude clones. A reader/writer lock in portable
# shell buys a correctness problem of its own. The window here is one `migrate
# up` against a template that is nearly always at head — sub-second, and hit
# only by an overlapping start — so backing off and retrying closes it without
# constraining the lane. The last failure propagates with its stderr: a clone
# that cannot be made is fatal, never silently skipped.
# The retry budget is read INSIDE the function, not from a script-level
# variable: make_clone is `export -f`'d into xargs worker subshells, which
# inherit exported variables only — TEMPLATE_NAME above is exported for exactly
# that reason, and a bare one here would arrive empty in every lane worker.
make_clone() {
  local db="$1" retries="${CLONE_RETRIES:-3}" attempt=1 out rc
  # Validated, not coerced: shell arithmetic reads a non-numeric name as 0,
  # which would turn the budget into "give up immediately", and an absurd one
  # into a loop that sleeps its way past any timeout. Either way the operator
  # asked for something this cannot honour, so say so.
  if [[ ! "$retries" =~ ^[1-9][0-9]{0,2}$ ]]; then
    echo "FAIL: CLONE_RETRIES must be a positive integer up to 999, got '${retries}'" >&2
    return 1
  fi
  while :; do
    rc=0
    out="$(db_admin recreate-db --name "$db" --template "$TEMPLATE_NAME" 2>&1)" || rc=$?
    if (( rc == 0 )); then
      return 0
    fi
    if (( attempt >= retries )); then
      echo "$out" >&2
      echo "FAIL: could not clone ${TEMPLATE_NAME} into ${db} after ${retries} attempts" >&2
      return "$rc"
    fi
    sleep 1
    attempt=$(( attempt + 1 ))
  done
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

# resolve_it_timeout — set IT_TIMEOUT, the per-package `go test -timeout`, from
# INTEGRATION_TIMEOUT or the lane default. Both entry points run whole packages
# against the same suites, so they answer to one budget and one spelling of the
# rule; a second copy is how the two drift into disagreeing about what a package
# is allowed to cost. Exits rather than returning on a bad value: a lane that ran
# on a budget nobody asked for is worse than one that refuses to start.
#
# The budget is sized for the slowest package, not the median: compose/integration
# alone runs within a few seconds of 300s and tips over it under the concurrency
# the parallel lane itself creates, which reads as a regression in whatever branch
# happens to be running. 600s is headroom while that package is split.
#
# `go test -timeout` also accepts 10m or 1h30s. The parallel lane's budget column
# reads this as a seconds count, so anything else would price every package
# against a nonsense denominator and print a percentage nobody can act on.
# Rejecting the spelling is better than reporting confidently wrong numbers.
#
# Zero is rejected separately and matters more: `go test -timeout 0` DISABLES the
# timeout, so a run that meant to loosen the budget would instead remove the guard
# entirely and let a hung package sit until the CI job's own limit — the one
# failure this bound exists to turn into a legible message.
resolve_it_timeout() {
  IT_TIMEOUT="${INTEGRATION_TIMEOUT:-600s}"
  if [[ ! "$IT_TIMEOUT" =~ ^[0-9]+s$ ]]; then
    echo "FAIL: INTEGRATION_TIMEOUT must be <seconds>s (e.g. 600s), got '${IT_TIMEOUT}'"
    exit 1
  fi
  # Matched, not evaluated: `(( 08 == 0 ))` reads a leading zero as octal and
  # fails with "value too great for base", which leaves 08s and 09s accepted
  # behind a bash error nobody asked about. A zero budget is a string question,
  # so ask it as one.
  if [[ "${IT_TIMEOUT%s}" =~ ^0+$ ]]; then
    echo "FAIL: INTEGRATION_TIMEOUT must be greater than 0s — go test reads 0 as NO timeout, which "\
"removes the per-package guard rather than widening it"
    exit 1
  fi
}
