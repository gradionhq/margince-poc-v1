#!/usr/bin/env bash
# Run ONE integration package (optionally a single test) on a throwaway clone db +
# private MinIO bucket + its own Redis logical db (MARGINCE_TEST_REDIS_DB, 15 by
# default — never db 0, which a running `make dev` owns) — the fast inner-loop
# shortcut for iterating on one test without booting the whole parallel lane.
#
#   scripts/test-integration-one.sh DIR [RUN]
#     DIR  repo-root package dir, e.g. backend/internal/compose/integration
#     RUN  optional -run regex, e.g. TestVoiceProfileMutationsRejectAgents
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=scripts/lib-testdb.sh
source "$ROOT/scripts/lib-testdb.sh"

DIR="${1:-}"
RUN="${2:-}"
if [ -z "$DIR" ]; then
  echo "usage: $0 DIR [RUN]   (DIR e.g. backend/internal/compose/integration; RUN e.g. TestFoo)" >&2
  exit 2
fi
# Every integration package lives in the backend module; map the repo-root dir to
# a module-relative package path.
if [ "$DIR" = "backend" ]; then
  rel="."
elif [ "${DIR#backend/}" != "$DIR" ]; then
  rel="./${DIR#backend/}"
else
  echo "FAIL: '$DIR' is not under the backend module" >&2
  exit 2
fi

parse_test_dsn
ensure_template
db="margince_it_one_$$"
make_clone "$db"
# Teardown keeps the test status unless it is the only failure: a clone that
# cannot be dropped is a leaked database and must not report a green run.
trap 'st=$?; if ! drop_clone "$db"; then echo "FAIL: clone db $db was not dropped — leaked on the test cluster" >&2; if [[ "$st" -eq 0 ]]; then st=1; fi; fi; exit "$st"' EXIT

run_flag=()
[ -n "$RUN" ] && run_flag=(-run "$RUN")
echo "test-integration-one: backend $rel ${RUN:+(-run $RUN) }(db=$db)"

( cd backend \
    && MARGINCE_ENV=dev \
       MARGINCE_TEST_DSN="$(owner_clone_dsn "$db")" \
       MARGINCE_TEST_APP_DSN="$(app_clone_dsn "$db")" \
       MARGINCE_TEST_BLOBSTORE_BUCKET="$(bucket_for one)" \
       MARGINCE_TEST_REDIS_DB="${MARGINCE_TEST_REDIS_DB:-15}" \
    go test -p 1 -tags=integration -v -count=1 -timeout=300s "${run_flag[@]+"${run_flag[@]}"}" "$rel" )
