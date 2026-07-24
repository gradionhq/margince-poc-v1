#!/usr/bin/env bash
# One-command local dev stack on the ONE shared infra: Postgres + Redis, the api
# (cmd/api), the background worker (cmd/worker — outbox relay + Surface-B runner),
# and the Vite dev server — so the SPA runs in a real browser against a live api.
# Bare `make dev` serves the app on :8080 (the api behind it on :18080);
# `make dev DEV_SLUG=<slug>` gives an isolated `margince_dev_<slug>` database on
# slug-derived ports, so two worktrees can run concurrently without colliding on
# the database or the host ports.
#
# MARGINCE_ENV=dev is set so the api trusts the X-Workspace-Slug header the FE
# and the seed script send. localhost is a browser secure-context, so the Secure session
# cookie survives over plain http — no TLS front door needed.
#
# BYOK: if .env.local sets a cloud key (GEMINI_API_KEY / OPENAI_API_KEY /
# ANTHROPIC_API_KEY / OPENAI_COMPATIBLE_API_KEY), sourcing it exports the var and
# the api/worker inherit it — SelectBrain reads the key from the environment at
# boot (the routing file holds only providers), so the cold-start read-back runs
# the real model; otherwise the offline fake (--ai-fake) drives it.
#
# Credentials are NOT hardcoded: the connection URLs derive from OWNER_DSN /
# APP_DSN (this repo's dev role DSNs; overridable), so this script carries no
# secret literal beyond the shared dev defaults.
#
#   scripts/dev.sh up   [slug] [--fresh]  # spin infra + db + api + FE
#   scripts/dev.sh stop [slug] [--drop]   # stop servers; --drop also drops the db
set -euo pipefail
# Runtime state under .tmp/dev/ (logs, pids) — keep everything this script
# writes owner-only.
umask 077

cmd="${1:-}"
slug="${2:-}"
drop=0
fresh=0
case "${3:-}" in
  --drop) drop=1 ;;
  --fresh) fresh=1 ;;
esac

cd "$(git rev-parse --show-toplevel)"
repo_root="$PWD"

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
# same db + ports, so a resume reuses whatever that database already holds).
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
# :8080 is THE port — the app, the thing a human opens, always and only.
# The api sits behind it on 18080 and the app's dev server proxies /v1 and
# the probes through, so `curl localhost:8080/v1/...` still answers and
# nobody has to remember which of two ports serves what. The two ranges
# (8080.. and 18080..) cannot collide however the slug hashes.
fe_port=$(( 8080 + hash ))
api_port=$(( 18080 + hash ))

# Swap the database segment of each base DSN — no credential literal here.
owner_prefix="${OWNER_DSN%/*}"          # scheme://user:pass@host:port
app_prefix="${APP_DSN%/*}"
dev_owner_url="${owner_prefix}/${db}"
dev_app_url="${app_prefix}/${db}"

# psql is NOT a host requirement (hosts need Go + Docker only): every ad-hoc
# SQL statement runs inside the compose postgres container, the same way
# `make db-init` applies scripts/db-init.sql.
psql_owner() { # db [psql args…] — SQL via args or stdin
  local db="$1"; shift
  docker compose -f infra/docker-compose.dev.yml exec -T postgres \
    psql -U margince_owner -d "$db" "$@"
}

rundir=".tmp/dev/${slug:-_base}"
log="${rundir}/dev.log"
state="${rundir}/env"

wait_ready() { # url timeout_s — only a 2xx counts as ready (a 401/500/503 is not).
  local url="$1" timeout="$2"
  for _ in $(seq 1 "$timeout"); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "$url" 2>/dev/null || true)
    [[ "$code" =~ ^2[0-9]{2}$ ]] && return 0
    sleep 1
  done
  return 1
}

# A stack that outlives the shell that started it is the worst failure mode this
# script has: an api from an earlier branch keeps answering while Vite serves the
# code you just wrote, and the app breaks in ways that look exactly like your bug.
# So a bare `make dev` does not merely check its own two ports — it claims the
# machine: every margince server process, every recorded stack, and every
# leftover per-slug database goes, and the one stack that remains is this one on
# :8080 against `margince`. `DEV_SLUG` keeps its escape hatch (it sweeps
# nothing, so an isolated env survives until the next bare `make dev`).

