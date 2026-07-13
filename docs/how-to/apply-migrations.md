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

Follow this checklist — several obligations are enforced by fitness tests, so missing one fails
`make check` / `make test-integration` rather than shipping a latent bug.

1. **Create the next sequential pair** in `backend/migrations/core/`: `NNNN_<name>.up.sql` **and**
   `NNNN_<name>.down.sql`. Both halves are mandatory (the runner rejects a missing `.down.sql`).
   **Never edit a shipped core migration** — additive migrations only; extend a `CHECK` vocabulary
   with a new migration rather than rewriting the old one. (The runner is
   [decisions/0002](../../decisions/0002-hand-rolled-migration-runner.md).)
2. **Tenant tables** carry `workspace_id uuid NOT NULL REFERENCES workspace(id)` with `ENABLE`+`FORCE`
   row-level security + an isolation policy, and composite same-workspace foreign keys — the RLS
   coverage integration test derives these from the live schema and fails any table that misses them.
3. **Keep enums in sync** — a new `CHECK (col IN (...))` that a Go enum mirrors means extending that Go
   const set, or `enumsync_test.go` fails.
4. **Reach erasure + SAR** if the table holds PII (`piicoverage_test.go`), and record the table in the
   owning module's `doc.go` "Tables owned" list (`tableownership_test.go`).
5. **Apply and verify** — `make migrate`, then `make check` / `make test-integration`.

Fork-local schema goes in `backend/migrations/custom/`, which sorts after core (timestamp-named,
`x_`-prefixed columns) and survives upstream merges untouched.
