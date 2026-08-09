# CLAUDE.md — operating this repo

This file provides guidance to Claude Code (claude.ai/code) when working in this
repository. It is the long form: the full operating detail lives here.
[AGENTS.md](AGENTS.md) is a deliberately shorter digest for other agent
harnesses that links back here — the two are **not** copies, so do not sync
them line by line. AGENTS.md is machine-read, though, so keep it accurate:
`cli/craft` feeds the **whole** nearest AGENTS.md into the gate prompt
(`gate.Assembler.nearestAgents` walks up from the touched directories; this root
file is the only one in the tree today), and `make check-craft-doc` separately
asserts that **AGENTS.md** still carries a `## Craftsmanship` heading. Nothing
gates this file's own copy — keep the two in agreement by hand. When a rule
below changes, decide whether the digest needs it too.

Margince CRM implementation PoC (WP0 foundation + WP1 core spine). This is the
**build repo** — the running Go software. The *specification* lives in a separate
repo (see below); this code is built **from** that spec, contract-first.

## Where the spec is (read before building)

The normative spec is the sibling repo **`margince-foundation`**; its key trees
(paths relative to that repo's root):

- **`specs/README.md`** — what the spec tree is and the three rules that keep it
  true (nobody edits downstream artifacts; upstream changes arrive only through a
  spec change; versions pin).
- **`specs/contract/`** — implementation source-of-truth:
  `crm.yaml` (OpenAPI 3.1), `interfaces.md` (incl. the §0 error-sentinel registry),
  `ai-operational-spec.md`, `formulas-and-rules.md`, `data-semantics.md`,
  `seed-and-fixtures.md`.
- **`specs/subsystems/`** — the per-capability chapters, one per bounded module
  (`capture.md`, `people-and-organizations.md`, `deals-and-pipeline.md`, …). This is
  where a module's behaviour, formulas, DDL pins, and ACs actually live — start here
  when building a module.
- **`specs/architecture/`** — the build blueprint, named (not numbered) files:
  `architecture.md`, `data-model.md`, `code-organization.md`, `event-bus.md`,
  `api-conventions.md`, `runtime-config.md`, `frontend.md`, …
- **`specs/use-cases/`** — `UC-*.md`, the end-to-end stories with their acceptance
  criteria and failure modes.
- **`specs/quality/`** — `craftsmanship.md` (the anti-tell catalog T1–T11 the
  Craftsmanship section below cites), `threat-model.md`, `testing.md`,
  `acceptance-standards.md`, `quality-gates.md`.
- **`specs/product/`** — `principles.md` (P1–Pn), `journeys.md`, `personas.md`, `scope.md`.
- **`specs/adr/`** — `DECISIONS.md` (the locked index) + `ADR-*.md`;
  **ADR-0054/A69** mandates this repo's layout (amended 2026-07-04 —
  four `cmd/<role>` binaries + the §9 single-tx exception).
- **`tooling/delivery-board.md`** — at the spec repo **root**, not under `specs/` —
  how the team tracks delivery. The `backlog/` ticket tree that used to sit beside
  it was retired on 2026-07-22; a chapter's ACs and the subsystem text are the
  work definition now, so don't go looking for a ticket per chapter.

Two traps when reading the spec: `specs/spec/` is a dead stub (a stale
`__pycache__`) — ignore it. And chapters carry `derives-from:` pins to older paths
(e.g. `specs/spec/architecture/15-code-craftsmanship.md`); per `specs/README.md`
rule 3 those resolve in **git history**, not the working tree — the content has moved.

**Contract-first (principle P3): when this code and the spec disagree, the spec wins.**
Product name **Margince** is locked; older docs say "Gradion CRM" — same product.
The spec is under active cleanup by another session: some docs still show the old
`crm-*` layout. Don't edit the spec from here — raise discrepancies for
upstream reconciliation.

