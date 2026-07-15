#!/usr/bin/env bash
# mcp-inspector.sh — launch the MCP Inspector against cmd/mcp over stdio, wired
# to the running dev stack (`make dev`). This is the one-command form of the
# manual guide's §6.a walkthrough: it builds the stdio server, resolves the same
# app-role DSN the running api connects as, and hands the Inspector a UI-minted
# passport token.
#
# Everything but the token is derived, and the token + DSN flow through the
# environment only (never argv), so a passport bearer credential never lands in
# a host process listing:
#
#   - The passport token is minted in the UI (Settings → AI & autonomy). Pass it
#     as MARGINCE_PASSPORT_TOKEN in .env.local, or on the command line
#     (`make mcp-inspector TOKEN=mgp_…`). No token → fail loudly; a silent empty
#     credential would only surface as an opaque auth error deep in the loop.
#   - The DSN is read from the running stack's own state file (the real database,
#     so an isolated `make dev DEV_SLUG=<slug>` stack just works) rather than
#     re-derived — one source of truth, and it proves the stack is actually up.
#
#   make mcp-inspector TOKEN=mgp_… [DEV_SLUG=<slug>] [WORKSPACE=<slug>]
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# .env.local carries local-only overrides (the BYOK key dev.sh reads, and
# optionally MARGINCE_PASSPORT_TOKEN / OWNER_DSN / APP_DSN) — source it the same
# way dev.sh does so this wrapper honours the same local config.
if [[ -f .env.local ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env.local
  set +a
fi

# Explicit TOKEN wins over a token from the environment / .env.local.
token="${TOKEN:-${MARGINCE_PASSPORT_TOKEN:-}}"
if [[ -z "$token" ]]; then
  echo "FAIL: no passport token. Mint one in the UI (Settings → AI & autonomy)," >&2
  echo "      then: make mcp-inspector TOKEN=mgp_…  (or set MARGINCE_PASSPORT_TOKEN in .env.local)." >&2
  exit 1
fi

slug="${DEV_SLUG:-}"
workspace="${WORKSPACE:-demo-workspace}"

# The running stack records its real database under .tmp/dev/<slug>/env; reading
# it keeps this wrapper correct for both the bare stack and an isolated DEV_SLUG
# one, and its absence is the honest "stack isn't running" signal.
state=".tmp/dev/${slug:-_base}/env"
if [[ ! -f "$state" ]]; then
  echo "FAIL: dev stack not running${slug:+ for DEV_SLUG=$slug} — run 'make dev${slug:+ DEV_SLUG=$slug}' first (no ${state})." >&2
  exit 1
fi
db="$(
  # shellcheck disable=SC1090
  . "$state"
  printf '%s' "${DB:-}"
)"
if [[ -z "$db" ]]; then
  echo "FAIL: ${state} carries no DB entry — is the dev stack healthy? (re-run 'make dev')." >&2
  exit 1
fi

# The same app-role DSN the api connects as (RLS-bound, non-superuser), with the
# database segment swapped to the running stack's database — mirrors dev.sh's
# derivation so an APP_DSN override in .env.local flows through here too.
APP_DSN="${APP_DSN:-postgres://margince_app:margince_app_dev@localhost:55432/margince}"
dsn="${APP_DSN%/*}/${db}"

echo "mcp-inspector: building bin/mcp…"
(cd backend && go build -o ../bin/mcp ./cmd/mcp)

echo "mcp-inspector: launching Inspector → workspace '${workspace}', database '${db}'"
# Token + DSN in the environment only (never argv): a passport is a bearer
# credential and argv is world-readable in a process listing.
export MARGINCE_PASSPORT_TOKEN="$token"
export MARGINCE_DSN="$dsn"
exec npx @modelcontextprotocol/inspector bin/mcp --workspace "$workspace"
