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
#              resets the data between tests — see internal/platform/testdb).
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
# package's *_test.go files) so it costs no compile; files under the known
# opt-in lane tags (e2e_llm, livesmoke, voicelive) are skipped exactly as the compiler
# skips them, and any other constraint — an expression, or a lone tag the
# allowlist does not know (a satisfied built-in like linux or cgo would
# compile but never be sliced) — fails discovery loudly instead of being
# silently mis-sliced. Shard teeth on top of
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

# Candidate packages: every directory in the module that holds test files. Which
# of them belong to THIS lane is decided by the build-constraint scan in
# discovery below, never by grepping for the tag's spelling: a header comment may
# name `go:build integration` while pointing at the DB-backed sibling that owns
# the other half of a proof, and a package whose tagged files declare no Test
# function has nothing for this lane to run. Deciding membership here, from the
# text, is what once handed a Postgres clone to packages with no tagged test.
CANDIDATES="$(mktemp)"
for d in "${GO_DIRS[@]}"; do
  [[ -d "$d" ]] || continue
  find "$d" -type f -name '*_test.go' -print0 \
    | xargs -0 -n1 dirname \
    | LC_ALL=C sort -u \
    | while IFS= read -r pkgdir; do
        rel="./${pkgdir#"$d"/}"; [[ "$pkgdir" = "$d" ]] && rel="."
        echo "$d|$rel"
      done
done > "$CANDIDATES"

CONSTRAINT_SCOPE="$(mktemp)" LANE_PKGS="$(mktemp)" WORK="$(mktemp)"
DISCOVERY="$(mktemp)" ASSIGNED="$(mktemp)" RAN="$(mktemp)" UNTAGGED="$(mktemp)"
TIMING="$(mktemp)"
OUTDIR="$(mktemp -d)"
trap 'rm -f "$CANDIDATES" "$CONSTRAINT_SCOPE" "$LANE_PKGS" "$WORK" "$DISCOVERY" "$ASSIGNED" "$RAN" "$UNTAGGED" "$TIMING" ${GROUPED:+"$GROUPED"}; rm -rf "$OUTDIR" ${REGEX_DIR:+"$REGEX_DIR"}' EXIT

# The ONE spelling of "which build constraints does this file declare": the
# region above the `package` clause, one expression per line. Both passes below
# read it and judge it differently — the ownership pass asks only whether
# `integration` appears, discovery holds the expression to a strict allowlist —
# and a lane that extracted it twice could drift into admitting packages whose
# files it then refuses to tag.
#
# scripts/check-test-lanes.sh answers the opposite direction (no unit test opens
# real infra) and still matches the constraint line with a substring test rather
# than tokenizing it. That is latent, not live: only bare `integration` exists in
# the tree. Unifying all three call sites needs a shared shell library neither
# script has today.
constraint_exprs() {
  sed -n '/^package /q;p' "$1" | sed -nE 's|^//go:build ||p; s|^// \+build ||p'
}

