# Apply migrations

Schema changes ship as embedded SQL migrations in two namespaces
(ADR-0017): `backend/migrations/core/` (upstream-owned) and
`backend/migrations/custom/` (fork-owned — upstream never writes there).
`cmd/migrate` applies both, in order, with the **owner-role** DSN; the
runtime app role never owns schema.

## The golden path

```sh
make db-up    # once: start the dev Postgres and create the app role
make migrate  # apply everything pending
```

`make migrate` runs:

```sh
go run ./cmd/migrate up --dsn "postgres://margince_owner:dev@localhost:55432/margince"
```

## Direct invocation

```sh
migrate up   --dsn <owner-dsn>
migrate down --dsn <owner-dsn> --steps 1
```

- `up` applies every pending core + custom migration.
- `down` reverts the most recent `--steps` migrations (default 1).
  Migrations are written reversible, but treat `down` as a dev tool —
  shipped core migrations are additive-only and are never edited.

`--dsn` falls back to `MARGINCE_DSN`. Point it at the owner role: RLS
policies, roles, and triggers need owner privileges to create.

## Writing a migration

- Upstream (this repo) changes go in `backend/migrations/core/` with the
  next sequential number; **never edit a shipped core migration** —
  additive migrations only.
- Fork-local schema goes in `backend/migrations/custom/`, which sorts
  after core and survives upstream merges untouched.
- Every tenant table must carry `workspace_id` with `ENABLE`+`FORCE`
  row-level security and composite same-workspace foreign keys — the
  integration lane derives these obligations from the live schema and
  fails any table that misses them.
