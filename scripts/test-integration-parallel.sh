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
# Shard mode (the CI matrix): INTEGRATION_SHARD="k/N" slices the lane across N
# independent runners BY TEST, not by package — package-level fan-out bottoms
# out at the heaviest package (compose/integration is minutes of serial tests),
# so each shard runs a deterministic round-robin slice of every package's
# top-level Test functions via -run. Discovery is static (`func Test…` in the
# package's *_test.go files) so it costs no compile; files under a different
# lone build tag (e2e_llm, livesmoke) are skipped exactly as the compiler
# skips them, and a constraint expression the grep cannot decide fails
# discovery loudly instead of being silently mis-sliced. Shard teeth on top of
# the lane's own: the set of
# tests a shard actually ran must equal its assigned slice, and
# scripts/test-integration-reconcile.sh re-checks the union across shards
# (complete + disjoint) before merging coverage — a slicing bug can only read
# as red, never as a quietly thinner lane.
#
# Env:
#   INTEGRATION_JOBS        max concurrent packages (default: min(nproc, 8))
#   INTEGRATION_SHARD       "k/N" → run the k-th of N test slices (CI matrix)
#   INTEGRATION_SHARD_OUT   shard mode only: directory receiving the manifests
#                           (discovery/assigned/ran/meta) and per-package binary
#                           covdata pods for the CI fan-in to reconcile + merge;
#                           coverage instrumentation is on iff this is set
#   INTEGRATION_TIMEOUT     per-package go-test timeout (default 300s)
#   MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN   owner + app DSNs (Makefile defaults)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/lib-testdb.sh
source "$ROOT/scripts/lib-testdb.sh"
parse_test_dsn

SHARD_IDX=0 SHARD_TOTAL=0
if [[ -n "${INTEGRATION_SHARD:-}" ]]; then
  if [[ ! "$INTEGRATION_SHARD" =~ ^([0-9]+)/([0-9]+)$ ]]; then
    echo "FAIL: INTEGRATION_SHARD must be k/N (e.g. 3/8), got '${INTEGRATION_SHARD}'"
    exit 1
  fi
  SHARD_IDX="${BASH_REMATCH[1]}" SHARD_TOTAL="${BASH_REMATCH[2]}"
  if (( SHARD_IDX < 1 || SHARD_IDX > SHARD_TOTAL )); then
    echo "FAIL: INTEGRATION_SHARD index out of range: ${INTEGRATION_SHARD}"
    exit 1
  fi
fi

