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
# into the image, and ONLY while the installation has no organization: ADR-0061
# §2 consumes bootstrap values exactly once and permits deleting the
# `bootstrap_admin` secret once the organization exists, so a plaintext
# credential at rest past that point protects nothing. The probe runs AFTER
# migrations because it reads a table migrations create.
#
# The path is fixed, and margince.yaml's `password_file` must name the same one:
# /app/secrets/admin-password, i.e. `secrets/admin-password` relative to the api's
# /app working directory. It used to be overridable via
# MARGINCE_ADMIN_PASSWORD_FILE, which nothing ever set — a deployment that wants a
# different path changes it in margince.yaml and here together, and one knob is
# fewer things to keep in agreement than two.
admin_password_file="/app/secrets/admin-password"
# Either condition is worth a probe. The variable being set may mean a
# credential must be WRITTEN; the file already existing may mean a spent one
# must be RETIRED. Gating on the variable alone would strand a file left by an
# earlier boot the moment an operator follows the advice below and unsets it —
# the exact sequence this block asks for.
if [ -n "${MARGINCE_ADMIN_PASSWORD:-}" ] || [ -e "$admin_password_file" ]; then
    # Capture the probe's own exit status rather than testing its output
    # directly: inside an `if` condition a command substitution is exempt from
    # `set -e`, so a failed probe would read as empty, miss the "true" branch,
    # and write the credential onto a provisioned installation — the one
    # outcome this block exists to prevent. `org-exists` prints its answer
    # precisely so a caller can tell "no" from "could not ask"; this is the
    # shape ensure_template in scripts/lib-testdb.sh already uses.
    if ! provisioned="$(margince-migrate org-exists)"; then
        echo "FAIL: could not determine whether this installation already has an organization — fix the error above; a failed probe is not 'unprovisioned', and treating it as one would write a plaintext credential onto a live installation" >&2
        exit 1
    fi
    if [ "$provisioned" = "true" ]; then
        # Say so rather than silently doing nothing: a supplied credential that is
        # ignored must not look like one that was applied. Removing bootstrap_admin
        # from margince.yaml is the action that actually retires it — unsetting only
        # this variable leaves the api reading a file nothing writes.
        if [ -n "${MARGINCE_ADMIN_PASSWORD:-}" ]; then
            echo "entrypoint: MARGINCE_ADMIN_PASSWORD is set, but this installation already has an organization, so the bootstrap credential is neither written nor read. Remove the bootstrap_admin section from margince.yaml and unset MARGINCE_ADMIN_PASSWORD; use 'margince-migrate reset-password' to change an existing user's password." >&2
        fi
        # Any copy left by an earlier boot is retired here. The invariant is that
        # no plaintext bootstrap credential is at rest once the organization
        # exists — not merely that this start did not add one.
        #
        # A failed removal warns and continues rather than refusing to start. The
        # file is spent: nothing reads it, so its presence is a hygiene defect,
        # not an escalation — while a read-only /app/secrets (how a Kubernetes
        # secret projection mounts by default) would turn every start of a
        # healthy installation into an outage. Refusing here would trade the
        # failure this change just removed for another one.
        if [ -e "$admin_password_file" ] && ! rm -f "$admin_password_file"; then
            echo "entrypoint: WARNING could not remove the spent bootstrap credential at $admin_password_file — remove it by hand; it is plaintext and nothing reads it now" >&2
        fi
    elif [ -n "${MARGINCE_ADMIN_PASSWORD:-}" ]; then
        ( umask 077
          mkdir -p "$(dirname "$admin_password_file")"
          printf '%s' "$MARGINCE_ADMIN_PASSWORD" > "$admin_password_file" )
    fi
fi

echo "entrypoint: starting api…"
exec margince-api
