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

`make dev` re-runs db-up + migrate and then boots `cmd/api` on `:8080`
with the app-role DSN. By default the outbox relay runs inline in the
same process, so this one command is a complete install.

## 4. Bootstrap a workspace

Open <http://localhost:8080> — the embedded web UI. The first screen
lets you bootstrap a workspace (name, slug, your admin user). After
login you have people, leads, the deal board, and the activity timeline.

Prefer the API? The same bootstrap is `POST /v1/workspaces`. Local API
calls need the workspace header (production uses the subdomain):

```sh
curl http://localhost:8080/v1/me -H 'X-Workspace-Slug: <slug>' --cookie 'crm_session=<token>'
```

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

- Connect an AI agent: [how-to/mint-a-passport.md](../how-to/mint-a-passport.md),
  then [how-to/run-the-mcp-server.md](../how-to/run-the-mcp-server.md).
- Every flag and environment variable: [reference/configuration.md](../reference/configuration.md).
- Why the code is shaped the way it is: [explanation/architecture.md](../explanation/architecture.md).
