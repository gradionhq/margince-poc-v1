#!/usr/bin/env bash
# Parallel integration-test runner.
#
# The serial lane runs every integration package with `go test -p 1` against ONE
# shared database, because parallel packages racing on the same DB collide on the
# schema each test rebuilds. That serialization is the only reason for -p 1 — the
# suites are I/O-bound (mostly idle on Postgres round-trips and TLS handshakes in
# the HTTP e2e), so the CPU sits unused while one package runs.
#
# This runner removes the shared-state constraint instead of the serialization.
# Each package gets private throwaway state, so concurrent packages share nothing:
#   Postgres — its own empty clone db (the Go harness migrates it once, then
#              TRUNCATEs between tests — see internal/platform/testdb).
#   MinIO    — a private bucket (MARGINCE_TEST_BLOBSTORE_BUCKET=<base>-p<idx>),
#              auto-created by the blobstore store.
#   Redis    — the one shared instance, but each package gets its own logical
#              db (MARGINCE_TEST_REDIS_DB, mapped 1..15 by slot), so no two
#              packages ever share a stream. Db 0 is reserved for `make dev`, so
#              a running dev stack and this lane never collide either. Only the
#              events package touches Redis today; the per-slot db keeps it
#              collision-free if that ever changes.
# Within a package nothing changes — still -p 1, the same sequential model that is
# green today — so no test file needs editing.
#
# Same teeth as the serial lane: zero-skip guard (a SKIP fails the run) and any
# package failure fails the whole run. MARGINCE_ENV=dev is exported so the HTTP
# e2e suites' X-Workspace-Slug trust switch is honored (same as the serial lane).
#
# Env:
#   INTEGRATION_JOBS   max concurrent packages (default: min(nproc, 8))
#   MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN   owner + app DSNs (Makefile defaults)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/lib-testdb.sh
source "$ROOT/scripts/lib-testdb.sh"
parse_test_dsn

