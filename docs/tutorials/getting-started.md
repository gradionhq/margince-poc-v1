# Getting started

This tutorial takes you from a fresh clone to a running Margince
instance with a bootstrapped workspace, using only the repository's
Makefile targets.

## Prerequisites

- Go ≥ 1.26 (the module pins toolchain `go1.26.4`)
- Docker (dev Postgres 16 + Redis 7 run as containers)
- `golangci-lint` (only needed for `make check`)

All targets exist at the repo root (a thin delegator) and in `backend/`;
the commands below work from either directory.

## 1. Start the databases

```sh
make db-up
```

This starts a `pgvector/pgvector:pg16` container on port 55432 and a
`redis:7` container on port 56379, waits for Postgres to accept
connections, and applies `scripts/db-init.sql` (which creates the
runtime app role — the API never runs as the schema owner).

## 2. Apply the migrations

```sh
make migrate
```

Runs `cmd/migrate up` with the owner DSN: all core migrations plus the
fork-owned custom namespace. Migrations are reversible; see
[how-to/apply-migrations.md](../how-to/apply-migrations.md).

## 3. Run the API

```sh
make dev
```

`make dev` brings up the infra, re-runs db-up + migrate, API-seeds the demo
workspace, and boots `cmd/api` on `:8080` with the app-role DSN (plus the
Vite SPA on `:5173`). By default the outbox relay runs inline in the api
process, so this one command is a complete install; it returns when ready
and the servers run in the background — stop them with `make dev-stop`.

## 4. Bootstrap a workspace

Open <http://localhost:5173> — the Vite/React web UI (it proxies `/v1`
to the api on :8080). The first screen lets you bootstrap a workspace
(name, slug, your admin user). After login you have people, leads, the
deal board, and the activity timeline.

Prefer the API? The same bootstrap is `POST /v1/workspaces`. A ready-made demo workspace already exists —
`make dev` API-seeds `demo-workspace` (`admin@demo.test` / `demo-password-123`) on boot; `make seed-dev`
re-runs the same idempotent seed if you need it.

Then log in over the API and reuse the session. The `crm_session` cookie is `Secure`, so pull it out of
the login response rather than relying on curl's jar; local calls also need the `X-Workspace-Slug`
header (production resolves the workspace from the subdomain):

```sh
SESSION=$(curl -sS -D - -o /dev/null http://localhost:8080/v1/auth/login \
  -H 'X-Workspace-Slug: demo-workspace' -H 'Content-Type: application/json' \
  -d '{"email":"admin@demo.test","password":"demo-password-123"}' \
  | sed -n 's/^[Ss]et-[Cc]ookie: crm_session=\([^;]*\).*/\1/p' | tr -d '\r')

curl http://localhost:8080/v1/me -H 'X-Workspace-Slug: demo-workspace' --cookie "crm_session=$SESSION"
```

(An agent uses a passport instead of a session — see [how-to/mint-a-passport.md](../how-to/mint-a-passport.md).)

## 5. Verify your setup

```sh
make check
```

is the merge gate (build, vet, lint, arch-lint, unit tests, contract
drift). With the containers from step 1 running,

```sh
make test-integration
```

runs the real-Postgres lane: RLS gates, the governed-agent-writes loop,
and the HTTP end-to-end sales flow. It fails loudly when the database is
missing — it never skips.

## Where next

- **Contributing to the backend? Start here:**
  [explanation/backend-onboarding.md](../explanation/backend-onboarding.md) — the orientation hub (map,
  reading order, how to add an endpoint or a migration).
- Connect an AI agent: [how-to/mint-a-passport.md](../how-to/mint-a-passport.md),
  then [how-to/run-the-mcp-server.md](../how-to/run-the-mcp-server.md).
- Every flag and environment variable: [reference/configuration.md](../reference/configuration.md).
- Why the code is shaped the way it is: [explanation/architecture.md](../explanation/architecture.md).
