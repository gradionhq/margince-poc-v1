#!/usr/bin/env bash
# One-command local dev stack on the ONE shared infra: Postgres + Redis, the api
# (cmd/api), and the Vite dev server — so the SPA runs in a real browser against
# a live api. Bare `make dev` uses the shared `margince` database on :8080/:5173;
# `make dev DEV_SLUG=<slug>` gives an isolated `margince_dev_<slug>` database on
# slug-derived ports, so two worktrees can run concurrently without colliding on
# the database or the host ports.
#
# MARGINCE_ENV=dev is set so the api trusts the X-Workspace-Slug header the seed
# and FE send. localhost is a browser secure-context, so the Secure session
# cookie survives over plain http — no TLS front door needed.
#
# BYOK: if .env.local sets ANTHROPIC_API_KEY it is injected as a literal api_key
# onto the anthropic tiers of a scratch ai-routing copy (the routing parser does
# NOT expand ${ENV}), so the cold-start read-back runs the real model; otherwise
# the offline fake (--ai-fake) drives it.
#
# Credentials are NOT hardcoded: the connection URLs derive from OWNER_DSN /
# APP_DSN (this repo's dev role DSNs; overridable), so this script carries no
# secret literal beyond the shared dev defaults.
#
#   scripts/dev.sh up   [slug]            # spin infra + db + api + FE
#   scripts/dev.sh stop [slug] [--drop]   # stop servers; --drop also drops the db
set -euo pipefail
# Runtime state under .tmp/dev/ includes the scratch ai-routing.yaml with the
# injected BYOK key — keep everything this script writes owner-only.
umask 077

cmd="${1:-}"
slug="${2:-}"
drop=0
[[ "${3:-}" == "--drop" ]] && drop=1

cd "$(git rev-parse --show-toplevel)"

# This repo's dev connection surface (overridable). OWNER_DSN runs migrations;
# APP_DSN is the non-superuser role the api connects as (RLS binds it).
OWNER_DSN="${OWNER_DSN:-postgres://margince_owner:dev@localhost:55432/margince}"
APP_DSN="${APP_DSN:-postgres://margince_app:margince_app_dev@localhost:55432/margince}"
REDIS_PORT="${REDIS_PORT:-56379}"
# The compose MinIO backs the blobstore seam (attachments); minioadmin is the
# well-known throwaway dev credential the compose stack already ships, never a
# production secret.
MINIO_PORT="${MINIO_PORT:-59000}"

# Bare `make dev` runs the shared `margince` database on the base ports, so it
# stays coherent with `make migrate` / `seed-dev` / `verify-boot`. A DEV_SLUG
# gives an isolated database on deterministically derived ports (same slug →
# same db + ports, so a resume reuses the existing migrated+seeded data).
if [[ -z "$slug" ]]; then
  label="dev"
  db="margince"
  hash=0
else
  # The slug flows into a filesystem path and a CREATE DATABASE identifier —
  # keep it to a safe charset so it can neither traverse paths nor break SQL.
  if ! [[ "$slug" =~ ^[a-z0-9_-]+$ ]]; then
    echo "FAIL: DEV_SLUG must match ^[a-z0-9_-]+$ (got '$slug')" >&2
    exit 1
  fi
  label="dev '$slug'"
  db="margince_dev_${slug}"
  hash=$(printf '%s' "$slug" | cksum | awk '{print $1 % 1000}')
fi
api_port=$(( 8080 + hash ))
fe_port=$(( 5173 + hash ))

# Swap the database segment of each base DSN — no credential literal here.
owner_prefix="${OWNER_DSN%/*}"          # scheme://user:pass@host:port
app_prefix="${APP_DSN%/*}"
dev_owner_url="${owner_prefix}/${db}"
dev_app_url="${app_prefix}/${db}"
admin_url="${owner_prefix}/postgres"

rundir=".tmp/dev/${slug:-_base}"
log="${rundir}/dev.log"
state="${rundir}/env"
routing="${rundir}/ai-routing.yaml"