# Optional coverage: when COVER_OUT is set (the sonarcloud CI job sets it), each
# package emits binary coverage into its own dir and we merge them into a single
# Go text profile at the end (go tool covdata — built-in, no external merge tool).
# This keeps the coverage run parallel instead of the old serial `-p 1 ./...`.
COVER_OUT="${COVER_OUT:-}"
if [[ -n "$COVER_OUT" ]]; then
  case "$COVER_OUT" in /*) ;; *) COVER_OUT="$ROOT/$COVER_OUT";; esac
  COVERDIR="$(mktemp -d)"; export COVERDIR
fi

# Per-package go-test timeout. Coverage instrumentation roughly doubles a
# package's wall-time, and the heaviest package (compose/integration, ~200
# serial re-migrating tests after the email-ingestion suites landed) crosses
# the tight 300s cap under coverage + parallel Postgres contention. Give the
# coverage run generous headroom; a plain run keeps the tight 300s for fast PR
# feedback. Overridable via INTEGRATION_TIMEOUT.
IT_TIMEOUT="${INTEGRATION_TIMEOUT:-$([[ -n "$COVER_OUT" ]] && echo 900s || echo 300s)}"
export IT_TIMEOUT

# Build the migrated template once, fresh, before fanning out. Every package
# clones from it (CREATE DATABASE ... TEMPLATE) instead of re-migrating.
echo "test-integration-parallel: building migrated template ${TEMPLATE_NAME}…"
build_template

# Every integration test in this repo lives in the backend module.
GO_DIRS=(backend)

ncpu() { sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo 4; }
JOBS="${INTEGRATION_JOBS:-$(( $(ncpu) < 8 ? $(ncpu) : 8 ))}"

# Build the work list: "module_dir|relative_pkg" for every package that carries a
# //go:build integration file.
WORK="$(mktemp)"
for d in "${GO_DIRS[@]}"; do
  [[ -d "$d" ]] || continue
  matches="$(grep -rl "go:build integration" --include="*.go" "$d" 2>/dev/null || true)"
  [[ -n "$matches" ]] || continue
  printf '%s\n' "$matches" | xargs -n1 dirname | sort -u | while IFS= read -r pkgdir; do
    rel="./${pkgdir#"$d"/}"; [[ "$pkgdir" = "$d" ]] && rel="."
    echo "$d|$rel"
  done
done > "$WORK"

NPKGS=$(wc -l < "$WORK" | tr -d ' ')
echo "test-integration-parallel: $NPKGS packages, up to $JOBS concurrent (template db=$TEMPLATE_DB)"

OUTDIR="$(mktemp -d)"; trap 'rm -f "$WORK"; rm -rf "$OUTDIR" "${COVERDIR:-}"' EXIT

# One job = clone an empty db + own a private MinIO bucket, run that package
# against them, drop the clone.
run_one() {
  local line="$1" idx="$2" outdir="$3"
  local d="${line%%|*}" rel="${line#*|}"
  local db="margince_it_p${idx}_$$"
  local log="$outdir/$idx.log"
  local bucket; bucket="$(bucket_for "$idx")"
  # Redis logical db per slot, in 1..15 — db 0 stays reserved for `make dev`.
  local redis_db=$(( 1 + (idx - 1) % 15 ))
  # Coverage args (empty unless COVERDIR is set). -coverpkg=./... attributes
  # cross-package exercise; binary output goes to a per-package dir, merged later.
  local cover_pre=() cover_post=()
  if [[ -n "${COVERDIR:-}" ]]; then
    mkdir -p "$COVERDIR/$idx"
    cover_pre=(-cover -coverpkg=./... -covermode=atomic)
    cover_post=(-args -test.gocoverdir="$COVERDIR/$idx")
  fi
  {
    echo "=== integration $d $rel (db=$db bucket=$bucket redis-db=$redis_db) ==="
    make_clone "$db"
    local st=0
    ( cd "$d" \
        && MARGINCE_ENV=dev \
           MARGINCE_TEST_DSN="$(owner_clone_dsn "$db")" \
           MARGINCE_TEST_APP_DSN="$(app_clone_dsn "$db")" \
           MARGINCE_TEST_BLOBSTORE_BUCKET="$bucket" \
           MARGINCE_TEST_REDIS_DB="$redis_db" \
        go test -p 1 -tags=integration -v -count=1 -timeout="$IT_TIMEOUT" \
          "${cover_pre[@]+"${cover_pre[@]}"}" "$rel" "${cover_post[@]+"${cover_post[@]}"}" ) || st=$?
    drop_clone "$db"
    echo "EXIT $st"
  } > "$log" 2>&1
}
export -f run_one owner_clone_dsn app_clone_dsn make_clone drop_clone db_admin bucket_for

# Fan out with a bounded worker pool. nl numbers the lines → stable per-job db
# names + logs.
nl -ba -w1 -s'|' "$WORK" \
  | xargs -P "$JOBS" -I{} bash -c 'line="{}"; idx="${line%%|*}"; rest="${line#*|}"; run_one "$rest" "$idx" "'"$OUTDIR"'"'

# Aggregate: print every log in package (idx) order, then enforce the teeth.
fail=0
ran=0
for base in $(cd "$OUTDIR" && ls -1 -- *.log 2>/dev/null | sort -n); do
  log="$OUTDIR/$base"
  cat "$log"
  ran=$((ran + 1))
  grep -q "^EXIT 0$" "$log" || fail=1
done

# Reconcile against discovery: a green run must have executed every package we
# found. NPKGS=0 (a go:build-integration selector regression that matches
# nothing) or a missing log (a worker that never wrote its EXIT line) must read
# as red — otherwise the "0 skips" sentinel below is a false green.
if [[ "$NPKGS" -eq 0 ]]; then
  echo "FAIL: no integration packages discovered — the 'go:build integration' selector matched nothing (regression?)"
  fail=1
elif [[ "$ran" -ne "$NPKGS" ]]; then
  echo "FAIL: ran $ran package log(s) but discovered $NPKGS — a worker did not report; treating as red"
  fail=1
fi

if grep -rq -- '--- SKIP' "$OUTDIR"; then
  echo "FAIL: integration tests must not skip — provision the env/service, do not skip:"
  grep -rh -- '--- SKIP' "$OUTDIR"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "FAIL: integration tests failed (parallel, $NPKGS packages) — see package logs above"
  exit 1
fi

# Merge per-package coverage into the single text profile SonarCloud reads.
if [[ -n "$COVER_OUT" ]]; then
  # Unit coverage: the parallel lane above ran only the integration-tagged
  # packages, but the ~20 unit-only packages need their own pass — otherwise
  # SonarCloud sees them at a false ~0% new-code coverage and the gate fails on
  # well-unit-tested code. This restores what the old serial
  # `go test -tags integration ./...` covered (it compiled + ran unit tests
  # across every package). Untagged tests never open a real DB (the test-lanes
  # gate enforces it), so this pass needs no clone/service.
  mkdir -p "$COVERDIR/unit"
  ( cd backend && go test ./... -cover -coverpkg=./... -covermode=atomic \
      -args -test.gocoverdir="$COVERDIR/unit" )
  dirs="$(find "$COVERDIR" -mindepth 1 -maxdepth 1 -type d | paste -sd, -)"
  if [[ -n "$dirs" ]]; then
    ( cd backend && go tool covdata textfmt -i="$dirs" -o="$COVER_OUT" )
    echo "coverage: merged $(printf '%s' "$dirs" | tr ',' '\n' | grep -c .) profiles (integration + unit) → $COVER_OUT"
  fi
fi

# Keep the exact success sentinel the gates grep for; the count is informational.
echo "OK: integration passed with 0 skips ($NPKGS packages, parallel)"
