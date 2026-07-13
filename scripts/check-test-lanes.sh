#!/usr/bin/env bash
# Test-lane separation (ported from the foundation skeleton).
#
# A unit test (one WITHOUT a `//go:build integration` or `//go:build livesmoke`
# tag, so it runs under `make test`) must never open a REAL Postgres/Redis
# connection. Anything that needs real infrastructure belongs in the
# integration lane. This keeps `make test` hermetic and kills the "DB test
# that silently degrades in the unit lane" anti-pattern.
#
# Fakes are fine and NOT flagged: in-memory fake sql drivers carry none of
# the markers below. If a miniredis-style fake (which does dial via
# redis.NewClient) ever joins the unit lane, narrow that marker then —
# deliberately, in this file — rather than pre-weakening the gate today.
set -euo pipefail
cd "$(dirname "$0")/.."

# Markers that only a real connection uses. MARGINCE_TEST_* are the env vars
# the integration harness reads (backend/Makefile exports them from db-up's
# port contract); a unit test reaching for them is in the wrong lane.
real='sql\.Open\("(postgres|pgx)"|pgxpool\.New|pgx\.Connect|os\.Getenv\("MARGINCE_TEST_(DSN|APP_DSN|REDIS)"\)|redis\.ParseURL|redis\.New(Universal)?Client'

violations=0
while IFS= read -r f; do
  # Skip files already in a non-unit lane. Build constraints sit anywhere
  # above the package clause, so scan up to it instead of a fixed head.
  if sed -n '/^package /q;p' "$f" | grep -qE '^//go:build .*(integration|livesmoke)'; then
    continue
  fi
  if grep -Eq "$real" "$f"; then
    echo "VIOLATION (unit test opens real infra — add //go:build integration, or fake the boundary): $f"
    grep -nE "$real" "$f" | sed 's/^/    /'
    violations=1
  fi
# Search roots: every hand-written Go tree. cli/craft is vendored verbatim
# from the foundation (hash-pinned); its lane discipline is upstream's job.
done < <(find backend -name '*_test.go' 2>/dev/null | sort)

if [ "$violations" -ne 0 ]; then
  echo "FAIL: test-lanes — real-infra tests must carry //go:build integration."
  exit 1
fi
echo "OK: test-lanes — no unit test opens a real DB/Redis"
