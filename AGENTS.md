# AGENTS.md — operating this repo

Margince CRM implementation PoC (WP0 foundation + WP1 core spine), built from the
sibling spec repo **`margince-foundation`** (contract-first, P3: when this code and
the spec disagree, the spec wins). Its tree is `specs/` — `specs/subsystems/` holds
the per-module chapters, `specs/contract/` the normative contract, `specs/adr/` the
decisions; the ticket backlog is at that repo's root in `backlog/`. See
[CLAUDE.md](CLAUDE.md#where-the-spec-is-read-before-building) for the full map.
Don't edit the spec from here — raise discrepancies for upstream reconciliation.

**Start at [STATUS.md](STATUS.md)** — progress, in-flight work, and the
session-pickup point; update it at the end of every working session.
Route findings as you work: implementation decisions are recorded in the
commit and PR that makes the change (git history is the record); spec/ticket
defects are reconciled upstream against the spec (contract-first, P3), never
worked around in this source.

## Build / test / seed

All Go code lives under `backend/` (one Go module,
`github.com/gradionhq/margince/backend`); the root Makefile delegates there.

```
make install            # one-shot fresh-worktree setup: FE deps + gate tools + hooks
make db-up              # start PG16 + Redis 7 containers, create the app role
make migrate            # apply core + custom migrations (owner DSN)
make check              # the full merge gate = check-backend + check-fe
make check-backend      # backend half: build, vet, lint (baseline + new-code
                        # strict), arch-lint, unit + fitness tests, contract drift,
                        # plus the root script gates (craft drift, image pins,
                        # contract-breaking, test-lanes, file-length, rls-store-path,
                        # no-jurisdiction). This is what CI's deterministic-gates runs.
make check-fe           # frontend half (biome + vitest + tsc + build)
make test-integration   # real-Postgres lane: RLS gates + HTTP end-to-end (needs db-up).
                        # Parallel — each package on its own throwaway clone db; ends
                        # with `OK: integration passed with 0 skips`, never skips silently
make dev                # full local stack: the app on :8080 (api behind it on :18080)
                        # (worker = cmd/worker, always on: outbox relay + Surface-B runner)
                        # (DEV_SLUG=x → isolated margince_dev_<slug> on slug-derived ports)
make dev-stop           # stop the stack (add DEV_SLUG=x [DROP=1] for an isolated env)
```

### EXACTLY ONE dev stack at a time (non-negotiable)

**`make dev` enforces this itself — it sweeps before it starts.** A bare
`make dev` kills every margince api/worker/vite on the machine (recorded,
orphaned, or from another checkout), evicts whatever holds :8080, drops
every leftover `margince_dev_*` database, and only then boots ONE stack on
:8080 against `margince`. So `make dev` is always safe to run; you no
longer stop the old stack by hand.

The failure it removes is the one that does NOT announce itself: an `api`
binary started from an earlier branch keeps serving :8080 happily while Vite
hot-reloads the code you just wrote. The SPA then calls endpoints the running
binary has never heard of, and the app fails in ways that look like your bug
and are not — an old server is indistinguishable from a broken feature.

The api is a compiled binary: **Vite hot-reloads the frontend, the API does
not.** Any backend change — a new endpoint, a migration, a handler fix — needs
`make dev` again (it sweeps and rebuilds). Restarting is the only way your Go
code reaches the browser.

`make dev-fresh` is `make dev` onto a rebuilt database — the first-run
installation again, for when a previous session left data behind.

`make dev-stop` is the mirror: bare, it stops EVERY stack, not just the one it
recorded. The `margince` database survives both (stopping is not deleting);
`DROP=1` removes the per-slug databases only.

`DEV_SLUG=x` still gives an isolated stack (own database, own ports) and is the
one thing the sweep leaves alone — but the next bare `make dev` will take it
down, by design. Tear yours down with `DEV_SLUG=x make dev-stop DROP=1`.

This repo's working tree is often shared with parallel agent sessions that
switch branches under you. Before you trust ANY manual test, confirm both:
`git branch --show-current` is the branch you think it is, and the api on :8080
was started after your last backend change.

`check-q` (quiet), `check-go` (backend-only), `fe-typecheck`, `fe-uat`
(change-scoped Storybook render gate), and `infra-up`/`infra-down` round out
the golden-command set. Full table:
[docs/reference/make-targets.md](docs/reference/make-targets.md). The CI
pipeline that runs these gates as required checks — the change classifier, the
job graph, and the SonarCloud coverage flow — is documented in
[infra/ci-pipeline.md](infra/ci-pipeline.md).

Four process-role binaries, all wired through
`internal/compose`: `cmd/api` (HTTP; inline outbox relay behind
`--inline-relay`, default true), `cmd/worker` (standalone relay),
`cmd/migrate` (up|down), `cmd/mcp` (the A1 stdio server).

MCP (Surface A1): mint a passport (`POST /v1/passports`, session-authed),
then `MARGINCE_PASSPORT_TOKEN=mgp_… mcp --dsn …`
serves the tool surface over stdio. The same token is a REST Bearer
credential; a passport on REST is governed exactly like MCP (ADR-0055,
superseding the old "read-only on REST" C1 rule) — 🟢 mutations
auto-execute, 🟡 ones stage for confirm-first approval, all still capped
by the granting human's live seat/RBAC. Every call re-authenticates:
revocation binds mid-session.

Host requirements: Go ≥ 1.26, Docker, and `golangci-lint` (the codegen
tool chain is pure Go, in its own module `backend/tools/`).

One installation serves one organization (A107/ADR-0061): the server
resolves its singleton organization itself — no request selects a tenant:
`curl http://localhost:8080/v1/me --cookie 'crm_session=…'`. First boot
bootstraps the organization + admin from `margince.yaml` (`--config` /
`MARGINCE_CONFIG`). `make dev` seeds a gitignored `config/margince.yaml`
from `config/margince.example.yaml` on first run and then **leaves it**
(the same create-if-missing / leave-if-exists pattern as
`config/ai-routing.yaml`), so edits — org details, admin, or the
`ai.capture_payloads` posture — persist across `make dev-stop` / `make dev`;
delete it to reset.

Operational surface: `/healthz` (dumb liveness), `/readyz` (dependency
probes; 503 names the unready dependency), and `/metrics` (Prometheus
text: outbox backlog, relay throughput, pool state) sit next to `/v1`.
api, worker, and mcp take `--log-level` (debug|info|warn|error) and
`--log-format` (text|json), env-backed as `MARGINCE_LOG_LEVEL` /
`MARGINCE_LOG_FORMAT`; an invalid value is a boot error, never a silent
default. The full flag/env table:
[docs/reference/configuration.md](docs/reference/configuration.md).

## Shipping a change (branch → local gates → PR → green → merge)

Every commit lands through this loop — code, docs, and config alike.
Direct pushes to `main` are blocked by branch protection; there is no
other path to merge.

1. **Branch off `main`**: `git switch -c <type>/<slug> origin/main`.
2. **Sign off every commit** (`git commit -s`) — the DCO gate rejects a
   PR containing any commit without a `Signed-off-by` trailer.
3. **Local gates BEFORE pushing**: `make check` (the merge gate — build,
   vet, lint, arch-lint, unit tests, contract drift); add
   `make frontend-check` when `frontend/` changed. The pre-push hook
   (installed once via `make hooks`) runs `craft static` diff-scoped on
   top — a BLOCKER finding stops the push; fix it, never bypass the hook.
4. **Push the branch and open a PR** (`gh pr create`).
5. **Watch the GitHub gates and fix red**: CI, DCO, CodeRabbit, and
   SonarCloud must all pass (`gh pr checks <n> --watch`). Fix failures
   locally, re-run the local gates, push again; address CodeRabbit
   findings rather than dismissing them.
6. **Merge only when everything is green** (squash is the house style:
   `gh pr merge <n> --squash`), then delete the branch. Never merge over
   a red or still-running check.

## Layout (spec ADR-0054/A69 as amended: four `cmd/<role>` binaries + the §9 single-tx exception)

The `backend/internal/{modules,platform,shared}` triad — the DAG is
`shared → platform → modules → compose → cmd`, enforced three ways
(depguard, go-arch-lint, `backend/arch_test.go` fitness tests):

- `internal/shared/` — Tier-0 leaves, stdlib-only (test-enforced):
  `kernel/{ids,events,provenance,principal}`, `apperrors` (the fixed
  sentinel registry — extend only with the spec's interfaces.md §0), and
  `ports/{authz,datasource,mcp,connector,workflow,model,retrieval,extraction,fieldcatalog,jurisdiction}`
  (the frozen seam interfaces + additive provider mechanics).
- `internal/platform/` — technical plumbing, owns no domain:
  `database` (pg pool + the RLS `WithWorkspaceTx` GUC contract) +
  `database/storekit` (the ONE spelling of the audit+outbox write shape,
  keyset cursors, version patches), `auth` (the ONE admission point:
  `Admit` (scope ∧ tier) + object RBAC + row-scope clauses incl. the
  activity link-walk), `events` (outbox relay/subscriber/dedupe),
  `dbmigrate`, `httperr` (RFC 7807 + wire helpers), `httpserver` (chassis).
- `internal/modules/` — eighteen bounded capabilities, flat by default per
  ADR-0054 §3 (store + mapping + transport + provider in one package),
  growing subpackages only when a named trigger fires (split for a reason, never symmetry); a module NEVER
  imports a sibling: `identity` (workspaces, users, sessions, passports;
  RBAC policy docs ONLY in `identity/internal/policy`),
  `people` (person, organization, lead + merge + promote —
  cross-aggregate single-tx SQL ownership per the §9 single-tx exception), `deals`
  (deal, pipeline/stage config, workspace seed, won/lost + FX freeze),
  `activities` (the timeline: idempotent logging + polymorphic links),
  `approvals` (the 🟡 confirm-first engine, ADR-0036: staged rows ARE
  the authority object), `agents` (the governed tool
  surface: registry, admission gate, stdio/hosted transports, the
  Surface-B loop — reaches records only through the datasource seam),
  `automation` (the closed 7×7 trigger/action catalog, ADR-0035: the
  registry, the per-workspace standing automation store, and the
  deterministic trigger runtime — event matcher and clock time-scan
  converging on one path, gated at both author-time and match-time),
  `ai` (the model runtime behind ports/model: Anthropic BYOK, Ollama,
  the offline fake, routing + budget + secret-stripping), `search`
  (row-scoped retrieval: FTS + pgvector/RRF hybrid + context graph),
  `capture` (the ONE `connector.Sink`: normalized inbound capture,
  idempotent on the source natural key), `consent` (per-purpose consent
  + the default-deny outbound suppression gate + the DSR case queue),
  `privacy` (the GDPR engines: Art. 17 erasure, Art. 15 SAR assembly,
  the nightly retention evaluator — the ratified cross-store writer,
  gated by `backend/tableownership_test.go`), `collections`
  (lists — static and dynamic segments — and tags, visibility-probed),
  `signals` (the consent-gated warm-room substrate: company-level
  signals, the inspectable resolver, warm/cold join), `customfields`
  (the governed add-field engine: the sole runtime `ALTER TABLE`
  chokepoint; record stores read the `cf_*` columns via the
  `fieldcatalog` seam), `quotas` (RD-T06 owner-XOR-team revenue
  targets, human-set, workspace-shared config posture), `de` (the
  German jurisdiction pack: GoBD retention floors, registered via
  `ports/jurisdiction`), and `overlay` (the incumbent-CRM mirror: a second
  `datasource.SystemOfRecordProvider` selected per-workspace by
  `workspace.x_sor_mode`, serving mirror-backed reads behind the inner
  `incumbent.Incumbent` seam — fail-closed visibility deny-join,
  budget-metered force-fresh read-through, continuous sync (backfill +
  reconcile poller), disconnect teardown; writes +
  RunReport declared `unsupported_by_sor`).

  Two sanctioned spine shapes, and ONLY two — don't invent a third:
  **Handlers→Store** for CRUD modules (people, deals, activities, …:
  the store owns the transactional write shape and the RBAC gate at its
  entry points) and **Handlers→Service** for engine modules (approvals,
  identity: a service owns the multi-step domain logic and drives the
  SQL inside it).
- `internal/compose/` — the composition layer every process role shares:
  the contract HTTP surface (module handlers shadow generated 501 stubs),
  the composite `datasource.SystemOfRecordProvider`, the MCP registry +
  approvals adapter, and the cross-module integration suites (in
  `compose/integration`, with the shared harness). Every cross-module
  edge is injected HERE (identity's workspace seed ← deals; agents'
  staging ← approvals). Cross-module ORCHESTRATION groups live in
  subpackages under the same named-trigger growth policy (`compose/briefs`
  is the pilot); a compose subpackage never durably owns a business
  entity.
- `internal/contracts/` — GENERATED from `backend/api/crm.yaml`. Never edit.
- `backend/api/crm.yaml` — the authoritative OpenAPI 3.1 contract.
- `backend/migrations/core|custom/` — the ADR-0017 namespaces.
  `modules/<name>/custom/` + `migrations/custom/` — the fork-owned seam:
  upstream never writes there (ADR-0054 §7).
- `backend/tools/` — the codegen tool chain (contract-overlay,
  gen-stubs, gen-agentpolicy); its own Go module so the generators'
  dependencies stay out of the product module's go.mod.
- `frontend/` — the Vite/React web UI: a standalone static build served
  separately from the API binary (which serves `/v1` only — no embedded
  SPA); `make frontend-check` / `make dev` exist at the repo root.

## DO NOT TOUCH

- `internal/contracts/api_gen.go`, `internal/compose/stubs_gen.go` —
  generated (`make gen`); the drift gate fails a hand edit.
- `migrations/core/*` that have shipped — additive migrations only.
- RLS policies and the `database.WithWorkspaceTx` GUC contract — every
  tenant query goes through it; there is no raw-pool path for tenant data.
- `internal/shared/apperrors` — the fixed sentinel registry; extend only
  together with the spec's interfaces.md §0.

## The write shape (non-negotiable)

Every mutation commits domain row + `audit_log` row + `event_outbox` row
in ONE transaction — spelled once in `platform/database/storekit`
(`Audit` + `Emit`), called by every module store. `captured_by` is
stamped from the authenticated principal, never from the request body.
The outbox envelope is the `shared/kernel/events` contract (events.md
§2): the HTTP layer mints one `correlation_id` per request, `Audit()`
returns the audit row id, `Emit()` links both into the trace —
publishing is ALWAYS through the outbox (`platform/events.Relay` ships
it; no direct XADD from domain code) and consumers wrap handlers in
`events.Dedupe` because the bus is at-least-once. Every store entry
point is RBAC-gated (`auth.Require` + `auth.EnsureVisible` + the list
scope clauses in `platform/auth`): object denial →
`apperrors.ErrPermissionDenied` (403), row-scope miss →
`apperrors.ErrNotFound` (404, existence-hiding).

## Craftsmanship

Match the spec's `specs/quality/craftsmanship.md` (anti-tell catalog T1–T11). The rule
under every rule:
**code that reads best to a human reads best to the next agent that edits it** —
legibility is the product, not polish.

- Comments say *why*, not *what* (T1). Domain names, not `data/tmp/helper` (T4).
- **Never swallow an error** — no `_ = f()`, no empty `catch`, no ignored return;
  errors flow through the sentinels, and messages are actionable and never leak
  internals (no stack/SQL/table names to a client) (T2).
- No `any`/`as`/unchecked assertions (T6). No dead or speculative code, no
  abstraction without a second concrete caller today, no `TODO` without an issue
  ref (T3/T8).
- Handle the honest hard cases (empty page, version skew, cross-tenant, GUC-unset) (T7).
- **Tests prove behaviour or they are noise (T11):** no assertion-free test (it can
  only fail by panicking), no `time.Sleep` / real-clock / real-network flakiness, no
  over-mocking that asserts call-order; mock only true boundaries (DB/HTTP/clock/queue)
  and inject a `Clock`. Tests read as specs; the integration lane fails loudly without a
  database — a skipped security gate looks exactly like a passing one.
- **Pre-submit self-check:** would a senior write it this way? does it match the
  surrounding file? do the errors say what-went-wrong *and* what-to-do? would a stranger
  find where this change lives without a guide? is this the smallest diff that does the job?

**The gate runs before every push (diff-scoped).** `.githooks/pre-push` runs the
deterministic arm — `craft static` (the repo's `cli/craft` tool, ADR-0045) — over the
backend Go files **this push changes vs `origin/main`**. New/touched
code must be clean; the pre-existing backlog is *not* gated. So write it right the first
time — a swallowed error or a sleep in a test you add will block your push.
- Install the hook once after cloning: **`make hooks`** (sets `core.hooksPath=.githooks`).
- Full manual sweep of the whole backend: **`make craft-static`** (still red on the backlog).
- Only `BLOCKER` findings (`swallowed-errors`, `test-sleep`) block; `MAJOR`/`MINOR` are advisory.
- A *genuine* false positive is waived **in-source with a reason**: `//craft:ignore <check> <reason>`
  (a reasonless waiver is itself a finding).

## License headers (every new hand-written Go file)

Every hand-written `*.go` file starts with the BUSL-1.1 SPDX header — the
two lines at the very top, above the `package` clause, followed by a blank
line:

```go
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion
```

Exempt: generated files (`*_gen.go`) and the drift-frozen
`internal/contracts/` package — do NOT stamp those. The rule is enforced by
`TestEveryHandWrittenGoFileCarriesTheLicenseHeader` in
`backend/license_test.go` (part of `make check`), which derives the file
list from the tree, so a new file that skips the header fails the gate.
Keep the copyright line as-is (`2026 Gradion`); it names the release year,
not the current year. This is the license model's "honest labeling / don't
strip notices" obligation (spec `business/12-license.md` §5, §8).

## Rules learned from the review loop (binding)

Full rationale in [README.md](README.md#engineering-rules-learned-from-the-review-loop);
the short form:

1. **Fix the invariant, not the call site** — grep every mutation/read
   site of the same column/constraint/record and fix them as one change
   (the recurring reviewer catch here was "fixed the case under review,
   missed the sibling copy").
2. **Prefer fitness functions over point fixes** — derive the obligation
   from the system (e.g. every `workspace_id` table must have FORCE RLS;
   every CHECK violation maps to a 4xx; `backend/arch_test.go` derives
   its package lists from the tree), don't maintain it as a list.
3. **Anything that returns a record is a read** and carries the row-scope
   gate — including replay, conflict, and error paths.
4. **No build-process residue in comments** — no review-ticket numbers or
   fix narration; state the invariant so it stands alone. History belongs
   to git, not the source. Same for test names.
5. **Never rationalize a known gap in a comment** — restructure it away
   or gate it with a test.
