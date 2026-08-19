#!/usr/bin/env bash
# test-dev-dsn.sh — prove the dev stack resolves its DSNs the way the product
# does, and that doing so did not widen what an isolated stack can reach.
#
# `make dev` passes --dsn explicitly, which OUTRANKS the environment the
# binaries would otherwise read. That is why the resolution has to happen HERE,
# and why it is worth a test: the failure it replaces was a value set in
# .env.local that looked meaningful and did nothing for the whole dev stack.
#
# What must survive honouring MARGINCE_DSN, all three from #1252:
#   1. DEV_SLUG keeps owning the database NAME. A stack that inherited the name
#      from a supplied DSN would sit on slug-derived ports in front of the BASE
#      database — two stacks that look isolated quietly sharing one.
#   2. --fresh still refuses a redirected owner DSN, compared against the
#      EFFECTIVE value rather than the literal default.
#   3. A DSN is never echoed. It carries a password.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
dev="$root/scripts/dev.sh"
failures=0

check() { # want got description
    if [ "$1" = "$2" ]; then
        printf '  ok   %s\n' "$3"
    else
        printf '  FAIL %s\n       want: %s\n       got:  %s\n' "$3" "$1" "$2" >&2
        failures=$((failures + 1))
    fi
}

# The helper under test, lifted from the script rather than reimplemented — a
# copy here would be a second version of production that proves nothing about it.
eval "$(sed -n '/^with_database() {/,/^}$/p' "$dev")"

echo "dev.sh: the database segment is this stack's to name"

check "postgres://u:p@h:15432/margince_dev_x" \
      "$(with_database "postgres://u:p@h:15432/margince" margince_dev_x)" \
      "a supplied DSN keeps its credentials, host and port, and takes the slug's database"

check "postgres://u:p@h:15432/margince_dev_x" \
      "$(with_database "postgres://u:p@h:15432/somebody_elses_db" margince_dev_x)" \
      "the database it named is REPLACED, never inherited"

check "postgres://u:p@h:5432/margince_dev_x?sslmode=require" \
      "$(with_database "postgres://u:p@h:5432/prod?sslmode=require" margince_dev_x)" \
      "a query string survives — MARGINCE_DSN is what a deployment fills in, and sslmode is ordinary there"

check "postgres://u:p@h:5432/margince" \
      "$(with_database "postgres://u:p@h:5432" margince)" \
      "a DSN naming no database still gets one"

check "postgresql://u:p@h:5432/margince_dev_x" \
      "$(with_database "postgresql://u:p@h:5432/prod" margince_dev_x)" \
      "postgresql:// works too — libpq accepts both spellings, so refusing one would be this script's own rule"

# Anything that cannot be redirected is refused rather than rewritten: libpq's
# key/value form has no database segment to replace, and another scheme would be
# pointed at this stack's database only to fail at the client. Both are the same
# "fails later, further from the cause" this function exists to avoid.
for bad in "host=h port=5432 dbname=prod user=u" "mysql://u:p@h:3306/prod"; do
    status=0
    out="$(with_database "$bad" margince_dev_x 2>&1)" || status=$?
    check nonzero "$([ "$status" -ne 0 ] && echo nonzero || echo zero)" \
          "refused rather than rewritten: ${bad%%:*}…"
    check yes "$(grep -q "must be a postgres:// or postgresql:// URL" <<<"$out" && echo yes || echo no)" \
          "and says what shape it needs: ${bad%%:*}…"
done

echo "dev.sh: resolution order, and what it must not leak"

# The order is asserted against the script's own text: running `make dev` here
# would need Docker, a database and a free :8080, and would sweep every other
# stack on the machine — including a colleague's.
check yes "$(grep -q 'OWNER_DSN="${OWNER_DSN:-${MARGINCE_OWNER_DSN:-$COMPOSE_OWNER_DSN}}"' "$dev" && echo yes || echo no)" \
      "owner: an explicit argument, else MARGINCE_OWNER_DSN, else the compose default"
check yes "$(grep -q 'APP_DSN="${APP_DSN:-${MARGINCE_DSN:-$COMPOSE_APP_DSN}}"' "$dev" && echo yes || echo no)" \
      "app: an explicit argument, else MARGINCE_DSN, else the compose default"

# The interlock refuses when the owner DSN is not the compose Postgres, because
# --fresh drops through the compose container while migrations follow the DSN.
# Comparing against a literal would let the default drift away from the value it
# is checked against, disabling the check silently.
check yes "$(grep -q '"\$OWNER_DSN" != "\$COMPOSE_OWNER_DSN"' "$dev" && echo yes || echo no)" \
      "--fresh compares the EFFECTIVE owner DSN against the compose default, not a literal"

# Every destructive path goes through the compose container, never the DSN, so
# honouring MARGINCE_DSN cannot widen what a drop can reach.
check yes "$(sed -n '/^psql_owner() {/,/^}$/p' "$dev" | grep -q 'docker compose' && echo yes || echo no)" \
      "psql_owner talks to the compose container, so a redirected DSN drops nothing new"

# A DSN carries a password. echo/printf must never be handed one.
leaks="$(grep -nE '^[^#]*(echo|printf)[^|]*\$(dev_owner_url|dev_app_url|OWNER_DSN|APP_DSN|MARGINCE_DSN|MARGINCE_OWNER_DSN)' "$dev" || true)"
check "" "$leaks" "no DSN is ever echoed"

if [ "$failures" -ne 0 ]; then
    echo "FAIL: $failures dev-stack DSN expectation(s) not met" >&2
    exit 1
fi
echo "OK: the dev stack resolves DSNs through the product's own names, and still owns the database"
