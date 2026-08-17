# Apply migrations

Schema changes ship as embedded SQL migrations in two namespaces:
`backend/migrations/core/` (upstream-owned) and
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
MARGINCE_OWNER_DSN="<the owner DSN>" go run ./cmd/migrate up
```

The DSN reaches the command through the environment rather than argv — it carries
a password, and argv is world-readable. The recipe announces its target with the
credential stripped (`postgres://***@localhost:15432/margince`), so `migrate-down`
says which database it is about to revert.

## Direct invocation

```sh
MARGINCE_OWNER_DSN=<owner-dsn> migrate up
MARGINCE_OWNER_DSN=<owner-dsn> migrate down --steps 1
```

`--dsn <owner-dsn>` still works and takes precedence; prefer the environment so
the credential stays out of the process list.

- `up` applies every pending core + custom migration.
- `down` reverts the most recent `--steps` migrations (default 1).
  Migrations are written reversible, but treat `down` as a dev tool —
  shipped core migrations are additive-only and are never edited.

With no `--dsn`, the DSN comes from `MARGINCE_OWNER_DSN`, else `MARGINCE_DSN`.
The owner variable takes precedence because every verb here runs DDL — RLS
policies, roles and triggers need owner privileges to create — while
`MARGINCE_DSN` is the app role elsewhere in the product (`NOSUPERUSER
NOBYPASSRLS`, no DDL rights). `MARGINCE_DSN` remains the last resort for an
installation running everything under one sufficiently-privileged credential.

An explicitly empty `--dsn ""` is refused rather than falling through to the
environment, so a wrapper passing an unset variable aborts instead of running
`down` or `drop-db` against whatever the ambient DSN names.

## Writing a migration

Follow this checklist — several obligations are enforced by fitness tests, so missing one fails
`make check` / `make test-integration` rather than shipping a latent bug.

1. **Scaffold the pair** — `make migrate-create NAME=<name>` writes
   `<unix-seconds>_<name>.up.sql` **and** `.down.sql` in `backend/migrations/core/`. Both halves are
   mandatory (the runner rejects a missing `.down.sql`). The version is the clock, not the next
   number in a sequence: two branches open at once pick the same *number* and `main` stops loading
   the moment both merge, which is how core `0240` and then `0248` were each claimed twice. The
   four-digit sequence `0001`–`0284` is closed, not renamed — those versions are recorded in every
   database that applied them, and renaming one strands that database.
   **Never edit a shipped core migration** — additive migrations only; extend a `CHECK` vocabulary
   with a new migration rather than rewriting the old one. (The runner is
   hand-rolled — one transaction per migration under a cluster-wide advisory
   lock — because the core/custom/jurisdiction ownership namespaces don't fit
   an off-the-shelf one-dir-one-table migrator.)

   A stamp removes collisions, not the ordering rule: a migration must still sort after everything
   on `origin/main`, so a branch that sat while another migration merged re-runs
   `make migrate-create` and moves its SQL across. `make check` reports that
   (`scripts/check-migration-versions.sh`).
2. **Tenant tables** carry `workspace_id uuid NOT NULL REFERENCES workspace(id)` with `ENABLE`+`FORCE`
   row-level security + an isolation policy, and composite same-workspace foreign keys — the RLS
   coverage integration test derives these from the live schema and fails any table that misses them.
3. **Keep enums in sync** — a new `CHECK (col IN (...))` that a Go enum mirrors means extending that Go
   const set, or `enumsync_test.go` fails.
4. **Reach erasure + SAR** if the table holds PII (`piicoverage_test.go`), and record the table in the
   owning module's `doc.go` "Tables owned" list (`tableownership_test.go`).
5. **Apply and verify** — `make migrate`, then `make check` / `make test-integration`.

Fork-local schema goes in `backend/migrations/custom/`, which has its own tracking table and applies
after core (`YYYYMMDDHHMMSS`-named, `x_`-prefixed columns) and survives upstream merges untouched.
