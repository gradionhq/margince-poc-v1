# Make targets

The real Makefile is `backend/Makefile`; the root Makefile delegates the
common backend targets and adds the frontend lane. In `backend/`, `make`
(or `make help`) lists targets with descriptions; `help`, `vuln`, and
`hooks` are backend-only (`make -C backend <target>` from the root).

## Everyday

| Target | What it does |
|---|---|
| `help` | List targets (the default goal) |
| `dev` | db-up + migrate + `cmd/api` on `:8080` |
| `db-up` | Start the dev Postgres 16 (pgvector, port 55432) and Redis 7 (port 56379) containers, create the app role |
| `db-init` | (Re)apply `scripts/db-init.sql` to the running Postgres |
| `migrate` | Apply core + custom migrations with the owner DSN |
| `clean` | Remove the dev containers |

## Gates

| Target | What it does |
|---|---|
| `check` | **The merge gate**: build + vet + lint + arch-lint + test + drift |
| `build` | `go build ./...` |
| `vet` | `go vet ./...` |
| `test` | Unit tests; the root fitness tests (license header, write shape, architecture) run uncached |
| `test-integration` | Real-Postgres lane (`-tags integration`): RLS gates, governed-agent loop, HTTP end-to-end. Fails loudly without a database — never skips |
| `lint` | `golangci-lint run` (depguard, gosec, misspell, revive, gofmt) |
| `arch-lint` | go-arch-lint over `.go-arch-lint.yml` — a hard gate on the import DAG |
| `gen` | Regenerate everything derived from `api/crm.yaml` (contract types, 501 stubs, agent-policy table) |
| `drift` | `gen`, then fail if any generated file changed — the contract drift gate |

## Occasional

| Target | What it does |
|---|---|
| `vuln` | govulncheck over all packages (not yet part of `check`; CI wiring comes later) |
| `hooks` | Install `scripts/pre-commit` (gofmt + license-header test) into git's resolved hooks dir (honors `core.hooksPath`) |

## Root-only (frontend lane)

| Target | What it does |
|---|---|
| `frontend-check` | `pnpm install --frozen-lockfile && pnpm check` in `frontend/` (needs node + pnpm) |
| `frontend-dev` | `pnpm install && pnpm dev` in `frontend/` |

## Variables

`GO`, `PG_PORT` (55432), `REDIS_PORT` (56379), `DB_NAME` (margince),
`OWNER_DSN`, `APP_DSN` — all overridable (`make migrate PG_PORT=5432`).
The Makefile exports `MARGINCE_ENV=dev` and the `MARGINCE_TEST_*`
variables so tests find the dev containers.