SHARD_OUT="${INTEGRATION_SHARD_OUT:-}"
if [[ -n "$SHARD_OUT" ]]; then
  if (( SHARD_TOTAL == 0 )); then
    echo "FAIL: INTEGRATION_SHARD_OUT is set but INTEGRATION_SHARD is not — the manifests only mean something for a shard"
    exit 1
  fi
  case "$SHARD_OUT" in /*) ;; *) SHARD_OUT="$ROOT/$SHARD_OUT";; esac
  # Binary covdata pods, one dir per package slot; the fan-in merges every
  # shard's pods (plus the unit pass) into the one text profile SonarCloud
  # reads. Kept binary here because `go tool covdata` merges dirs, not text.
  COVERDIR="$SHARD_OUT/covdata"
  rm -rf "$COVERDIR"
  rm -f "$SHARD_OUT/discovery.txt" "$SHARD_OUT/assigned.txt" "$SHARD_OUT/ran.txt" "$SHARD_OUT/meta.txt"
  mkdir -p "$COVERDIR"
  export COVERDIR
fi

# Per-package go-test timeout. Shard slices stay far under a full package even
# with coverage instrumentation, so one tight default serves both modes.
# Overridable via INTEGRATION_TIMEOUT.
IT_TIMEOUT="${INTEGRATION_TIMEOUT:-300s}"
if [[ -n "${COVERDIR:-}" && -z "${INTEGRATION_TIMEOUT:-}" ]] && (( SHARD_TOTAL == 1 )); then
  IT_TIMEOUT=900s
fi
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

NPKGS_DISCOVERED=$(wc -l < "$WORK" | tr -d ' ')

DISCOVERY="$(mktemp)" ASSIGNED="$(mktemp)" RAN="$(mktemp)"
OUTDIR="$(mktemp -d)"
trap 'rm -f "$WORK" "$DISCOVERY" "$ASSIGNED" "$RAN"; rm -rf "$OUTDIR" ${REGEX_DIR:+"$REGEX_DIR"}' EXIT

if (( SHARD_TOTAL > 0 )); then
  # Static discovery: every top-level Test function of every integration
  # package, as "module_dir|rel|TestName". TestMain is a fixture, not a test;
  # a lowercase rune after "Test" makes a plain function, not a test — both
  # match `go test`'s own rules, and the ran==assigned teeth below catch any
  # future divergence between this grep and the compiler's view.
  while IFS='|' read -r d rel; do
    pkgdir="$d/${rel#./}"; [[ "$rel" = "." ]] && pkgdir="$d"
    for f in "$pkgdir"/*_test.go; do
      [[ -e "$f" ]] || continue
      # Build-constrained files: a bare single tag is statically decidable —
      # `integration` is in this lane's build, any other lone tag (e2e_llm,
      # livesmoke: the paid opt-in lanes) is not, so that file's tests are
      # skipped exactly as the compiler skips them. An expression with
      # operators would need a real constraint evaluator: fail loudly (on
      # stderr — stdout here feeds the sort) rather than guess and quietly
      # mis-slice.
      skip_file=0
      while IFS= read -r expr; do
        [[ -n "$expr" ]] || continue
        if [[ ! "$expr" =~ ^[A-Za-z0-9_.]+$ ]]; then
          echo "FAIL: $f carries build constraint '$expr' — too complex for the static shard" >&2
          echo "  discovery in scripts/test-integration-parallel.sh; teach it or simplify the constraint" >&2
          exit 1
        fi
        [[ "$expr" = "integration" ]] || skip_file=1
      done < <(sed -n '/^package /q;p' "$f" | sed -nE 's|^//go:build ||p; s|^// \+build ||p')
      (( skip_file )) && continue
      # `|| true`: a helper-only test file with no Test functions is fine.
      { grep -hE '^func Test[A-Za-z0-9_]*\(' "$f" || true; } \
        | sed -E 's/^func (Test[A-Za-z0-9_]*)\(.*/\1/' \
        | while IFS= read -r name; do
            [[ "$name" = "TestMain" ]] && continue
            [[ "$name" != "Test" && "${name:4:1}" =~ [a-z] ]] && continue
            echo "$d|$rel|$name"
          done
    done
  done < "$WORK" | LC_ALL=C sort > "$DISCOVERY"

  NTESTS=$(wc -l < "$DISCOVERY" | tr -d ' ')
  if (( NTESTS < SHARD_TOTAL )); then
    echo "FAIL: discovered only $NTESTS integration tests for $SHARD_TOTAL shards — the discovery is broken or the shard count is absurd"
    exit 1
  fi

  # Deterministic round-robin over the sorted list: line i goes to shard
  # ((i-1) % N) + 1. Every shard computes the same list from the same commit,
  # so the slices are complete and disjoint by construction — and the fan-in
  # verifies exactly that instead of trusting it.
  awk -v k="$SHARD_IDX" -v n="$SHARD_TOTAL" 'NR % n == k % n' "$DISCOVERY" > "$ASSIGNED"

  # Shrink the work list to this shard's packages. Each package's anchored
  # -run union of its assigned test names goes to a per-slot FILE, not onto
  # the work line — hundreds of test names make a regex far past what xargs
  # -I can carry.
  GROUPED="$(mktemp)"
  awk -F'|' '
    { key = $1 "|" $2; re[key] = (key in re) ? re[key] "|" $3 : $3 }
    END { for (k in re) print k "|^(" re[k] ")$" }
  ' "$ASSIGNED" | LC_ALL=C sort > "$GROUPED"
  REGEX_DIR="$(mktemp -d)"
  export REGEX_DIR
  : > "$WORK"
  slot=0
  while IFS= read -r line; do
    slot=$((slot + 1))
    d="${line%%|*}" rest="${line#*|}"
    printf '%s|%s\n' "$d" "${rest%%|*}" >> "$WORK"
    printf '%s' "${rest#*|}" > "$REGEX_DIR/$slot"
  done < "$GROUPED"
  rm -f "$GROUPED"
fi

NPKGS=$(wc -l < "$WORK" | tr -d ' ')
if (( SHARD_TOTAL > 0 )); then
  echo "test-integration-parallel: shard ${SHARD_IDX}/${SHARD_TOTAL} — $(wc -l < "$ASSIGNED" | tr -d ' ') of $(wc -l < "$DISCOVERY" | tr -d ' ') tests across $NPKGS packages, up to $JOBS concurrent (template db=$TEMPLATE_DB)"
else
  echo "test-integration-parallel: $NPKGS packages, up to $JOBS concurrent (template db=$TEMPLATE_DB)"
fi