**Start at [STATUS.md](STATUS.md)** — progress, in-flight work, and the session-pickup
point; update it at the end of every working session. Route findings as you work:
implementation decisions are recorded in the commit and PR that makes the change
(git history is the record); spec/ticket defects are reconciled upstream against
the spec (contract-first, P3), never worked around in this source; anything found
but **not** fixed in the current change — a bug, a gap, a follow-up task — becomes
a GitHub issue in this repo. When to file is the engineer's call. This repo is
public, so an issue carries no private spec paths, no local machine paths, no
secrets; cite the spec by chapter/ADR/pin ID. How the team tracks issues beyond
this repo is internal: see the spec repo's `tooling/delivery-board.md`.

## Build / test / seed

All Go code lives under `backend/` (one Go module,
`github.com/gradionhq/margince/backend`); the root Makefile delegates there.

```
make install            # one-shot fresh-worktree setup: FE deps + gate tools + hooks
make db-up              # start PG16 + Redis 7 containers, create the app role
make migrate            # apply core + custom migrations (owner DSN)
make check              # the full merge gate = check-backend + check-fe
make check-backend      # backend half; this is what CI's deterministic-gates runs
make check-fe           # frontend half (biome + vitest + tsc + build)
make test-integration   # real-Postgres lane: RLS gates + HTTP end-to-end (needs db-up).
                        # Ends with `OK: integration passed with 0 skips` — it fails
                        # loudly without a database rather than skipping silently
make dev                # full local stack: the app on :8080, worker always on
make dev-stop           # stop the stack
make dev-logs           # follow the stack log, coloured per process
```

What each target actually runs, plus `check-q`, `check-go`, `fe-typecheck`,
`fe-uat`, `infra-up`/`infra-down`, and the `DEV_SLUG` flags:
[docs/reference/make-targets.md](docs/reference/make-targets.md).

### EXACTLY ONE dev stack at a time (non-negotiable)

**`make dev` enforces this itself — it sweeps before it starts.** A bare
`make dev` kills every margince api/worker/vite on the machine (recorded,
orphaned, or from another checkout), evicts whatever holds :8080, drops every
leftover `margince_dev_*` database, and only then boots ONE stack. So `make dev`
is always safe to run; you no longer stop the old stack by hand. Bare
`make dev-stop` is the mirror and stops EVERY stack; `DEV_SLUG=x` gives an
isolated stack that the sweep spares, until the next bare `make dev` takes it
down. Details and the other targets:
[docs/reference/make-targets.md](docs/reference/make-targets.md).

Two failures this prevents, both of which look exactly like a bug in your code:

- **A stale api still serving :8080.** A binary started from an earlier branch
  keeps answering happily while Vite hot-reloads the code you just wrote. The
  SPA then calls endpoints that binary has never heard of. An old server is
  indistinguishable from a broken feature.
- **A backend change that never reached the browser.** The api is compiled —
  Vite hot-reloads the frontend, the API does not. Every backend change (new
  endpoint, migration, handler fix) needs `make dev` again.

This working tree is often shared with parallel agent sessions that switch
branches under you. Before you trust ANY manual test, confirm both:
`git branch --show-current` is the branch you think it is, and the api on :8080
was started after your last backend change.

The CI pipeline that runs these gates as required checks — the change
classifier, the job graph, and the SonarCloud coverage flow — is documented in
[infra/ci-pipeline.md](infra/ci-pipeline.md).

Three process-role binaries, all wired through
`internal/compose`: `cmd/api` (HTTP; inline outbox relay behind
`--inline-relay`, default true), `cmd/worker` (standalone relay),
`cmd/migrate` (up|down).

MCP (Surface A2): the api serves the governed tool surface at `/mcp`, on the
same origin as `/oauth/*` and the discovery documents — A1 stdio and its
`cmd/mcp` binary are retired (SCR-9). A client needs only the URL:
`claude mcp add --transport http margince <base>/mcp` walks discovery, DCR,
consent and the token exchange itself. `tools/list` advertises only what the
presenting passport's scopes admit. A passport is also a REST Bearer
credential, governed exactly like MCP (ADR-0055, superseding the old
"read-only on REST" C1 rule) — 🟢 mutations auto-execute, 🟡 ones stage for
confirm-first approval, all still capped by the granting human's live
seat/RBAC. Every call re-authenticates: revocation binds mid-session.