kill_pids() { # pid… — TERM, then KILL whatever is still standing
  local pids=("$@") alive=()
  [[ ${#pids[@]} -gt 0 ]] || return 0
  kill "${pids[@]}" 2>/dev/null || true
  for _ in $(seq 1 10); do
    alive=()
    for p in "${pids[@]}"; do kill -0 "$p" 2>/dev/null && alive+=("$p"); done
    [[ ${#alive[@]} -eq 0 ]] && return 0
    sleep 0.5
  done
  kill -9 "${alive[@]}" 2>/dev/null || true
}

# Margince server processes anywhere on this machine, not just this checkout: a
# second worktree's api on :8081 owns a different database and is exactly the
# ghost this sweep exists to remove. Matched on the binary name AND a margince
# connection string, so an unrelated program called `api` is never touched.
margince_server_pids() {
  local pid cmd
  for pid in $(pgrep -f 'bin/(api|worker)|exe/(api|worker)' 2>/dev/null || true); do
    [[ "$pid" == "$$" ]] && continue
    cmd=$(ps -o command= -p "$pid" 2>/dev/null || true)
    [[ "$cmd" == *margince* ]] && echo "$pid"
  done
}

# Vite resolves out of <repo>/frontend/node_modules, so its command line carries
# the worktree path — that is what distinguishes our dev server from any other
# Vite project the developer happens to be running.
vite_pids() {
  local pid cmd
  for pid in $(pgrep -f 'vite' 2>/dev/null || true); do
    [[ "$pid" == "$$" ]] && continue
    cmd=$(ps -o command= -p "$pid" 2>/dev/null || true)
    [[ "$cmd" == *"$repo_root"* ]] && echo "$pid"
  done
}

# port_listeners names only the processes SERVING a port. Plain
# `lsof -ti tcp:8080` also lists everything CONNECTED to it — the
# developer's browser among them — and this sweep kills what it is given.
port_listeners() { # port
  lsof -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null || true
}

# still_ours answers whether a pid recorded in an old state file is still a
# process of ours. PIDs are recycled: a stack killed by a crash or a machine
# sleep leaves its file behind, and by the time the next `make dev` reads it
# that number can belong to anything. The pgrep paths already re-check the
# live command line before killing — a recorded pid gets the same proof.
still_ours() { # pid
  local cmd
  cmd=$(ps -o command= -p "$1" 2>/dev/null || true)
  [[ -n "$cmd" ]] || return 1
  [[ "$cmd" == *margince* || "$cmd" == *"$repo_root"* ]]
}

sweep_stacks() { # kill every margince dev stack: recorded, orphaned, or foreign
  local victims=() pids p port state_file
  local BACKEND_PID FE_PID WORKER_PID API_PORT FE_PORT
  # 1. Every stack this script ever recorded — its own pids and its own ports.
  #    The locals above shadow what the state file sets, so sourcing one cannot
  #    leak a stale pid into the rest of the run.
  for state_file in .tmp/dev/*/env; do
    [[ -f "$state_file" ]] || continue
    BACKEND_PID=''; FE_PID=''; WORKER_PID=''; API_PORT=''; FE_PORT=''
    # shellcheck disable=SC1090
    . "$state_file"
    for p in "$BACKEND_PID" "$FE_PID" "$WORKER_PID"; do
      [[ -n "$p" ]] && still_ours "$p" && victims+=("$p")
    done
    for port in "$API_PORT" "$FE_PORT"; do
      [[ -n "$port" ]] || continue
      for p in $(port_listeners "$port"); do victims+=("$p"); done
    done
  done
  # 2. Orphans whose state file is gone, and stacks from other checkouts.
  for p in $(margince_server_pids) $(vite_pids); do victims+=("$p"); done
  # 3. Anything at all holding the ports this stack is about to bind — a foreign
  #    process on :8080 loses the port rather than silently shadowing the api.
  for port in "$api_port" "$fe_port"; do
    for p in $(port_listeners "$port"); do victims+=("$p"); done
  done

  # Deduplicate: the same pid legitimately arrives from several sources.
  pids=$(printf '%s\n' "${victims[@]+"${victims[@]}"}" | grep -E '^[0-9]+$' | sort -u || true)
  if [[ -n "$pids" ]]; then
    # shellcheck disable=SC2086
    kill_pids $pids
    echo "dev: swept $(printf '%s\n' $pids | wc -l | tr -d ' ') stray process(es) from earlier stacks"
  fi
  rm -rf .tmp/dev/*
}

drop_stray_dev_dbs() { # every margince_dev_<slug> database an isolated env left behind
  local strays
  strays=$(psql_owner postgres -tAc \
    "SELECT datname FROM pg_database WHERE datname LIKE 'margince\\_dev\\_%'" 2>/dev/null | tr -d '\r' || true)
  [[ -n "$strays" ]] || return 0
  while read -r stray; do
    [[ -n "$stray" ]] || continue
    # WITH (FORCE) terminates a connection the just-killed process has not
    # finished closing; the shared `margince` and the test lane's
    # margince_test* / margince_it_* namespaces never match this pattern.
    # </dev/null is load-bearing: psql_owner runs `docker compose exec -T`,
    # which would otherwise swallow the rest of this loop's input and drop
    # exactly one database however many are stray.
    psql_owner postgres -c "DROP DATABASE IF EXISTS \"${stray}\" WITH (FORCE)" >/dev/null 2>&1 </dev/null || true
    echo "dev: dropped stray database ${stray}"
  done <<<"$strays"
}

case "$cmd" in
up)
  if [[ -z "$slug" ]]; then
    # Bare `make dev` is the exclusive stack: clear the machine first, so the
    # ports below are free by construction and the browser can only be talking
    # to the api this command starts.
    sweep_stacks
  fi
  # Belt and braces after the sweep, and the only guard a slugged env gets: a
  # bound port must stop the boot, because binding would fail silently and
  # wait_ready would then read "ready" off the OLD server. (Vite without
  # --strictPort would not even fail — it would walk to a port we never poll.)
  for _p in "$api_port" "$fe_port"; do
    if [[ -n "$(port_listeners "$_p")" ]]; then
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
    # The stray-database sweep runs HERE, not with the process sweep above:
    # every statement it issues goes through the compose Postgres, so running
    # it before db-up on a stopped stack would connect to nothing, fail
    # silently, and leave the databases to reappear the moment infra starts.
    if [[ -z "$slug" ]]; then
      drop_stray_dev_dbs
    fi
    # --fresh means "the install a first customer gets": drop the database
    # and let the migrations and the bootstrap rebuild it. Deliberately not
    # the default — a restart to pick up a backend change must not cost the
    # records you were half-way through creating.
    if [[ "$fresh" == "1" ]]; then
      # psql_owner always talks to the COMPOSE Postgres, while the migration
      # below connects through OWNER_DSN. Point that elsewhere and --fresh
      # would erase one database and migrate another; refuse rather than
      # rebuild something the caller never named.
      if [[ "$OWNER_DSN" != "postgres://margince_owner:dev@localhost:55432/margince" ]]; then
        # The DSN itself is never echoed: it carries a password, and this
        # branch exists precisely because the caller supplied a real one.
        echo "FAIL: --fresh rebuilds the compose Postgres, but OWNER_DSN points somewhere else — drop that database yourself, then run make dev" >&2
        exit 1
      fi
      psql_owner postgres -c "DROP DATABASE IF EXISTS \"${db}\" WITH (FORCE)" </dev/null
      psql_owner postgres -c "CREATE DATABASE \"${db}\"" </dev/null
      psql_owner "$db" -v ON_ERROR_STOP=1 <scripts/db-init.sql
    fi
    # The base `margince` db already exists (db-up + db-init); only a slugged
    # env needs its own database created.
    [[ -n "$slug" ]] && psql_owner postgres -c "CREATE DATABASE \"${db}\"" 2>&1 || true
    ( cd backend && go run ./cmd/migrate up --dsn "$dev_owner_url" )
    echo "=== build api (once, before the readiness poll) ==="
    # The composed workspace (ADR-0069): materialize build/composition/
    # and build the role binaries against it, so an enabled extension set
    # under extensions/ reaches the dev stack; vanilla composes empty.
    ( cd backend && GOWORK="$PWD/../go.work" go run ./tools/gen-composition )
    ( cd backend && GOWORK="$PWD/../build/composition/go.work" go build -o ../bin/api ./cmd/api )
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

  # BYOK: the real model powers the /coldstart read-back when a cloud key is in
  # the environment, the offline fake otherwise. Secrets ride the ENVIRONMENT —
  # the api resolves each provider's key from its conventional env var
  # (GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY / OPENAI_COMPATIBLE_API_KEY)
  # at boot; the routing file names only providers, never a key. Sourcing
  # .env.local exports those vars, and the api/worker started below inherit them —
  # no key ever lands in a config file. Seed .env.local from the tracked template
  # on first run so a fresh clone has a documented place for these keys.
  if [[ ! -f .env.local && -f .env.template ]]; then
    cp .env.template .env.local
    echo "dev: seeded .env.local from .env.template — edit it to set keys (GEMINI_API_KEY, MARGINCE_GMAIL_*, …)"
  fi
  ai_flag=(--ai-fake)
  if [[ -f .env.local ]]; then
    set -a; . ./.env.local; set +a
  fi
  # Real routing needs the key for EVERY cloud provider the routing file
  # actually binds — SelectBrain fails closed at boot on the first bound
  # provider whose env key is missing, so "any key present" is not enough
  # (e.g. an anthropic-only .env.local against the gemini-bound template
  # would refuse to start). Comments are stripped before scanning so the
  # template's commented alternatives don't count as bindings.
  bound_providers=$(sed 's/#.*//' "$routing_src" | grep -Eo 'provider:[[:space:]]*[a-z_]+' | awk -F': *' '{print $2}' | sort -u || true)
  missing_keys=""
  for _p in $bound_providers; do
    _env=""
    case "$_p" in
      anthropic)         _env="ANTHROPIC_API_KEY" ;;
      openai)            _env="OPENAI_API_KEY" ;;
      gemini)            _env="GEMINI_API_KEY" ;;
      openai_compatible) _env="OPENAI_COMPATIBLE_API_KEY" ;;
    esac
    if [[ -n "$_env" && -z "${!_env:-}" ]]; then
      missing_keys="$missing_keys $_env"
    fi
  done
  # Real routing whenever every bound provider is satisfied — cloud providers
  # need their key; local ones (ollama/vllm/fake) need none, so a local-only
  # routing file gets --ai-routing without any key in the environment.
  if [[ -z "$missing_keys" ]]; then
    ai_flag=(--ai-routing "$routing_src")
    echo "dev: using $routing_src for the cold-start read-back (bound providers: $(echo $bound_providers | tr '\n' ' '))"
  else
    echo "dev: $routing_src binds provider(s) whose key is not set (${missing_keys# }) — cold-start runs on the offline fake; set the key(s) in .env.local or rebind the provider in $routing_src"
  fi

  # The dev keyvault seals connector credentials (IMAP app passwords, OAuth
  # refresh tokens). Export it unconditionally so `make dev` can connect an IMAP
  # mailbox with no Google OAuth app configured — the gmail branch below adds
  # only the OAuth-specific state key and base URLs.
  export MARGINCE_KEYVAULT_ROOT_KEY="${MARGINCE_KEYVAULT_ROOT_KEY:-bWFyZ2luY2UtZGV2LW9ubHkta2V5dmF1bHQtcm9vdGs=}"

  # Gmail capture connector: when .env.local supplies a Google OAuth app, pass
  # its flags to the api and run the sync worker. Absent it, `make dev` is
  # unchanged and the /connectors/gmail/* surface stays its declared 501.
  gmail_api_flags=()
  gmail_enabled=0
  if [[ -n "${MARGINCE_GMAIL_CLIENT_ID:-}" && -n "${MARGINCE_GMAIL_CLIENT_SECRET:-}" ]]; then
    gmail_enabled=1
    # Secrets travel via the environment, NEVER CLI flags (argv is visible in
    # the process table). The client id/secret are already exported from
    # .env.local; export the OAuth state key too. The api/worker flags
    # default to these env vars, so we pass only the non-secret, dev-computed
    # URLs on the command line.
    export MARGINCE_CONNECTOR_STATE_KEY="${MARGINCE_CONNECTOR_STATE_KEY:-margince-dev-connector-state-key-0001}"
    gmail_api_flags=(
      # public base = the SPA (:fe_port), where the browser lands after consent;
      # api base = the api (:api_port), where the callback redirect_uri resolves.
      --public-base-url "http://localhost:${fe_port}"
      --api-base-url "http://localhost:${api_port}"
    )
    echo "dev: gmail capture connector enabled (callback http://localhost:${api_port}/v1/connectors/gmail/callback)"
  fi

  # The deployment configuration (A107/ADR-0061): the api bootstraps the demo
  # organization itself at boot — no public provisioning endpoint exists. Seeded
  # ONCE into a gitignored config/margince.yaml from config/margince.example.yaml
  # and then LEFT ALONE — the same create-if-missing / leave-if-exists pattern as
  # config/ai-routing.yaml — so an engineer can edit org details or runtime
  # posture (e.g. ai.capture_payloads for Layer-3 capture) and it persists across
  # restarts (it lives in config/, not the scratch rundir dev-stop clears).
  deploy_cfg="config/margince.yaml"
  admin_pw_file="config/margince-admin-password"
  if [[ ! -f "$admin_pw_file" ]]; then
    printf '%s' "${ADMIN_PASSWORD:-demo-password-123}" >"$admin_pw_file"
    chmod 600 "$admin_pw_file"
  fi
  if [[ ! -f "$deploy_cfg" ]]; then
    cp config/margince.example.yaml "$deploy_cfg"
    echo "dev: seeded $deploy_cfg from config/margince.example.yaml — edit it to change org/admin or AI posture (e.g. ai.capture_payloads)"
  fi
  # Report which deployment config the api + worker are using, and its AI
  # posture — mirrors the ai-routing line above so both configs are visible.
  if grep -Eq '^[[:space:]]*capture_payloads:[[:space:]]*true' "$deploy_cfg"; then
    capture_note="ai.capture_payloads ON"
  else
    capture_note="ai.capture_payloads off"
  fi
  echo "dev: using $deploy_cfg for the deployment config ($capture_note)"

  # Run the compiled binary directly (not `go run`): it starts in <1s so the
  # poll window is real, and $be_pid is the actual server process for a clean
  # kill. Redis is the ONE shared instance. The api keeps its default inline
  # relay: it coexists with the worker's standalone relay (started below) —
  # outbox rows are claimed FOR UPDATE SKIP LOCKED, so two relays never
  # double-ship.
  MARGINCE_ENV=dev \
    MARGINCE_BLOBSTORE_ENDPOINT="localhost:${MINIO_PORT}" \
    MARGINCE_BLOBSTORE_ACCESS_KEY=minioadmin \
    MARGINCE_BLOBSTORE_SECRET_KEY=minioadmin \
    MARGINCE_BLOBSTORE_BUCKET=margince-dev \
    ./bin/api --addr ":${api_port}" --dsn "$dev_app_url" --config "$deploy_cfg" \
    --redis "localhost:${REDIS_PORT}" \
    "${ai_flag[@]}" "${gmail_api_flags[@]+"${gmail_api_flags[@]}"}" >>"$log" 2>&1 &
  be_pid=$!

  if ! wait_ready "http://localhost:${api_port}/readyz" 90; then
    echo "FAIL: $label api did not become ready — see ${log}" >&2
    kill "$be_pid" 2>/dev/null || true
    exit 1
  fi
  # No demo records: `make dev` brings up a COLD START — the installation the
  # api bootstrapped from the deployment config (one organization, one admin
  # seat) and nothing else, so onboarding, empty states, and first-run flows are
  # what a developer sees by default. Demo data is an explicit opt-in step:
  # `make seed-dev` (API records + the FX/RBAC fixture) jumps over the cold
  # start on a stack that is already up.

  # The background process role (cmd/worker) always runs alongside the api in
  # dev: the standalone outbox relay (coexists with the api's inline relay —
  # rows are claimed FOR UPDATE SKIP LOCKED, so two relays never double-ship),
  # the Surface-B runner scheduler, and the retention / close-date / reconcile
  # sweeps AND the automation time-scan (the ONLY cg:workflows consumer + the
  # clock-trigger scheduler — without the worker, `make dev` fires no
  # automations at all, event- or clock-triggered). It gets the SAME config
  # surface as the api — the same $ai_flag (real cloud model when
  # .env.local set a BYOK key, else the offline fake, so its runner
  # matches the api), the same blobstore endpoint, and the .env.local keys
  # already exported into this shell (vault + Gmail secrets travel via the
  # environment, never CLI flags). Gmail adds a short sync poll only when the
  # connector is configured.
  #
  # --retention-interval 720h: the worker runs the nightly GDPR
  # retention/erasure pass unconditionally. River's RunOnStart still fires one
  # evaluation immediately at boot (inherent, not gated by this flag) — but it
  # only ERASES data past its jurisdiction floor, so on a fresh dev database
  # it is a no-op. The long interval just stops it recurring during a dev
  # session.
  ( cd backend && GOWORK="$PWD/../build/composition/go.work" go build -o ../bin/worker ./cmd/worker ) >>"$log" 2>&1
  worker_gmail_flags=()
  if [[ "$gmail_enabled" == "1" ]]; then
    # A short poll makes the demo mailbox responsive; the default is 2m.
    worker_gmail_flags=(--gmail-sync-interval 30s)
  fi
  MARGINCE_ENV=dev \
    MARGINCE_BLOBSTORE_ENDPOINT="localhost:${MINIO_PORT}" \
    MARGINCE_BLOBSTORE_ACCESS_KEY=minioadmin \
    MARGINCE_BLOBSTORE_SECRET_KEY=minioadmin \
    MARGINCE_BLOBSTORE_BUCKET=margince-dev \
    ./bin/worker --dsn "$dev_app_url" --redis "localhost:${REDIS_PORT}" \
    --config "$deploy_cfg" \
    --retention-interval 720h \
    "${ai_flag[@]}" "${worker_gmail_flags[@]+"${worker_gmail_flags[@]}"}" >>"$log" 2>&1 &
  worker_pid=$!
  if [[ "$gmail_enabled" == "1" ]]; then
    echo "  worker   background relay + Surface-B runner + time-scan + Gmail sync (poll every 30s)"
  else
    echo "  worker   background relay + Surface-B runner + automation time-scan running"
  fi

  # The FE's /v1 proxy follows the api via BACKEND_PORT (see vite.config.ts).
  # `pnpm --dir frontend` keeps the cwd at the repo root, so $! is vite itself
  # (a `(cd … & )` subshell would capture the subshell, not the server).
  BACKEND_PORT="${api_port}" pnpm --dir frontend exec vite --port "${fe_port}" --strictPort >>"$log" 2>&1 &
  fe_pid=$!

  printf 'SLUG=%s\nAPI_PORT=%s\nFE_PORT=%s\nDB=%s\nBACKEND_PID=%s\nFE_PID=%s\nWORKER_PID=%s\nLOG=%s\n' \
    "$slug" "$api_port" "$fe_port" "$db" "$be_pid" "$fe_pid" "$worker_pid" "$log" >"$state"

  if wait_ready "http://localhost:${fe_port}/" 90; then
    echo "$label ready"
    echo ""
    echo "  OPEN     http://localhost:${fe_port}"
    echo ""
    echo "  api      http://localhost:${api_port}  (also proxied at :${fe_port}/v1)"
    # The only seat on a cold start is the bootstrap admin, and the deployment
    # config is where it is defined — read the address back from that file
    # rather than restating it, so an edited config prints the truth.
    admin_email="$(sed -n 's/^[[:space:]]*email:[[:space:]]*\(.*\)$/\1/p' "$deploy_cfg" | head -1)"
    echo "  login    ${admin_email} / $(cat "$admin_pw_file")  (bootstrap admin — cold start, no other data)"
    echo "  demo     make seed-dev  — adds the demo records + rep seats on top"
    echo "  logs     ${log}"
    echo "  stop     make dev-stop${slug:+ DEV_SLUG=$slug}"
  else
    echo "FAIL: $label FE did not become ready in time — see ${log}" >&2
    # Don't leave the api (and vite) orphaned when the FE readiness poll fails.
    kill "$be_pid" "$fe_pid" ${worker_pid:+"$worker_pid"} 2>/dev/null || true
    exit 1
  fi
  ;;

stop)
  if [[ -z "$slug" ]]; then
    # The mirror of `up`: bare dev-stop clears the machine, so `make dev-stop`
    # is a promise you can trust before starting anything else. Stray databases
    # are left alone unless DROP=1 asks for them — stopping is not deleting.
    sweep_stacks
    echo "stopped every dev stack (freed :$api_port :$fe_port)"
    if [[ "$drop" == "1" ]]; then
      drop_stray_dev_dbs
      echo "note: the shared 'margince' database is kept — DROP=1 only removes the per-slug ones" >&2
    fi
    exit 0
  fi
  if [[ -f "$state" ]]; then
    # shellcheck disable=SC1090
    . "$state"
    kill "${BACKEND_PID:-}" "${FE_PID:-}" "${WORKER_PID:-}" 2>/dev/null || true
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
      psql_owner postgres -c "DROP DATABASE IF EXISTS \"${db}\" WITH (FORCE)" >/dev/null 2>&1 || true
      echo "dropped ${db}"
    fi
  fi
  ;;

*)
  echo "usage: dev.sh {up|stop} [slug] [--drop]" >&2
  exit 2
  ;;
esac
