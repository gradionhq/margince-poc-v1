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

# The custom-fields runtime-DDL pool runs as the same owner role migrate uses,
# unless the deployment set its own MARGINCE_SCHEMA_DSN.
export MARGINCE_SCHEMA_DSN="${MARGINCE_SCHEMA_DSN:-$MARGINCE_OWNER_DSN}"

echo "entrypoint: applying core + custom migrations (owner role)…"
# No --dsn: cmd/migrate reads MARGINCE_OWNER_DSN itself, and the assertion above
# has already refused to start without it. Passing it as an argument would put the
# owner credential in this container's process list.
margince-migrate up

# First-boot bootstrap admin password (from the environment) → the file the
# mounted margince.yaml's `password_file` references. Written 0600, never baked
# into the image.
#
# It is written ONLY when no organization exists yet. ADR-0061 §2 says bootstrap
# values are consumed exactly once and the `bootstrap_admin` section may be
# deleted once the organization exists; writing this file on every start
# contradicted that, re-materializing a plaintext credential at each boot for the
# life of the installation even though nothing would ever read it again. The
# check runs AFTER migrations because it reads a table migrations create.
#
# The path is fixed, and margince.yaml's `password_file` must name the same one:
# /app/secrets/admin-password, i.e. `secrets/admin-password` relative to the api's
# /app working directory. It used to be overridable via
# MARGINCE_ADMIN_PASSWORD_FILE, which nothing ever set — a deployment that wants a
# different path changes it in margince.yaml and here together, and one knob is
# fewer things to keep in agreement than two.
admin_password_file="/app/secrets/admin-password"
if [ -n "${MARGINCE_ADMIN_PASSWORD:-}" ]; then
    if [ "$(margince-migrate org-exists)" = "true" ]; then
        # Say so rather than silently doing nothing: a supplied credential that is
        # ignored must not look like one that was applied. The operator's next step
        # is to stop supplying it, and nothing else here will tell them that.
        echo "entrypoint: MARGINCE_ADMIN_PASSWORD is set, but this installation already has an organization — the bootstrap credential is not written and would never be read. Unset MARGINCE_ADMIN_PASSWORD; use 'margince-migrate reset-password' to change an existing user's password." >&2
    else
        ( umask 077
          mkdir -p "$(dirname "$admin_password_file")"
          printf '%s' "$MARGINCE_ADMIN_PASSWORD" > "$admin_password_file" )
    fi
fi

echo "entrypoint: starting api…"
exec margince-api
