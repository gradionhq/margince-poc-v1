#!/usr/bin/env bash
# One-command local stack: HTTPS front door (:8080) → api (:8081) + Vite (:5173),
# so the SPA runs in a real browser with a single Secure-cookie origin — no curl.
#
# Kills the two dev gotchas recorded in STATUS.md / memory margince-local-run:
#   1. MARGINCE_ENV=dev so the X-Workspace-Slug header is trusted.
#   2. TLS origin so the Secure session cookie survives.
# The Anthropic BYOK key (for the /coldstart read-back) is read from the
# gitignored .env.local and injected into a scratch routing file — the routing
# YAML parser does NOT expand ${ENV}, so api_key must be a literal.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PG_PORT="${PG_PORT:-55432}"
# Local-only throwaway credentials for the dockerized dev database — the same
# well-known roles scripts/db-init.sql seeds and CI uses. Overridable via env;
# never a production secret. NOSONAR: not a leaked credential.
OWNER_DSN="${OWNER_DSN:-postgres://margince_owner:dev@localhost:${PG_PORT}/margince}"    # NOSONAR
APP_DSN="${APP_DSN:-postgres://margince_app:margince_app_dev@localhost:${PG_PORT}/margince}"    # NOSONAR

SCRATCH="$(mktemp -d)"
ROUTING="$SCRATCH/ai-routing.yaml"

cleanup() {
  echo
  echo "dev: shutting down…"
  # Kill the whole process group's children we started.
  for pid in "${API_PID:-}" "${DOOR_PID:-}" "${VITE_PID:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  rm -rf "$SCRATCH"
}
trap cleanup EXIT INT TERM

# --- AI key: real model powers the /coldstart read-back happy path ----------
AI_FLAG=(--ai-fake)
if [ -f .env.local ]; then
  set -a; . ./.env.local; set +a
fi
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  # Inject the literal key onto every anthropic tier binding.
  sed "s#provider: anthropic, model: \([^ }]*\)#provider: anthropic, model: \1, api_key: ${ANTHROPIC_API_KEY}#g" \
    backend/ai-routing.yaml > "$ROUTING"
  AI_FLAG=(--ai-routing "$ROUTING")
  echo "dev: using real Anthropic model for the read-back (key from .env.local)"
else
  echo "dev: no ANTHROPIC_API_KEY in .env.local — coldstart runs on the offline fake (read-back will 422)"
fi

# --- database ---------------------------------------------------------------
echo "dev: starting Postgres + Redis…"
make db-up >/dev/null
echo "dev: applying migrations…"
( cd backend && go run ./cmd/migrate up --dsn "$OWNER_DSN" >/dev/null )

# --- api (:8081), front door (:8080), vite (:5173) --------------------------
echo "dev: starting api on :8081…"
( cd backend && MARGINCE_ENV=dev go run ./cmd/api --dsn "$APP_DSN" --addr :8081 "${AI_FLAG[@]}" ) &
API_PID=$!

echo "dev: starting HTTPS front door on :8080…"
( cd dev && go run ./frontdoor ) &
DOOR_PID=$!

echo "dev: starting Vite dev server on :5173…"
( cd frontend && MARGINCE_DEV_TLS=1 pnpm dev --port 5173 ) &
VITE_PID=$!

echo
echo "  ┌────────────────────────────────────────────────────────┐"
echo "  │  Margince dev stack is up.                              │"
echo "  │  Open  https://localhost:8080  (accept the cert once).  │"
echo "  └────────────────────────────────────────────────────────┘"
echo
wait