wait_ready() { # url timeout_s — only a 2xx counts as ready (a 401/500/503 is not).
  local url="$1" timeout="$2"
  for _ in $(seq 1 "$timeout"); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "$url" 2>/dev/null || true)
    [[ "$code" =~ ^2[0-9]{2}$ ]] && return 0
    sleep 1
  done
  return 1
}

case "$cmd" in
up)
  # Refuse if either port is already bound — otherwise a second `up` would fail
  # to bind silently and wait_ready would get a false "ready" from the OLD
  # server. (Vite in particular would auto-increment off a taken port without
  # --strictPort, landing on a port we never poll.) Stop it first.
  for _p in "$api_port" "$fe_port"; do
    if lsof -ti "tcp:${_p}" >/dev/null 2>&1; then
      echo "FAIL: port :${_p} already in use — is $label already running? (make dev-stop${slug:+ DEV_SLUG=$slug})" >&2
      exit 1
    fi
  done
  # The FE runs via `pnpm exec vite`, which needs node_modules.
  if [[ ! -d frontend/node_modules ]]; then
    echo "FAIL: frontend/node_modules missing — run 'make install' (or 'cd frontend && pnpm install') before 'make dev'." >&2
    exit 1
  fi
  mkdir -p "$rundir"
  : > "$log"
  echo "$label → db=$db api=:$api_port fe=:$fe_port (logs: $log)"
  {
    echo "=== infra + db ==="
    make db-up
    # The base `margince` db already exists (db-up + db-init); only a slugged
    # env needs its own database created.
    [[ -n "$slug" ]] && psql "$admin_url" -c "CREATE DATABASE \"${db}\"" 2>&1 || true
    ( cd backend && go run ./cmd/migrate up --dsn "$dev_owner_url" )
    echo "=== build api (once, before the readiness poll) ==="
    ( cd backend && go build -o ../bin/api ./cmd/api )
    echo "=== servers ==="
  } >>"$log" 2>&1

  # Per-engineer routing lives in a gitignored config/ai-routing.yaml; seed it
  # from the committed template on first run so `make dev` is green without a
  # prior `make install`. Editing the copy binds your own local models.
  routing_src="config/ai-routing.yaml"
  if [[ ! -f "$routing_src" ]]; then
    cp config/ai-routing.example.yaml "$routing_src"
    echo "dev: seeded $routing_src from config/ai-routing.example.yaml — edit it to bind local models"
  fi

  # BYOK: real model powers the /coldstart read-back when a key is present; the
  # offline fake otherwise. The routing parser reads api_key literally (no ${ENV}
  # expansion), so the key is injected into a scratch copy under the rundir.
  ai_flag=(--ai-fake)
  if [[ -f .env.local ]]; then
    set -a; . ./.env.local; set +a
  fi
  if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
    sed "s#provider: anthropic, model: \([^ }]*\)#provider: anthropic, model: \1, api_key: ${ANTHROPIC_API_KEY}#g" \
      "$routing_src" > "$routing"
    ai_flag=(--ai-routing "$routing")
    echo "dev: using real Anthropic model for the cold-start read-back (key from .env.local)"
  else
    echo "dev: no ANTHROPIC_API_KEY in .env.local — cold-start runs on the offline fake"
  fi

  # Run the compiled binary directly (not `go run`): it starts in <1s so the
  # poll window is real, and $be_pid is the actual server process for a clean
  # kill. Redis is the ONE shared instance.
  MARGINCE_ENV=dev \
    MARGINCE_BLOBSTORE_ENDPOINT="localhost:${MINIO_PORT}" \
    MARGINCE_BLOBSTORE_ACCESS_KEY=minioadmin \
    MARGINCE_BLOBSTORE_SECRET_KEY=minioadmin \
    MARGINCE_BLOBSTORE_BUCKET=margince-dev \
    ./bin/api --addr ":${api_port}" --dsn "$dev_app_url" \
    --redis "localhost:${REDIS_PORT}" "${ai_flag[@]}" >>"$log" 2>&1 &
  be_pid=$!

  if ! wait_ready "http://localhost:${api_port}/readyz" 90; then
    echo "FAIL: $label api did not become ready — see ${log}" >&2
    kill "$be_pid" 2>/dev/null || true
    exit 1
  fi
  # Seed the demo workspace through the public API (idempotent). A seed failure
  # is fatal: `make dev` must not report ready while promising a login that the
  # unseeded workspace can't serve.
  if ! API_BASE="http://localhost:${api_port}" bash scripts/seed-dev.sh >>"$log" 2>&1; then
    echo "FAIL: $label API seed failed — see ${log}" >&2
    kill "$be_pid" 2>/dev/null || true
    exit 1
  fi
  # Dev DB seed for API-less demo data (FX rates today; see scripts/seed-dev.sql).
  # Applied here too so a plain `make dev` pre-fills everything — e.g. winning a
  # non-EUR deal shows the frozen FX line with no manual step.
  if ! psql "$dev_owner_url" -v ON_ERROR_STOP=1 -f scripts/seed-dev.sql >>"$log" 2>&1; then
    echo "FAIL: $label dev DB seed failed — see ${log}" >&2
    kill "$be_pid" 2>/dev/null || true
    exit 1
  fi

  # The FE's /v1 proxy follows the api via BACKEND_PORT (see vite.config.ts).
  # `pnpm --dir frontend` keeps the cwd at the repo root, so $! is vite itself
  # (a `(cd … & )` subshell would capture the subshell, not the server).
  BACKEND_PORT="${api_port}" pnpm --dir frontend exec vite --port "${fe_port}" --strictPort >>"$log" 2>&1 &
  fe_pid=$!

  printf 'SLUG=%s\nAPI_PORT=%s\nFE_PORT=%s\nDB=%s\nBACKEND_PID=%s\nFE_PID=%s\nLOG=%s\n' \
    "$slug" "$api_port" "$fe_port" "$db" "$be_pid" "$fe_pid" "$log" >"$state"

  if wait_ready "http://localhost:${fe_port}/" 90; then
    echo "$label ready"
    echo "  api      http://localhost:${api_port}"
    echo "  frontend http://localhost:${fe_port}"
    echo "  logs     ${log}"
    echo "  stop     make dev-stop${slug:+ DEV_SLUG=$slug}"
  else
    echo "FAIL: $label FE did not become ready in time — see ${log}" >&2
    # Don't leave the api (and vite) orphaned when the FE readiness poll fails.
    kill "$be_pid" "$fe_pid" 2>/dev/null || true
    exit 1
  fi
  ;;

