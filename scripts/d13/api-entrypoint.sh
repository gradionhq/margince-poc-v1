#!/bin/sh
# D13 api entrypoint: apply migrations as the OWNER role, then serve as the APP
# role. The two roles + the database are created ONCE, out of band, by
# scripts/d13/db-bootstrap.sql (the app pod deliberately holds no superuser
# credential). See docs/reference/d13-deployment.md.
set -eu

# The Vault-managed env file is baked into the image and also injected by D13 at
# runtime; sourcing it makes a plain `docker run --env-file .env` behave the same.
if [ -f /.env ]; then
    # shellcheck disable=SC1091
    . /.env
fi

: "${MARGINCE_OWNER_DSN:?MARGINCE_OWNER_DSN is required (owner role DSN for migrations + custom-fields DDL)}"
: "${MARGINCE_DSN:?MARGINCE_DSN is required (app role DSN the api serves under)}"
: "${MARGINCE_PUBLIC_BASE_URL:?MARGINCE_PUBLIC_BASE_URL is required (canonical external base URL)}"

# The bootstrap admin password (first-boot only) lands in the file margince.yaml
# references — written 0600, never baked into the image.
if [ -n "${MARGINCE_ADMIN_PASSWORD:-}" ]; then
    ( umask 077; printf '%s' "$MARGINCE_ADMIN_PASSWORD" > /app/secrets/admin-password )
fi

# The custom-fields runtime-DDL pool runs as the same owner role migrate uses.
export MARGINCE_SCHEMA_DSN="$MARGINCE_OWNER_DSN"

echo "entrypoint: applying core + custom migrations (owner role)…"
margince-migrate up --dsn "$MARGINCE_OWNER_DSN"

echo "entrypoint: starting api…"
# --dsn falls back to MARGINCE_DSN (app role), --schema-dsn to MARGINCE_SCHEMA_DSN,
# --redis to MARGINCE_REDIS.
exec margince-api \
    --config /app/config/margince.yaml \
    --ai-routing /app/config/ai-routing.yaml \
    --public-base-url "$MARGINCE_PUBLIC_BASE_URL"