# Which packages does the strict constraint check below apply to? A package
# qualifies when at least one of its test files names `integration` in its
# build-constraint region. This is NOT where lane membership is decided — that is
# derived from discovery further down, from the tests actually found.
#
# The pass exists to bound the strict constraint check. That check fails
# the whole lane on a constraint it cannot decide, because in a package this lane
# RUNS, a satisfied built-in tag (linux, amd64, cgo, …) would compile under
# `go test` yet land in no slice — a silently thinner lane. In a package the lane
# never runs, the same tag is simply none of its business, and failing on it would
# stop the lane over a file it was never going to execute.
while IFS='|' read -r d rel; do
  pkgdir="$d/${rel#./}"; [[ "$rel" = "." ]] && pkgdir="$d"
  for f in "$pkgdir"/*_test.go; do
    [[ -e "$f" ]] || continue
    if constraint_exprs "$f" | grep -qw 'integration'; then
      echo "$d|$rel"
      break
    fi
  done
done < "$CANDIDATES" | LC_ALL=C sort -u > "$CONSTRAINT_SCOPE"

# Static discovery: every top-level Test function of every integration-TAGGED
# file, as "module_dir|rel|TestName". TestMain is a fixture, not a test;
# a lowercase rune after "Test" makes a plain function, not a test — both
# match `go test`'s own rules, and the ran==assigned teeth below catch any
# future divergence between this grep and the compiler's view.
#
# Untagged files are excluded here, and that exclusion is the whole point:
# `-tags=integration` is ADDITIVE, so `go test -tags=integration <pkg>` compiles
# a package's untagged test files too. Without this the lane re-runs the whole
# unit suite against real infrastructure, proving nothing `make check` had not
# already proved — and the counts it reports below say how much that is on the
# tree in front of it, rather than a number frozen into a comment.
while IFS='|' read -r d rel; do
  pkgdir="$d/${rel#./}"; [[ "$rel" = "." ]] && pkgdir="$d"
  for f in "$pkgdir"/*_test.go; do
    [[ -e "$f" ]] || continue
    # Build-constrained files: only allowlisted lone tags are statically
    # decidable — `integration` is in this lane's build, the manual opt-in
    # lanes (e2e_llm, livesmoke, voicelive) are not, so those files' tests are skipped
    # exactly as the compiler skips them. Anything else — an expression
    # with operators, or a lone tag the allowlist does not know — fails
    # loudly (on stderr — stdout here feeds the sort) rather than guess:
    # a satisfied built-in tag (linux, amd64, cgo, …) would compile under
    # `go test` yet land in no slice, and the ran==assigned teeth cannot
    # see a test that discovery itself never counted.
    #
    # The constraint region is everything ABOVE the `package` clause; the same
    # scan scripts/check-test-lanes.sh uses. A tag named below it is prose.
    skip_file=0 tagged=0
    while IFS= read -r expr; do
      [[ -n "$expr" ]] || continue
      case "$expr" in
        integration) tagged=1 ;;
        e2e_llm|livesmoke|voicelive) skip_file=1 ;;
        *)
          echo "FAIL: $f carries build constraint '$expr' — not one the static shard discovery knows" >&2
          echo "  teach the allowlist in scripts/test-integration-parallel.sh or simplify the constraint" >&2
          exit 1
          ;;
      esac
    done < <(constraint_exprs "$f")
    # An unconstrained file is a unit-lane file. Record what it holds, keyed by
    # package, so the lane can report what it left to `make check` rather than
    # silently narrowing — a quieter lane and a broken selector look identical.
    # Keyed, not summed, because only packages that DO own a tagged test were
    # ever running these: counting the rest would overstate the exclusion.
    if (( ! tagged && ! skip_file )); then
      printf '%s|%s|%s\n' "$d" "$rel" "$({ grep -cE '^func Test[A-Za-z0-9_]*\(' "$f" || true; })" >> "$UNTAGGED"
      continue
    fi
    (( skip_file )) && continue
    # An anchored -run union filters Fuzz and Example functions too, and both are
    # things `go test` would otherwise run and report. Discovery enumerates only
    # `func Test…`, so one of those in a tagged file would sit in neither ASSIGNED
    # nor RAN — and because the teeth compare two sets derived from THIS grep, the
    # omission would read as green. Before the union covered unsharded runs,
    # `go test` was the independent authority that caught it; nothing is now, so
    # the case fails loudly instead of being silently dropped.
    if grep -qE '^func (Fuzz|Example)[A-Za-z0-9_]*\(' "$f"; then
      echo "FAIL: $f declares a Fuzz or Example function in the integration lane" >&2
      echo "  the anchored -run union would filter it out and the ran==assigned teeth cannot see it;" >&2
      echo "  teach discovery to enumerate it, or move it to the unit lane" >&2
      exit 1
    fi
    # `|| true`: a helper-only test file with no Test functions is fine.
    { grep -hE '^func Test[A-Za-z0-9_]*\(' "$f" || true; } \
      | sed -E 's/^func (Test[A-Za-z0-9_]*)\(.*/\1/' \
      | while IFS= read -r name; do
          [[ "$name" = "TestMain" ]] && continue
          [[ "$name" != "Test" && "${name:4:1}" =~ [a-z] ]] && continue
          echo "$d|$rel|$name"
        done
  done
done < "$CONSTRAINT_SCOPE" | LC_ALL=C sort > "$DISCOVERY"

NTESTS=$(wc -l < "$DISCOVERY" | tr -d ' ')