stop)
  if [[ -f "$state" ]]; then
    # shellcheck disable=SC1090
    . "$state"
    kill "${BACKEND_PID:-}" "${FE_PID:-}" 2>/dev/null || true
    # Backstop: free the recorded ports by listener (reaps vite, pnpm's child).
    for p in "${API_PORT:-}" "${FE_PORT:-}"; do
      [[ -n "$p" ]] || continue
      pids=$(lsof -ti "tcp:${p}" 2>/dev/null || true)
      [[ -n "$pids" ]] && kill $pids 2>/dev/null || true
    done
    rm -rf "$rundir"
    echo "stopped $label (freed :${API_PORT:-?} :${FE_PORT:-?})"
  else
    for p in "$api_port" "$fe_port"; do
      pids=$(lsof -ti "tcp:${p}" 2>/dev/null || true)
      [[ -n "$pids" ]] && kill $pids 2>/dev/null || true
    done
    echo "no recorded env for $label (freed derived ports :$api_port :$fe_port if bound)"
  fi
  if [[ "$drop" == "1" ]]; then
    if [[ -z "$slug" ]]; then
      echo "refusing to drop the shared 'margince' database — pass DEV_SLUG=<slug> to drop an isolated env" >&2
    else
      # WITH (FORCE) (PG13+) terminates any lingering connection so the drop
      # doesn't fail on a slow-to-close api/vite child.
      psql "$admin_url" -c "DROP DATABASE IF EXISTS \"${db}\" WITH (FORCE)" >/dev/null 2>&1 || true
      echo "dropped ${db}"
    fi
  fi
  ;;

*)
  echo "usage: dev.sh {up|stop} [slug] [--drop]" >&2
  exit 2
  ;;
esac
