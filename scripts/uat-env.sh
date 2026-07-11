#!/usr/bin/env bash
# Per-worktree UAT env on the ONE shared infra. Each slug gets its own database
# margince_uat_<slug> plus deterministic api/FE ports derived from the slug, so
# two worktrees can run a live UAT stack concurrently without colliding on the
# database or the host ports. Ported from the foundation skeleton and adapted to
# this repo: cmd/api (an --addr binary, not bin/server), cmd/migrate (not the
# golang-migrate CLI), the API-driven seed (scripts/seed-dev.sh, not SQL
# fixtures), and the plain frontend/ (not a pnpm workspace).
#
# Credentials are NOT hardcoded: the connection URLs derive from OWNER_DSN /
# APP_DSN (this repo's dev role DSNs; overridable), so this script carries no
# secret literal beyond the shared dev defaults.
#
#   scripts/uat-env.sh up   <slug>            # spin infra + db + api + FE
#   scripts/uat-env.sh stop <slug> [--drop]   # stop servers; --drop also drops the db
set -euo pipefail

cmd="${1:-}"
slug="${2:-}"
shift 2 2>/dev/null || true
drop=0
for a in "$@"; do [[ "$a" == "--drop" ]] && drop=1; done

if [[ -z "$slug" ]]; then
  echo "FAIL: uat_env requires UAT_SLUG=<slug>" >&2
  exit 1
fi
# The slug flows into a filesystem path and a CREATE DATABASE identifier — keep
# it to a safe charset so it can neither traverse paths nor break/inject SQL.
if ! [[ "$slug" =~ ^[a-z0-9_-]+$ ]]; then
  echo "FAIL: UAT_SLUG must match ^[a-z0-9_-]+$ (got '$slug')" >&2
  exit 1
fi

cd "$(git rev-parse --show-toplevel)"

# This repo's dev connection surface (overridable). OWNER_DSN runs migrations;
# APP_DSN is the non-superuser role the api connects as (RLS binds it).
OWNER_DSN="${OWNER_DSN:-postgres://margince_owner:dev@localhost:55432/margince}"
APP_DSN="${APP_DSN:-postgres://margince_app:margince_app_dev@localhost:55432/margince}"
REDIS_PORT="${REDIS_PORT:-56379}"

# Deterministic derivation (same slug → same db + ports, so a resume reuses the
# existing migrated+seeded data and rebinds the same host ports).
hash=$(printf '%s' "$slug" | cksum | awk '{print $1 % 1000}')
api_port=$(( 8080 + hash ))
fe_port=$(( 5173 + hash ))
db="margince_uat_${slug}"

# Swap the database segment of each base DSN — no credential literal here.
owner_prefix="${OWNER_DSN%/*}"          # scheme://user:pass@host:port
app_prefix="${APP_DSN%/*}"
uat_owner_url="${owner_prefix}/${db}"
uat_app_url="${app_prefix}/${db}"
admin_url="${owner_prefix}/postgres"

rundir=".tmp/uat/${slug}"
log="${rundir}/uat.log"
state="${rundir}/env"

wait_ready() { # url timeout_s — any HTTP response (even 401) means the port is serving.
  local url="$1" timeout="$2"
  for _ in $(seq 1 "$timeout"); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "$url" 2>/dev/null || true)
    [[ -n "$code" && "$code" != "000" ]] && return 0
    sleep 1
  done
  return 1
}

case "$cmd" in
up)
  # Refuse if the derived api port is already bound — otherwise a second `up` for
  # a still-running slug would fail to bind silently and wait_ready would get a
  # false "ready" from the OLD server. Stop it first.
  if lsof -ti "tcp:${api_port}" >/dev/null 2>&1; then
    echo "FAIL: api port :${api_port} already in use — is env '$slug' already running? (make uat_env_stop UAT_SLUG=$slug)" >&2
    exit 1
  fi
  # The FE runs via `pnpm exec vite`, which needs node_modules.
  if [[ ! -d frontend/node_modules ]]; then
    echo "FAIL: frontend/node_modules missing — run 'cd frontend && pnpm install' before 'make uat_env'." >&2
    exit 1
  fi
  mkdir -p "$rundir"
  : > "$log"
  echo "uat_env '$slug' → db=$db api=:$api_port fe=:$fe_port (logs: $log)"
  {
    echo "=== infra + db ==="
    make db-up
    psql "$admin_url" -c "CREATE DATABASE \"${db}\"" 2>&1 || true
    ( cd backend && go run ./cmd/migrate up --dsn "$uat_owner_url" )
    echo "=== build api (once, before the readiness poll) ==="
    ( cd backend && go build -o ../bin/api ./cmd/api )
    echo "=== servers ==="
  } >>"$log" 2>&1

  # Run the compiled binary directly (not `go run`): it starts in <1s so the
  # poll window is real, and $be_pid is the actual server process for a clean
  # kill. MARGINCE_ENV=dev is required so the api trusts the X-Workspace-Slug
  # header the seed and FE send. Redis is the ONE shared instance.
  MARGINCE_ENV=dev ./bin/api --addr ":${api_port}" --dsn "$uat_app_url" \
    --redis "localhost:${REDIS_PORT}" >>"$log" 2>&1 &
  be_pid=$!

  if ! wait_ready "http://localhost:${api_port}/readyz" 90; then
    echo "FAIL: uat_env '$slug' api did not become ready — see ${log}" >&2
    kill "$be_pid" 2>/dev/null || true
    exit 1
  fi
  # Seed the demo workspace through the public API (idempotent).
  API_BASE="http://localhost:${api_port}" bash scripts/seed-dev.sh >>"$log" 2>&1 || true

  # The FE's /v1 proxy follows the api via BACKEND_PORT (see vite.config.ts).
  # `pnpm --dir frontend` keeps the cwd at the repo root, so $! is vite itself
  # (a `(cd … & )` subshell would capture the subshell, not the server).
  BACKEND_PORT="${api_port}" pnpm --dir frontend exec vite --port "${fe_port}" >>"$log" 2>&1 &
  fe_pid=$!

  printf 'SLUG=%s\nAPI_PORT=%s\nFE_PORT=%s\nDB=%s\nBACKEND_PID=%s\nFE_PID=%s\nLOG=%s\n' \
    "$slug" "$api_port" "$fe_port" "$db" "$be_pid" "$fe_pid" "$log" >"$state"

  if wait_ready "http://localhost:${fe_port}/" 90; then
    echo "UAT env '$slug' ready"
    echo "  api      http://localhost:${api_port}"
    echo "  frontend http://localhost:${fe_port}"
    echo "  logs     ${log}"
    echo "  stop     make uat_env_stop UAT_SLUG=${slug}"
  else
    echo "FAIL: uat_env '$slug' FE did not become ready in time — see ${log}" >&2
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
    echo "stopped uat_env '$slug' (freed :${API_PORT:-?} :${FE_PORT:-?})"
  else
    for p in "$api_port" "$fe_port"; do
      pids=$(lsof -ti "tcp:${p}" 2>/dev/null || true)
      [[ -n "$pids" ]] && kill $pids 2>/dev/null || true
    done
    echo "no recorded env for '$slug' (freed derived ports :$api_port :$fe_port if bound)"
  fi
  if [[ "$drop" == "1" ]]; then
    psql "$admin_url" -c "DROP DATABASE IF EXISTS \"${db}\"" >/dev/null 2>&1 || true
    echo "dropped ${db}"
  fi
  ;;

*)
  echo "usage: uat-env.sh {up|stop} <slug> [--drop]" >&2
  exit 2
  ;;
esac