Host requirements: Go ≥ 1.26, Docker, and `golangci-lint` (the codegen
tool chain is pure Go, in its own module `backend/tools/`).

One installation serves one organization (A107/ADR-0061): the server resolves
its singleton organization itself — no request selects a tenant. First boot
bootstraps the organization + admin from `margince.yaml`, which `make dev`
seeds from `config/margince.example.yaml` on first run and then **leaves
alone**, so your edits survive a restart; delete it to reset.

`/healthz`, `/readyz`, and `/metrics` sit next to `/v1`. The config file, the
CLI flags and their env equivalents, and the operational endpoints are all
documented in [docs/reference/configuration.md](docs/reference/configuration.md).

## Shipping a change (branch → local gates → PR → green → merge)

Every commit lands through this loop — code, docs, and config alike.
Direct pushes to `main` are blocked by branch protection; there is no
other path to merge.

**Run repository publishing with host access.** In a sandboxed agent session,
`gh auth status` is not authoritative because the sandbox may not see the host
keychain even when the active host account is valid. Run the authentication
check again with host escalation before asking the user to re-authenticate.
Every repository or remote mutation must likewise run with host escalation,
including branch creation, commit, rebase, push, PR create/edit/merge, and PR
check monitoring. Read-only working-tree inspection (`git status`, `git diff`,
`git log`) may stay sandboxed.

1. **Branch off `main`**: `git switch -c <type>/<slug> origin/main`.
2. **Sign off every commit** (`git commit -s`) — the DCO gate rejects a
   PR containing any commit without a `Signed-off-by` trailer.
3. **Local gates BEFORE pushing**: `make check` (the merge gate — build,
   vet, lint, arch-lint, unit tests, contract drift); add
   `make frontend-check` when `frontend/` changed. The pre-push hook
   (installed once via `make hooks` — the **root** target, which sets
   `core.hooksPath`) runs `craft static --strict` diff-scoped on top — a
   BLOCKER or MAJOR finding stops the push; fix it, never bypass the hook.
   When a push does change hand-written backend Go, the hook then also runs the
   two sub-second whole-tree greps (`check-rls-store-path`,
   `check-no-jurisdiction`), so an RLS-bypassing store statement or a
   jurisdiction string in core fails locally rather than in CI. A push with no
   qualifying backend Go changes exits before all three — a docs-only push runs
   none of them.
4. **Push the branch and open a PR** (`gh pr create`).
5. **Watch the GitHub gates and fix red**: CI, DCO, CodeRabbit, and
   SonarCloud must all pass (`gh pr checks <n> --watch`). Fix failures
   locally, re-run the local gates, push again; address CodeRabbit
   findings rather than dismissing them.
6. **Merge only when everything is green** (squash is the house style:
   `gh pr merge <n> --squash`), then delete the branch. Never merge over
   a red or still-running check.

### Never commit machine or session debris

Only product — code, tests, docs, config templates — belongs in a commit.
Before you `git add`, check `git status` for anything that is a build cache,
a working note, or a screenshot, and leave it out:

- **Build caches** — `.pnpm-store/`, `node_modules/`, compiled binaries.
  Regenerable, machine-local, never tracked.
- **Session scratch** — put working notes, plans, and intermediate output in
  the session's scratchpad temp dir, **not** a `scratchpad/` at the repo root.
- **Screenshots / captures** — a `*.png`/`*.jpg` you took to look at something
  is debris unless the product or the repo docs intentionally reference it
  (e.g. imported from `frontend/src/assets/`, or embedded in a docs page).

