#!/bin/sh
# Container entrypoint for cmd/api: apply migrations as the OWNER role, then serve
# as the APP role. Deployment-target-agnostic — every setting is read from the
# environment (the MARGINCE_* vars in docs/reference/configuration.md); the api
# resolves --config/--dsn/--redis/--ai-routing/--public-base-url from their env
# fallbacks, so no flags are needed here.
#
# The two DB roles + the database are created ONCE, out of band, by
# scripts/deploy/db-bootstrap.sql — margince enforces tenant isolation with
# FORCE ROW LEVEL SECURITY, which only a superuser bypasses, so the app must
# never connect as one and this container holds no superuser credential.
set -eu

# Optional convenience: source an env file if one is mounted at /.env. The
# primary path is real environment variables set by the orchestrator. `set -a`
# auto-exports every var the file defines so the exec'd binary actually inherits
# them (a bare `KEY=value` line otherwise sets only a shell var, not the child's
# environment).
if [ -f /.env ]; then
    set -a
    # shellcheck disable=SC1091
    . /.env
    set +a
fi

: "${MARGINCE_OWNER_DSN:?MARGINCE_OWNER_DSN is required (owner role DSN for migrations + custom-fields DDL)}"
: "${MARGINCE_DSN:?MARGINCE_DSN is required (app role DSN the api serves under, via the --dsn env fallback)}"

# First-boot bootstrap admin password (from the environment) → the file the
# mounted margince.yaml's `password_file` references. Written 0600, never baked
# into the image; only consumed on the first boot against an empty database.
# The path must match your margince.yaml's `password_file` (default
# /app/secrets/admin-password, i.e. `secrets/admin-password` relative to /app);
# override both together with MARGINCE_ADMIN_PASSWORD_FILE.
admin_password_file="${MARGINCE_ADMIN_PASSWORD_FILE:-/app/secrets/admin-password}"
if [ -n "${MARGINCE_ADMIN_PASSWORD:-}" ]; then
    ( umask 077
      mkdir -p "$(dirname "$admin_password_file")"
      printf '%s' "$MARGINCE_ADMIN_PASSWORD" > "$admin_password_file" )
fi

# The custom-fields runtime-DDL pool runs as the same owner role migrate uses,
# unless the deployment set its own MARGINCE_SCHEMA_DSN.
export MARGINCE_SCHEMA_DSN="${MARGINCE_SCHEMA_DSN:-$MARGINCE_OWNER_DSN}"

echo "entrypoint: applying core + custom migrations (owner role)…"
margince-migrate up --dsn "$MARGINCE_OWNER_DSN"

echo "entrypoint: starting api…"
exec margince-api
