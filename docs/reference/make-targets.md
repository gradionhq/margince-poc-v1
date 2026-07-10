# Make targets

The real Makefile is `backend/Makefile`; the root Makefile delegates the
common backend targets and adds the frontend lane. In `backend/`, `make`
(or `make help`) lists targets with descriptions; `help`, `vuln`, and
`hooks` are backend-only (`make -C backend <target>` from the root).

## Everyday

| Target | What it does |
|---|---|
| `help` | List targets (the default goal) |
| `install` | One-shot fresh-worktree setup (frontend deps + Go gate binaries + git hooks). The factory's `worktree-init` runs this by name |
| `dev` | db-up + migrate + `cmd/api` on `:8080` |
| `db-up` / `infra-up` | Start the dev Postgres 16 (pgvector, port 55432) and Redis 7 (port 56379) containers, create the app role (`infra-up` is a skeleton-compatible alias) |
| `db-init` | (Re)apply `scripts/db-init.sql` to the running Postgres |
| `migrate` | Apply core + custom migrations with the owner DSN |
| `infra-down` | Stop the dev containers but keep the data volumes |
| `clean` | Remove the dev containers **and** the data volumes |

## Factory-compatibility golden commands

These are the target names the dark-factory tooling, its UAT runner, and its
UAT guides call by name (`docs/target-minimum-setup.md §3`). `check-q`,
`check-go`, and `fe-typecheck` are the quiet/scope-aware gate variants;
`test-integration` ends with the literal `OK: integration passed with 0 skips`.

| Target | What it does |
|---|---|
| `check-q` | Quiet `make check` — full log in `.tmp/check.log`, excerpt on failure |
| `check-go` | The Go half of the gate (`make -C backend check`) |
| `fe-install` / `fe-typecheck` | Frontend deps install / `tsc` typecheck (scope-aware FE gates) |

## Gates

| Target | What it does |
|---|---|
| `check` | **The merge gate**: build + vet + lint + arch-lint + test + drift |
| `build` | `go build ./...` |
| `vet` | `go vet ./...` |
| `test` | Unit tests; the root fitness tests (license header, write shape, architecture, enum sync, `audit_log` enum coherence, contract `$ref` resolution) run uncached |
| `test-integration` | Real-Postgres lane (`-tags integration`): RLS gates, governed-agent loop, HTTP end-to-end. Fails loudly without a database — never skips |
| `lint` | `golangci-lint run` (depguard, gosec, misspell, revive, gofmt) |
| `arch-lint` | go-arch-lint over `.go-arch-lint.yml` — a hard gate on the import DAG |
| `gen` | Regenerate everything derived from `api/crm.yaml` (contract types, 501 stubs, agent-policy table) |
| `drift` | `gen`, then fail if any generated file changed — the contract drift gate |

The root `make check` runs the backend gate above **and** these deterministic
root gates (each is a small script; all merge-blocking):

| Target | What it does |
|---|---|
| `check-image-pins` | Every workflow `uses:` and container `image:` is pinned to an immutable ref |
| `contract-breaking-check` | oasdiff severity gate on `api/crm.yaml` vs `origin/main` (breaking change fails; additive passes) |
| `test-lanes` | Hermetic-unit-lane check: no untagged test opens a real Postgres/Redis |
| `go-file-length` | Hard 500-LOC cap on hand-written Go, ratcheted via `scripts/go-file-length-waivers.txt` |
| `rls-store-path` | No `internal/modules` statement addresses the superuser pool directly (RLS bypass); `// rls-exempt: <reason>` is the escape for a genuinely cross-workspace query |
| `no-jurisdiction` | No country-specific regulatory identifier (XRechnung/ZUGFeRD/DATEV/…) or ISO-3166 code in core **code**, only in the jurisdiction seam (`internal/modules/de`, `internal/shared/ports/jurisdiction`); statute citations in comments are allowed |

## Occasional

| Target | What it does |
|---|---|
| `vuln` | govulncheck over all packages (not yet part of `check`; CI wiring comes later) |
| `hooks` | Install `scripts/pre-commit` (gofmt + license-header test) into git's resolved hooks dir (honors `core.hooksPath`) |

## Root-only (frontend lane)

| Target | What it does |
|---|---|
| `frontend-check` | `pnpm install --frozen-lockfile && pnpm check` in `frontend/` (needs node + pnpm) |
| `frontend-e2e` | The screen-acceptance UAT harness: AC-named tests + 390px sweep + axe WCAG 2.2 AA + perceived-perf budgets, against the built app over the seed mock (`BASE_URL=…` targets a live backend). Wired into CI as the `uat` job |
| `frontend-dev` | `pnpm install && pnpm dev` in `frontend/` |
| `storybook` | The component workbench on `:6006` — the design-system catalog and the story surface `fe-uat` renders. Stories live beside their component as `<name>.stories.tsx` |
| `fe-uat` | Change-scoped Storybook render+capture UAT for frontend-only diffs: renders THIS branch's changed component's stories in headless Chromium and screenshots them (no live stack, no DB). Fails on an unclean render, an unregistered story, or a changed component with no story. Artifact: `.tmp/fe-uat/manifest.json`. Deliberately **not** in `make check` — the fe-only UAT lane a coordinator runs instead of the full stack. `ARGS="--allow-missing"` |

## Root-only (isolated UAT env, per worktree)

A live UAT stack that won't collide with another worktree's: the ONE shared
infra (Postgres/Redis on `55432`/`56379`), but a private database
`margince_uat_<slug>` and api/FE ports derived deterministically from the slug.

| Target | What it does |
|---|---|
| `uat_env` | `make uat_env UAT_SLUG=<slug>` — create + migrate + API-seed `margince_uat_<slug>`, then boot the api and Vite on slug-derived ports (the FE's `/v1` proxy follows the api via `BACKEND_PORT`). Logs + stop handle under `.tmp/uat/<slug>/` |
| `uat_env_stop` | `make uat_env_stop UAT_SLUG=<slug> [DROP=1]` — stop the servers and free the ports; `DROP=1` also drops the database |

## Root-only (craftsmanship gate)

| Target | What it does |
|---|---|
| `craft-static` | Full deterministic craftsmanship sweep of `backend/` (the pre-push hook runs the diff-scoped subset) |
| `craft-drift` | Verify the vendored `cli/craft` matches the foundation's `craft-manifest.sha256` — runs as a `check` prerequisite; a local gate edit fails it |
| `craft-sync` | Pull the current gate (source + manifest) from `../margince/skeleton/cli/craft` over the vendored copy |

## Variables

`GO`, `PG_PORT` (55432), `REDIS_PORT` (56379), `DB_NAME` (margince),
`OWNER_DSN`, `APP_DSN` — all overridable (`make migrate PG_PORT=5432`).
The Makefile exports `MARGINCE_ENV=dev` and the `MARGINCE_TEST_*`
variables so tests find the dev containers.