`.gitignore` catches the known offenders (root-anchored images, `/.pnpm-store/`,
`/scratchpad/`), but the rule is yours to keep — a new debris path it doesn't
yet list must still stay out, and be added to `.gitignore` when you spot it.

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
- `internal/modules/` — twenty bounded capabilities, flat by default per
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
  surface: registry, admission gate, the hosted HTTP transport and its
  JSON-RPC dispatcher, the
  Surface-B loop — reaches records only through the datasource seam),
  `automation` (the closed 7×7 trigger/action catalog, ADR-0035: the
  registry, the per-workspace standing automation store, and the
  deterministic trigger runtime — event matcher and clock time-scan
  converging on one path, gated at both author-time and match-time),
  `ai` (the model runtime behind ports/model: BYOK cloud — native
  anthropic/openai/gemini plus the generic openai_compatible wire —
  local ollama/vllm, the offline fake; routing + budget +
  secret-stripping, and the effective-dated `ai_model_rate` sheet the
  read-side pricer prices calls against — `ai_call` stores tokens, never
  a price), `search`
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
  targets, human-set, workspace-shared config posture), `webhooks`
  (outbound webhook subscriptions + owner-scoped delivery, E10), and `overlay` (the incumbent-CRM mirror: a second
  `datasource.SystemOfRecordProvider` selected per-workspace by
  `workspace.x_sor_mode`, serving mirror-backed reads behind the inner
  `incumbent.Incumbent` seam — fail-closed visibility deny-join,
  budget-metered force-fresh read-through, continuous sync (backfill +
  reconcile poller), disconnect teardown, and the ADR-0071
  overlay→native cutover; `Update`/`Archive` write back incumbent-first
  and re-mirror the returned state, while `Create`/`Merge`/`PromoteLead`/
  `AdvanceDeal` + RunReport are declared `unsupported_by_sor`),
  `comms` (outbound delivery machinery — the durable staging row, the
  transmit-time gates, the provider dispatcher; the message itself is an
  activity), `migration` (the shared importer engine: one classification
  step, one zero-write dry run, one checkpointed resumable run loop,
  with sources and native writers injected as seams).

  Two sanctioned spine shapes, and ONLY two — don't invent a third:
  **Handlers→Store** for CRUD modules (people, deals, activities, …:
  the store owns the transactional write shape and the RBAC gate at its
  entry points) and **Handlers→Service** for engine modules (approvals,
  identity: a service owns the multi-step domain logic and drives the
  SQL inside it).
- `internal/compose/` — the composition layer every process role shares:
  the contract HTTP surface (`Server` embeds every module's handler set and
  asserts `crmcontracts.ServerInterface` itself — a contract operation with
  no real handler fails that assertion at compile time, not a 501 at
  runtime), the composite `datasource.SystemOfRecordProvider`, the MCP registry +
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
  Every interactive control comes from `frontend/src/design-system/` —
  its README is the catalog to read before hand-rolling one, and a native
  `<select>` fails `frontend/scripts/check-native-controls.sh`.
- `extensions/<name>/` — the stable extension tier (ADR-0069): each unit
  is its own Go module importing ONLY the marker-allowlisted
  `backend/pkg/**` surface; presence under `extensions/` is the
  enablement. The vanilla tree ships two first-party units, enabled by
  default: `extensions/de` (the German jurisdiction pack — GoBD
  calendar-year retention floors) and `extensions/yogi` (one served
  🟢/read agent tool — the worked example of the governed-tool kind). `make composition` (run by every build lane)
  generates the ignored `build/composition/` wiring; `composition/` at
  the root is the committed vanilla stub so bare go commands resolve.

## DO NOT TOUCH

- `internal/contracts/api_gen.go`, `internal/compose/stubs_gen.go` —
  generated (`make gen`); the drift gate fails a hand edit.
- `migrations/core/*` that have shipped — additive migrations only. An applied
  version never re-runs, so editing one changes what FRESH installations get
  while every deployed database keeps the old behaviour: the two diverge
  silently. The tenant-scope sweep (see the migration-write rule below) is the
  one authorized exception in this tree's history, and it only holds because it
  shipped WITH additive repair migrations (core `0190`,
  custom `20260806120000`) that reach the already-deployed databases. Editing
  history without that second half is how an installation ends up permanently
  missing a backfill nobody can see is missing.
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

## A migration that writes tenant DATA binds the workspace first

Schema DDL is free, but the moment a migration writes ROWS to a tenant table it
must bind `app.workspace_id` — every one of them carries FORCE row-level
security with deny-on-unset semantics (`0014_rls.up.sql`), and FORCE binds the
table owner, which is exactly the role migrations run as. Unbound, the policy
expression resolves to NULL, and the three ways a migration can write part
company:

