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
| `dev` | Full local stack: db-up + migrate + `cmd/api` + `cmd/worker` (always on: the outbox relay + Surface-B runner) + API-seed + the Vite SPA, on `http://localhost:5173` (api `:8080`). Returns when ready; the servers run in the background. `DEV_SLUG=<slug>` gives an isolated `margince_dev_<slug>` on slug-derived ports (two worktrees at once). Reads an optional Anthropic BYOK key from `.env.local` for the live cold-start read-back |
| `dev-stop` | `make dev-stop [DEV_SLUG=<slug>] [DROP=1]` — stop the stack started by `make dev` and free its ports; `DROP=1` also drops an isolated `margince_dev_<slug>` database |
| `mcp-inspector` | `make mcp-inspector TOKEN=mgp_… [DEV_SLUG=<slug>] [WORKSPACE=<slug>]` — build `cmd/mcp` and open the MCP Inspector over stdio against the running `make dev` stack. Token comes from `TOKEN=` or `MARGINCE_PASSPORT_TOKEN` in `.env.local`; the DSN is read from the running stack (so `DEV_SLUG` just works). Token + DSN pass through the environment only, never argv |
| `db-up` / `infra-up` | Start the dev Postgres 16 (pgvector, port 55432) and Redis 7 (port 56379) containers, create the app role (`infra-up` is an alias) |
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
| `check` | **The merge gate.** Backend `make check` = build + vet + lint + arch-lint + test + drift. Root `make check` runs that **plus** the craft-doc floor, image pins, contract breaking-change (`oasdiff`), test-lane hygiene, and the file-length ratchet |
| `build` | `go build ./...` |
| `vet` | `go vet ./...` |
| `test` | Unit tests; the root fitness tests (license header, write shape, architecture, enum sync, `audit_log` enum coherence, contract `$ref` resolution) run uncached |
| `test-integration` | Real-Postgres lane (`-tags integration`): RLS gates, governed-agent loop, HTTP end-to-end. Runs on its own `margince_test` namespace, never the dev `margince` DB, so it can run concurrently with `make dev`. **Parallel** — each package runs on its own throwaway clone db (`CREATE DATABASE … TEMPLATE margince_test`) + private MinIO bucket + its own Redis logical db (1..15 by slot; db 0 stays reserved for `make dev`), so packages share nothing; within a package still `-p 1`. Fails loudly without a database — never skips. Tune concurrency with `INTEGRATION_JOBS=N` |
| `test-db-up` | (Re)build the migrated `margince_test` template the parallel lane clones from |
| `test-it` | Run ONE integration package on a throwaway clone (+ own MinIO bucket + Redis db 15): `make test-it DIR=backend/internal/modules/people [RUN=TestName]` |
| `e2e-siteread` | (backend Makefile) Deep-read quality floor vs the real gradion.com (`-tags e2e_llm`): paid, network, opt-in. Judge a candidate model with `MARGINCE_E2E_MODEL=provider:model` (+ its BYOK key) or `MARGINCE_AI_ROUTING`; every assertion is a floor — a different model must extract the same or better to pass |
| `e2e-ai` | Certify AI tasks against the corpus (`-tags e2e_llm`, `TestE2ECertify`): paid, network, opt-in. Defaults `MARGINCE_AI_ROUTING` to `config/ai-routing.yaml` (the file `make install` seeds) so it runs out of the box; narrow with `TASK=<task>`, override the candidate with `MODEL=<provider:model>`, repeat with `RUNS=<n>`. Fails loudly — never skips — without a routing file or a corpus match |
| `e2e-ai-report` | Print the task×provider×model certification matrix (verdict, reliability, score p50, latency p50) from the committed records under `backend/internal/compose/aicert/records/`. `go run`-only dev tool (`internal/compose/aicert/reportcmd`), not a shipped binary. No records yet (nothing certified) prints a one-line hint instead of an empty table |
| `test-integration-serial` | Escape hatch: the old sequential lane on the shared `margince_test` DB (for debugging a parallel-isolation issue) |
| `lint` | `golangci-lint run` (depguard, gosec, misspell, revive, gofmt) |
| `arch-lint` | go-arch-lint over `.go-arch-lint.yml` — a hard gate on the import DAG |
| `gen` | Regenerate everything derived from `api/crm.yaml` (contract types, 501 stubs, agent-policy table) |
| `drift` | `gen`, then fail if any generated file changed — the contract drift gate |
| `gen-workflow` | `make gen-workflow NAME=<snake_case_handler_name>` — scaffold a new automation `workflow.Handler` + its test stub (write-once; refuses to overwrite an existing scaffold). See [how-to/create-a-workflow.md](../how-to/create-a-workflow.md) |

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
| `storybook` | The component workbench on `:6006` — the design-system catalog and the story surface `fe-uat` renders. Stories live beside their component as `<name>.stories.tsx` |
| `fe-uat` | Change-scoped Storybook render+capture UAT for frontend-only diffs: renders THIS branch's changed component's stories in headless Chromium and screenshots them (no live stack, no DB). Fails on an unclean render, an unregistered story, or a changed component with no story. Artifact: `.tmp/fe-uat/manifest.json`. Deliberately **not** in `make check` — the fe-only UAT lane a coordinator runs instead of the full stack. `ARGS="--allow-missing"` |

## Isolated stack per worktree

`make dev DEV_SLUG=<slug>` runs a full stack that won't collide with another
worktree's: the ONE shared infra (Postgres/Redis on `55432`/`56379`), but a
private database `margince_dev_<slug>` and api/FE ports derived
deterministically from the slug (the FE's `/v1` proxy follows the api via
`BACKEND_PORT`). Logs + stop handle live under `.tmp/dev/<slug>/`. Bare
`make dev` uses the shared `margince` database on the base `:8080`/`:5173`
ports. Stop either with `make dev-stop [DEV_SLUG=<slug>] [DROP=1]`.

## Root-only (craftsmanship gate)

| Target | What it does |
|---|---|
| `craft-static` | Full deterministic craftsmanship sweep of `backend/` (the pre-push hook runs the diff-scoped subset) |

## Variables

`GO`, `PG_PORT` (55432), `REDIS_PORT` (56379), `DB_NAME` (margince),
`OWNER_DSN`, `APP_DSN` — all overridable (`make migrate PG_PORT=5432`).
The Makefile exports `MARGINCE_ENV=dev` and the `MARGINCE_TEST_*`
variables so tests find the dev containers.