# One job = clone an empty db + own a private MinIO bucket, run that package
# against them, drop the clone. In shard mode REGEX_DIR/<idx> holds the
# package's -run slice filter.
run_one() {
  local line="$1" idx="$2" outdir="$3"
  local d="${line%%|*}" rel="${line#*|}" runre=""
  [[ -n "${REGEX_DIR:-}" ]] && runre="$(cat "$REGEX_DIR/$idx")"
  local db="margince_it_p${idx}_$$"
  local log="$outdir/$idx.log"
  local bucket; bucket="$(bucket_for "$idx")"
  # Redis logical db per slot, in 1..15 — db 0 stays reserved for `make dev`.
  local redis_db=$(( 1 + (idx - 1) % 15 ))
  # Coverage args (empty unless COVERDIR is set — shard mode with a manifest
  # dir). -coverpkg=./... attributes cross-package exercise; binary output goes
  # to a per-package pod the CI fan-in merges.
  local cover_pre=() cover_post=() run_args=()
  if [[ -n "${COVERDIR:-}" ]]; then
    mkdir -p "$COVERDIR/$idx"
    cover_pre=(-cover -coverpkg=./... -covermode=atomic)
    cover_post=(-args -test.gocoverdir="$COVERDIR/$idx")
  fi
  [[ -n "$runre" ]] && run_args=(-run "$runre")
  {
    echo "=== integration $d $rel (db=$db bucket=$bucket redis-db=$redis_db${runre:+ sliced}) ==="
    make_clone "$db"
    local st=0
    ( cd "$d" \
        && MARGINCE_ENV=dev \
           MARGINCE_TEST_DSN="$(owner_clone_dsn "$db")" \
           MARGINCE_TEST_APP_DSN="$(app_clone_dsn "$db")" \
           MARGINCE_TEST_BLOBSTORE_BUCKET="$bucket" \
           MARGINCE_TEST_REDIS_DB="$redis_db" \
        go test -p 1 -tags=integration -v -count=1 -timeout="$IT_TIMEOUT" \
          "${cover_pre[@]+"${cover_pre[@]}"}" "${run_args[@]+"${run_args[@]}"}" \
          "$rel" "${cover_post[@]+"${cover_post[@]}"}" ) || st=$?
    if ! drop_clone "$db"; then
      echo "FAIL: clone db $db was not dropped — leaked on the test cluster"
      if [[ "$st" -eq 0 ]]; then st=1; fi
    fi
    echo "EXIT $st"
  } > "$log" 2>&1
}
export -f run_one owner_clone_dsn app_clone_dsn make_clone drop_clone db_admin bucket_for

# Fan out with a bounded worker pool. nl numbers the lines → stable per-job db
# names + logs. The work line rides as a positional arg, not spliced into the
# script, so the shard-mode regex characters never meet the shell.
nl -ba -w1 -s'|' "$WORK" \
  | xargs -P "$JOBS" -I{} bash -c 'line="$1"; idx="${line%%|*}"; rest="${line#*|}"; run_one "$rest" "$idx" "$2"' _ {} "$OUTDIR"

# Aggregate: print every log in package (idx) order, then enforce the teeth.
fail=0
ran=0
for base in $(cd "$OUTDIR" && ls -1 -- *.log 2>/dev/null | sort -n); do
  log="$OUTDIR/$base"
  cat "$log"
  ran=$((ran + 1))
  grep -q "^EXIT 0$" "$log" || fail=1
  if (( SHARD_TOTAL > 0 )); then
    # Top-level results only (subtest lines are indented): "rel|TestName" per
    # the package this log belongs to, for the ran==assigned check below.
    idx="${base%.log}"
    line="$(sed -n "${idx}p" "$WORK")"
    d="${line%%|*}" rest="${line#*|}"
    rel="${rest%%|*}"
    grep -E '^--- (PASS|FAIL): ' "$log" | awk -v p="$d|$rel|" '{print p $3}' >> "$RAN" || true
  fi
done

# Reconcile against discovery: a green run must have executed every package we
# found. NPKGS=0 (a go:build-integration selector regression that matches
# nothing) or a missing log (a worker that never wrote its EXIT line) must read
# as red — otherwise the "0 skips" sentinel below is a false green.
if [[ "$NPKGS_DISCOVERED" -eq 0 ]]; then
  echo "FAIL: no integration packages discovered — the 'go:build integration' selector matched nothing (regression?)"
  fail=1
elif [[ "$ran" -ne "$NPKGS" ]]; then
  echo "FAIL: ran $ran package log(s) but discovered $NPKGS — a worker did not report; treating as red"
  fail=1
fi

# Shard teeth: the tests that actually ran are exactly the assigned slice. A
# discovery/compiler divergence (a test the grep saw but go test didn't run, or
# vice versa) reads as red here, not as a quietly thinner lane.
if (( SHARD_TOTAL > 0 )); then
  LC_ALL=C sort -o "$RAN" "$RAN"
  if ! diff "$ASSIGNED" "$RAN" > /dev/null; then
    echo "FAIL: shard ${SHARD_IDX}/${SHARD_TOTAL} ran a different test set than assigned:"
    diff "$ASSIGNED" "$RAN" | sed -n 's/^< /  assigned but not run: /p; s/^> /  ran but not assigned: /p' || true
    fail=1
  fi
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

# The manifests the fan-in reconciles: full discovery (identical across
# shards), this shard's slice, and what actually ran.
if [[ -n "$SHARD_OUT" ]]; then
  cp "$DISCOVERY" "$SHARD_OUT/discovery.txt"
  cp "$ASSIGNED" "$SHARD_OUT/assigned.txt"
  cp "$RAN" "$SHARD_OUT/ran.txt"
  printf 'shard=%s\ntotal=%s\n' "$SHARD_IDX" "$SHARD_TOTAL" > "$SHARD_OUT/meta.txt"
fi

# Keep the exact success sentinel the gates grep for; the count is informational.
if (( SHARD_TOTAL > 0 )); then
  echo "OK: integration passed with 0 skips (shard ${SHARD_IDX}/${SHARD_TOTAL}: $(wc -l < "$ASSIGNED" | tr -d ' ') tests, $NPKGS packages, parallel)"
else
  echo "OK: integration passed with 0 skips ($NPKGS packages, parallel)"
fi