- `UPDATE` / `DELETE` are filtered by the policy's `USING` clause: **zero rows,
  reported as success**. The migration records itself as applied and the data
  change is silently gone.
- `INSERT … SELECT` reading a tenant table is filtered on the SOURCE before
  `WITH CHECK` ever runs: **zero rows, reported as success**, the same silence.
- `INSERT` of literal rows is judged by `WITH CHECK`, which the NULL fails, so
  it raises `new row violates row-level security policy` and aborts the
  migration.

Only the loud one announces itself. The rule below is written for the two that
do not. The shape, in every migration that needs it:

```sql
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE <table> SET ...
    WHERE (<the original condition>)
      AND <table>.workspace_id = ws;   -- required, see below
  END LOOP;
END $$;
```

Both halves are mandatory, and they do different jobs. The **binding** makes the
rows visible; it does **not** scope the statement. An executor RLS does not
filter — a superuser or a `BYPASSRLS` role, which is what every dev machine and
CI run as — sees every workspace on every iteration, so without the
**predicate** the write runs once per workspace: survivable for an idempotent
`UPDATE`, a unique violation for an `INSERT`. Bind
inside the loop (hoisting it out names one workspace for all of them), and
qualify the predicate with the statement's own target (an `INSERT … SELECT` names
it on the source alias instead, which is where its `workspace_id` comes from).

`workspace` itself is outside RLS, which is what lets the loop enumerate it, and
`set_config`'s third argument keeps the binding transaction-local. **Nothing
about this is visible in development**: the dev owner is the Postgres
container's `POSTGRES_USER`, a superuser, and FORCE does not reach a superuser
or a `BYPASSRLS` role — the policy is simply not applied to them — so an
unbound write works on every developer's machine and does nothing in
production. Two gates hold the line, and both must stay honest:
`TestTenantWritesInMigrationsAreWorkspaceScoped` (unit, reads every migration)
and the RBAC upgrade replay in the integration lane, which migrates as a
deliberately NON-SUPERUSER owner (`migrations/migrationrole_integration_test.go`).

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

**The gate runs before every push (diff-scoped), and it is STRICT.**
`.githooks/pre-push` runs the deterministic arm — `craft static --strict` (the repo's
`cli/craft` tool, ADR-0045) — over the Go files **this push changes vs
`origin/main`** in `backend/`, `extensions/` and `fixtures/` alike (a first-party
extension unit ships the same product). There is no pre-existing backlog to exempt: the whole tree was
cleared to zero findings before this bar was armed, so the rule is simply that
touched code is clean. Write it right the first time — a swallowed error, a sleep
in a test, a bare `any` in a signature, or an 81-line function you add will block
your push.
- Install the hook once after cloning: **`make hooks`** (sets `core.hooksPath=.githooks`).
- Full manual sweep of every hand-written Go tree (`backend/`, `extensions/`,
  `fixtures/`): **`make craft-static`** — green, and the
  CI `craftsmanship` job runs the same bar as a required check.
- `BLOCKER` and `MAJOR` findings both block; `MINOR` is advisory. The size ceilings
  are 80 CODE lines / 500 file lines for product code and 160 / 1000 for `*_test.go`
  — a long scenario test that sets up, acts and asserts once is not the
  god-function smell, but a suite still splits when it stops being navigable.
  A comment-only line is not length: the ceiling asks how much a reader must hold
  at once and an explanation reduces that, which is also what keeps this check
  agreeing with golangci's `funlen` (configured here `ignore-comments`).
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
list from the tree — `backend/`, `extensions/` and `fixtures/`, since each
unit is its own module — so a new file that skips the header fails the gate.
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
6. **A test that supplies its own version of production proves nothing
   about production** — hand-inserted rows the real writer never writes,
   or a hand-copied adapter mirroring what compose wires. Seed through
   the real writer; if a test needs the wiring, reach for the wiring
   (integration tests live directly in `package compose` so unexported
   adapters are in scope). An unexpectedly uncovered new file usually
   means a test double stands where the real thing should.

