#!/bin/sh
# Container entrypoint for cmd/worker: serve the background role as the APP role.
# Deployment-target-agnostic — every setting is read from the environment (the
# MARGINCE_* vars in docs/reference/configuration.md); the worker resolves
# --config/--dsn/--redis/--ai-routing/--public-base-url from their env fallbacks.
#
# The worker runs NO migrations (the api role owns that) — on a cold database it
# fails its dependency probe and the orchestrator restarts it until the api has
# migrated.
set -eu

# Optional convenience: source an env file if one is mounted at /.env. `set -a`
# auto-exports every var so the exec'd binary inherits them (a bare `KEY=value`
# line otherwise sets only a shell var, not the child's environment).
if [ -f /.env ]; then
    set -a
    # shellcheck disable=SC1091
    . /.env
    set +a
fi

: "${MARGINCE_DSN:?MARGINCE_DSN is required (app role DSN, via the --dsn env fallback)}"

echo "entrypoint: starting worker…"
exec margince-worker
