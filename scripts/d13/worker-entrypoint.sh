#!/bin/sh
# D13 worker entrypoint: serve the background role as the APP role. The worker
# runs NO migrations (the api role owns that) — on a cold database it fails its
# dependency probe and k8s restarts it until the api has migrated.
set -eu

if [ -f /.env ]; then
    # shellcheck disable=SC1091
    . /.env
fi

: "${MARGINCE_DSN:?MARGINCE_DSN is required (app role DSN)}"
: "${MARGINCE_PUBLIC_BASE_URL:?MARGINCE_PUBLIC_BASE_URL is required (canonical external base URL)}"

echo "entrypoint: starting worker…"
# --dsn falls back to MARGINCE_DSN (app role), --redis to MARGINCE_REDIS.
exec margince-worker \
    --config /app/config/margince.yaml \
    --ai-routing /app/config/ai-routing.yaml \
    --public-base-url "$MARGINCE_PUBLIC_BASE_URL"