# The lane's packages are exactly those that produced a tagged test. Deriving
# the list rather than maintaining it is what stops a package with no tagged
# test being handed a clone, and it cannot drift from what discovery found.
cut -d'|' -f1,2 "$DISCOVERY" | LC_ALL=C sort -u > "$LANE_PKGS"
NPKGS_DISCOVERED=$(wc -l < "$LANE_PKGS" | tr -d ' ')

# Untagged tests this lane no longer schedules — counted only inside packages it
# actually visits, since a package with no tagged test was never its business.
NUNTAGGED=$(awk -F'|' '
  NR == FNR { lane[$1 "|" $2] = 1; next }
  ($1 "|" $2) in lane { n += $3 }
  END { print n + 0 }
' "$LANE_PKGS" "$UNTAGGED")

if (( SHARD_TOTAL > 0 )); then
  if (( NTESTS < SHARD_TOTAL )); then
    echo "FAIL: discovered only $NTESTS integration tests for $SHARD_TOTAL shards — the discovery is broken or the shard count is absurd"
    exit 1
  fi
  # Deterministic round-robin over the sorted list: line i goes to shard
  # ((i-1) % N) + 1. Every shard computes the same list from the same commit,
  # so the slices are complete and disjoint by construction — and the fan-in
  # verifies exactly that instead of trusting it.
  awk -v k="$SHARD_IDX" -v n="$SHARD_TOTAL" 'NR % n == k % n' "$DISCOVERY" > "$ASSIGNED"
else
  # Unsharded: the whole lane is this run's slice. Naming it explicitly is what
  # lets the -run union and the ran==assigned teeth below serve both modes.
  cp "$DISCOVERY" "$ASSIGNED"
fi

# Each package's anchored -run union of its assigned test names goes to a
# per-slot FILE, not onto the work line — hundreds of test names make a regex
# far past what xargs -I can carry. The union is also what confines the run to
# tagged tests, so it is built in BOTH modes, not only when sharding.
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

NPKGS=$(wc -l < "$WORK" | tr -d ' ')
# Say what was left to the unit lane. An unreported exclusion and a broken
# selector produce the same silence, and this lane's whole contract is that a
# thinner run must never read as a passing one.
echo "test-integration-parallel: $NTESTS tagged tests in $NPKGS_DISCOVERED packages; skipped $NUNTAGGED untagged (they run in \`make check\`)"
if (( SHARD_TOTAL > 0 )); then
  echo "test-integration-parallel: shard ${SHARD_IDX}/${SHARD_TOTAL} — $(wc -l < "$ASSIGNED" | tr -d ' ') of $NTESTS tests across $NPKGS packages, up to $JOBS concurrent (template db=$TEMPLATE_DB)"
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
  # Top-level results only (subtest lines are indented): "rel|TestName" per
  # the package this log belongs to, for the ran==assigned check below.
  #
  # SKIP counts as run. A skipped test still executed, and this lane forbids
  # skipping outright a few lines down — omitting it here would report it as
  # "assigned but not run" instead, which names the reconciliation rather than
  # the skip and sends the reader looking for a dead worker.
  idx="${base%.log}"
  line="$(sed -n "${idx}p" "$WORK")"
  d="${line%%|*}" rest="${line#*|}"
  rel="${rest%%|*}"
  grep -E '^--- (PASS|FAIL|SKIP): ' "$log" | awk -v p="$d|$rel|" '{print p $3}' >> "$RAN" || true
  # Cost, taken from `go test`'s own trailing "ok <pkg> <n>s" — millisecond
  # precision already in the log, and it excludes the clone provisioning around
  # it, so what it prices is the tests themselves.
  # The duration field is matched, not the line end: a covered shard appends
  # "coverage: NN.N% of statements" after it, and anchoring on s$ would have
  # priced every CI package at nothing — shard mode always sets COVERDIR.
  awk -v rel="$rel" '
    /^(ok|FAIL)[ \t]+[^ \t]+[ \t]+[0-9.]+s([ \t]|$)/ { secs = $3; sub(/s$/, "", secs) }
    /^--- (PASS|FAIL|SKIP): / { n++ }
    END { if (secs != "") printf "%s|%s|%d\n", rel, secs, n + 0 }
  ' "$log" >> "$TIMING" || true
done

# Per-package cost. ADVISORY — printed, never enforced. A raw wall-time ceiling
# is the wrong shape: this package count grows, and a ceiling that healthy growth
# re-crosses gets bumped until nobody reads it. Amortized ms/test is
# scale-invariant, which is what makes it a signal — a per-test cost that is
# priced per table rather than per row trips it at any test count, while 300 clean
# new tests do not. Enforcing a number measured on one machine is the same
# unverified claim this lane keeps learning about, so the ceiling waits for a CI
# baseline.
if [[ -s "$TIMING" ]]; then
  # Ordered by ms/test, not by total, so an anomaly surfaces instead of merely
  # the biggest package: a small suite paying a per-test schema migration reads an
  # order of magnitude above the lane norm, and that is the shape worth seeing
  # first.
  echo "test-integration-parallel: per-package cost (advisory)"
  awk -F'|' '{ printf "%s|%s|%s|%.4f\n", $1, $2, $3, ($3 ? $2 * 1000 / $3 : 0) }' "$TIMING" \
    | LC_ALL=C sort -t'|' -k4 -rn \
    | awk -F'|' '
        { total += $2; tests += $3
          printf "  %-44s %8.2fs  %5d tests  %7.1f ms/test\n", $1, $2, $3, $4 }
        END { if (tests) printf "  %-44s %8.2fs  %5d tests  %7.1f ms/test\n", "TOTAL (sum of packages)", total, tests, total * 1000 / tests }
      '
fi

# Reconcile against discovery: a green run must have executed every package we
# found. NPKGS=0 (a constraint-scan regression that admits no file) or a missing
# log (a worker that never wrote its EXIT line) must read as red — otherwise the
# "0 skips" sentinel below is a false green.
if [[ "$NPKGS_DISCOVERED" -eq 0 ]]; then
  echo "FAIL: no integration packages discovered — the build-constraint scan admitted no tagged file (regression?)"
  fail=1
elif [[ "$ran" -ne "$NPKGS" ]]; then
  # $NPKGS is THIS run's package count — the shard's slice, not the lane total —
  # so it is the number a missing worker has to be counted against.
  echo "FAIL: ran $ran package log(s) but this run has $NPKGS package(s) — a worker did not report; treating as red"
  fail=1
fi

# The teeth: the tests that actually ran are exactly the assigned slice. A
# discovery/compiler divergence (a test the grep saw but go test didn't run, or
# vice versa) reads as red here, not as a quietly thinner lane. These bind in
# BOTH modes — unsharded runs are confined by the same -run union, so a test
# discovery failed to see would otherwise be silently dropped rather than run.
LC_ALL=C sort -o "$RAN" "$RAN"
if ! diff "$ASSIGNED" "$RAN" > /dev/null; then
  if (( SHARD_TOTAL > 0 )); then
    echo "FAIL: shard ${SHARD_IDX}/${SHARD_TOTAL} ran a different test set than assigned:"
  else
    echo "FAIL: the lane ran a different test set than discovery assigned:"
  fi
  diff "$ASSIGNED" "$RAN" | sed -n 's/^< /  assigned but not run: /p; s/^> /  ran but not assigned: /p' || true
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

# The manifests the fan-in reconciles: full discovery (identical across
# shards), this shard's slice, and what actually ran.
if [[ -n "$SHARD_OUT" ]]; then
  cp "$DISCOVERY" "$SHARD_OUT/discovery.txt"
  cp "$ASSIGNED" "$SHARD_OUT/assigned.txt"
  cp "$RAN" "$SHARD_OUT/ran.txt"
  # "rel|seconds|tests" per package, so cost can be tracked across runs rather
  # than re-derived by eye from a scrolled log.
  cp "$TIMING" "$SHARD_OUT/timing.txt"
  printf 'shard=%s\ntotal=%s\n' "$SHARD_IDX" "$SHARD_TOTAL" > "$SHARD_OUT/meta.txt"
fi

# Keep the exact success sentinel the gates grep for; the count is informational.
if (( SHARD_TOTAL > 0 )); then
  echo "OK: integration passed with 0 skips (shard ${SHARD_IDX}/${SHARD_TOTAL}: $(wc -l < "$ASSIGNED" | tr -d ' ') tests, $NPKGS packages, parallel)"
else
  echo "OK: integration passed with 0 skips ($NPKGS packages, parallel)"
fi
