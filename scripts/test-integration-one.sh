#!/usr/bin/env bash
# Run ONE integration package (optionally a single test) on a throwaway clone db +
# private MinIO bucket — the fast inner-loop shortcut for iterating on one test
# without booting the whole parallel lane.
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
trap 'drop_clone "$db"' EXIT

run_flag=()
[ -n "$RUN" ] && run_flag=(-run "$RUN")
echo "test-integration-one: backend $rel ${RUN:+(-run $RUN) }(db=$db)"

( cd backend \
    && MARGINCE_ENV=dev \
       MARGINCE_TEST_DSN="$(owner_clone_dsn "$db")" \
       MARGINCE_TEST_APP_DSN="$(app_clone_dsn "$db")" \
       MARGINCE_TEST_BLOBSTORE_BUCKET="$(bucket_for one)" \
    go test -p 1 -tags=integration -v -count=1 -timeout=300s "${run_flag[@]+"${run_flag[@]}"}" "$rel" )
