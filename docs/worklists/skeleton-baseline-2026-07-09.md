# Worklist — skeleton delta & OSS-baseline readiness (2026-07-09)

Goal: make **this repo** (`margince-poc-v1`) the baseline for the official
open-source Margince repository, absorbing what the foundation skeleton
(`../margince-foundation/skeleton/`) has that this repo lacks.

Full comparison run 2026-07-09 across four axes (gates/tooling, backend,
frontend, CI/hygiene). This file is the classified delta plus the sequenced
plan. Companion decision items are flagged `DECISION`.

## 0. Provenance — read this first

The skeleton is **not** an ancestor of this repo, and this repo was not built
from it. Both descend from the *older frozen* `margince-poc`:

```text
margince-poc (frozen 2026-07-02)
  ├─ harvested → margince-foundation/skeleton   (pruned scaffold + new gate/governance tooling)
  └─ (independent) margince-poc-v1 (this repo)  (full rebuild on the ADR-0054 layout, spec-first)
```

Consequences:

- The skeleton's *backend shape is older* than ours (unified `directory`
  module, `kernel/crmctx`, `golang-migrate`, one fat `cmd/api`, no
  `internal/compose`). Our layout is the one the spec mandates (ADR-0054/A69
  as amended). **Backend architecture: this repo wins; do not port the
  skeleton's shape.**
- The skeleton's *tooling, gate suite, and governance are newer* than ours
  (19-gate `make check`, craft gate at code_version 3 vs our vendored 1,
  SHA-pinned CI, docker-compose infra, seed/verify-boot). **Tooling &
  gates: the skeleton mostly wins; port selectively.**
- The foundation's dark-factory design (`tooling/spec-gate/DESIGN.md`)
  assumes the skeleton is the seed a build repo *clones*. Making this repo
  the baseline inverts that. `DECISION (founder)`: ratify the inversion, and
  reconcile `margince-foundation/specs/` (recipes, architecture chapters,
  subsystem docs) with **this** repo's actual architecture — today parts of
  that spec tree describe the skeleton's shape (`directory`, `crmctx`,
  golang-migrate), which this codebase does not have. Shipping the OSS repo
  with a spec that describes a different codebase is the single biggest
  readiness risk.

## 1. Adopt from the skeleton (this repo lacks it)

### 1a. Quick wins — no design decisions

- [x] **`make craft-sync`** — vendored `cli/craft` is at `code_version: 1`;
      the skeleton (the upstream source) is at **3**. Six files differ
      (`static/{checks,ast,runner,render,finding}.go`, `main.go`). Sync,
      restamp `craft-manifest.sha256`, confirm `make craft-drift` green.
      (Done PR A. Upstream's committed manifest is stale at its own HEAD for
      five files — restamped here with the foundation's own recipe; upstream
      fix noted in `feedback/2026-07-09-skeleton-craft-manifest-stale.md`.)
- [x] **`.env.template`** — port/adapt the skeleton's exhaustive template
      (DB, Redis, server, blobstore, BYOK keys, log level/format…). We have
      none; `docs/reference/configuration.md` is the flag table to mirror.
      (Done PR A — this repo's actual env surface only; blobstore/keyvault
      vars wait on their ADRs, §1c.)
- [x] **`infra/docker-compose.dev.yml`** — we have *no* compose file;
      `make db-up` hand-runs containers. Port the skeleton's stack
      (Postgres `pgvector/pgvector:pg16`, `redis:7-alpine`, named volumes).
      MinIO: see 1c (blobstore decision) — include it commented-out or gate
      it on that decision.
      (Done PR B — this repo's port/role contract kept (55432/56379,
      margince_owner + scripts/db-init.sql); `make db-up`/`db-init`/`clean`
      rewired onto compose as the ONE path, legacy `fable-*` containers
      auto-removed; project name is repo-specific so stale poc-era volumes
      can't shadow initdb; MinIO commented-out pending the §1c ADR.)
- [x] **SHA-pin all GitHub Actions** in `.github/workflows/ci.yml` (today
      only the sonar job is SHA-pinned) and add the skeleton's
      `check-image-pins.sh` as a gate so it stays pinned. (Done PR A —
      root `make check-image-pins`, a prerequisite of the root merge gate
      and a deterministic-gates CI step.)
- [x] **`concurrency:` cancel-in-progress groups** on every workflow (we
      have none; every foundation workflow has them). (Done PR A; main runs
      are never cancelled.)
- [x] **`make tools` bootstrap target** — one-shot pinned install of
      golangci-lint / go-arch-lint / oasdiff / migrate etc. (skeleton
      `tools-go`). We `go run` some pinned tools ad-hoc but a fresh machine
      has no bootstrap. (Done PR A — golangci-lint/go-arch-lint/govulncheck,
      the binaries our gates actually pin today; oasdiff joins with the
      PR C breaking-change gate, and we don't use golang-migrate.)
- [x] **`config/ai-routing.example.yaml`** — port as the documented example
      for our `modules/ai` routing (profiles/tiers/fallback ladders).
      (Done PR A — rewritten to our schema: tiers/embeddings/profile; the
      task ladders are code-side in `modules/ai/tasks.go`. A fitness test
      keeps the example parseable.)

### 1b. Gate parity — port these gates into `make check`

Skeleton gates we lack entirely (each is a small script; wire into
`backend/Makefile check` or root):

- [ ] `contract-breaking-check` — **oasdiff** severity gate on `crm.yaml` vs
      `origin/main` (we only have whole-file drift on generated Go).
- [ ] **TS type drift** — `frontend/src/api/schema.d.ts` is generated but
      *not* drift-gated; a `crm.yaml` change can silently strand the
      frontend types. Fold `pnpm gen:api` + `git diff --exit-code` into the
      gate (skeleton: `gen-types.sh check`).
- [ ] `go-file-length` — hard 500-LOC cap (non-test, non-generated). We
      already carry known >500 offenders (`people/person.go`,
      `people/lead.go`, `deals/offer.go`) — adopt diff-scoped or with a
      ratchet/waiver list so the gate lands without a big-bang split.
- [ ] `test-lanes` — hermetic-unit-lane check (no untagged test opens real
      PG/Redis). We rely on the `//go:build integration` convention with no
      enforcement.
- [ ] `check-image-pins` — see 1a.
- [ ] **Stricter `.golangci.yml`** — skeleton runs ~20 linters incl.
      gofumpt+gci, gocritic, staticcheck, funlen/cyclop/gocognit, errcheck,
      rowserrcheck/sqlclosecheck/bodyclose, forcetypeassert, nolintlint;
      ours is `default: standard` + 4. `DECISION (build)`: adopt wholesale
      (big backlog burn-down) vs adopt with `new-from-rev` so only new code
      is gated. Recommend `new-from-rev`.
- [ ] `check-doc-style` / subsystem-doc conventions — only if we adopt the
      foundation's subsystem-chapter format for `docs/` (ties to §0
      spec-reconciliation decision).
- [ ] **Zero-skip integration enforcement** — our lane's "fails loudly
      without a DB" claim is convention; skeleton scripts assert a skipped
      test fails the run. Cheap to add.
- [ ] Consider skeleton's **parallel integration harness** (per-package
      throwaway DB clones) — ours is `-p 1` serial; adopt when lane time
      hurts, not before.

### 1c. Backend platform — adopt behind decisions

- [x] **Seed harness + boot verification** — port `backend/seed/dev.sql`
      (+`reset.sql`) shape and `scripts/verify-boot.sh` (login as seeded
      admin → assert seeded people → FE build). We have programmatic test
      fixtures but no `make seed-dev`, no curl-level boot proof, and our
      demo-workspace bootstrap is manual. This also fixes the documented
      local-run friction (bootstrap rate-limit, MARGINCE_ENV=dev).
      (Done PR B — deliberately NOT the skeleton's SQL-fixture shape: the
      seed (`scripts/seed-dev.sh`, `make seed-dev`) drives the public API,
      because bootstrap is a cross-module transaction, role policies are
      compiled-in Go documents, passwords are salted Argon2id, and raw SQL
      would bypass the audit+outbox write shape. Idempotent via the
      natural-key 409s; only bootstraps when login fails, so re-runs never
      touch the 3/hour limiter. `scripts/seed-reset.sql` (`make seed-reset`)
      wipes the demo workspace dynamically over all `workspace_id` tables.
      `scripts/verify-boot.sh` (`make verify-boot`) proves login + seeded
      people + FE build against `/v1` (cookie `crm_session`,
      X-Workspace-Slug). Demo credentials stay the established convention:
      `demo-workspace` / `admin@demo.test` / `demo-password-123`.)
- [ ] `DECISION (ADR)` **blobstore** — skeleton has `platform/blobstore`
      (MinIO + memory) and MinIO in compose; we have none, and STATUS lists
      "transcript/blob-storage substrate" as an open arc. Adopting the
      skeleton's seam is the natural move; needs an ADR + spec touchpoint.
- [ ] `DECISION (ADR)` **keyvault** — skeleton `platform/keyvault`
      (local+config) for connector secrets; we keep secrets in env/DB.
      Relevant to the capture-connection vault reshape already queued
      (EP05 §B) — evaluate together.
- [ ] `DECISION` **River job queue** — skeleton uses River for durable
      background work; we run custom worker loops (`cmd/worker`,
      `--reconcile-interval`, `--close-date-interval`). Ours works and is
      simpler; River buys durability/retries/observability. Not urgent;
      revisit when a job needs at-least-once durability beyond the outbox.
- [ ] **Prometheus `client_golang`** — we hand-roll the `/metrics` text
      exposition (`platform/httpserver/observe.go`). Fine for now; consider
      the library when metric count grows. Low priority.
- [ ] **Env-root fitness test** — skeleton's `env_guard_test.go` proves one
      file is the sole `os.Getenv` root. Cheap, catches config sprawl;
      port the idea against `internal/compose`/cmd config.
- [ ] **Pack-boundary proof** — if jurisdiction packs ever become separate
      Go modules (skeleton's `crm-de` shape), port `pack_boundary_test.go`.
      Today our `ports/jurisdiction` + internal `modules/de` is
      spec-mandated; no action.

### 1d. Frontend — adopt behind decisions

Skeleton FE is a scaffold (1 real page) but with better *infrastructure*:

- [ ] `DECISION (frontend)` **routing** — react-router v7 + `ProtectedRoute`
      vs our custom hash router. Ours works across ~19 screens; migration is
      churn without user-visible gain. Recommend: keep hash router for now,
      note as tech-debt candidate.
- [ ] **RBAC primitives** — skeleton's `adminOnly` rail gating, `FieldGuard`
      masking, `RoleBadge`. We have no client-side role gating. Port the
      *concepts* into our shell/nav (small, real value).
- [ ] `DECISION (frontend)` **Storybook + component test lane** — skeleton
      has 10 stories + storybook-in-vitest browser project + `ui-shots`
      capture. We have none. Valuable for the design-system surface; adds a
      toolchain. Recommend adopt when the design system stabilizes.
- [ ] **Design-token purity gates** — `ds-purity` (no raw hex/px),
      `font-lock`, `icon-lint`. We already have token conformance *tests*;
      the skeleton's are cheap greps wired as gates. Port and adapt to our
      token names.
- [ ] `DECISION (frontend)` **Forge design system** — skeleton vendors
      `@shared/{token,ui}` (Forge); we have a bespoke ~740-LOC design
      system. Which is the product's DS of record? Ties to the foundation's
      web-design-system chapter.
- [ ] `DECISION` **the second SPA** — `backend/web/static/` (691-line
      vanilla embedded SPA) duplicates the Vite app's job. For an OSS
      baseline, two frontends is confusing. Decide: keep as the embedded
      zero-toolchain fallback (document why) or retire it and embed the
      Vite build output.

### 1e. CI / repo hygiene

- [x] **Live-boot CI job** (skeleton `skeleton-ci.yml` G0 shape): real
      docker-compose up → migrate → seed → verify-boot → build. Our CI uses
      GH `services:`; a compose-based boot job additionally proves the
      compose file + seed + boot script stay honest. Add once 1a/1c land.
      (Done PR B — ci.yml `live-boot` job runs the README quickstart
      literally. Not yet a required check: adding it to branch protection is
      a live-settings + `infra/branch-protection.json` change, decide after
      a few runs prove it stable.)
- [ ] **Branch-protection deltas** — ours is stricter overall; skeleton has
      nothing we lack here. No action beyond keeping
      `infra/branch-protection.json` mirroring live.
- [ ] `DECISION` **LLM craft review job** — skeleton CI runs `craft review`
      (Anthropic-judged, advisory, inline PR comments) + the
      CRAFT-FIX/CRAFT-DISPUTE marker loop. We run deterministic
      `craft static` (blocking) + Claude review agents outside CI. Adopting
      the advisory CI judge is optional polish; needs an API key secret.

## 2. Keep from this repo (skeleton lacks it — do NOT regress)

For completeness — everything here stays: BUSL-1.1 `LICENSE` + SPDX header
gate, `SECURITY.md`, `CHANGELOG.md`, `renovate.json`, SonarCloud
(+`sonar-project.properties`), `.editorconfig`, `.tool-versions`,
govulncheck job, real integration CI lane (RLS + erasure + HTTP e2e),
Playwright+axe e2e, `bench-perf`, golden-eval toolchain (`gen-evals`),
`gen-agentpolicy` fail-closed contract lint, chi-server codegen, the
fitness-test suite (table-ownership, PII/consent/enum/RBAC/write-shape/
concurrency-guard/license), `.githooks/` pre-commit+pre-push,
`.claude/agents/{craft-reviewer,security-redteam}`, four process-role
binaries, `internal/compose`, storekit, `platform/{auth,events,netguard,
ratelimit}`, the core/custom migration namespaces (ADR-0017), i18n (en/de),
theme toggle, command palette, signup flow, IMAP capture.

## 3. OSS-publication sanitization (neither repo covers this)

Before pushing this repo to the official public org:

- [ ] **Scrub internal narrative** — `STATUS.md` (77 KB of session logs,
      internal names/emails, spend-limit notes), `scratchpad/` references,
      review-loop narration. Either truncate STATUS to a public-safe status
      or move history out of the tree. The foundation just did the same
      scrub on the skeleton (commit `837484a` "scrub private decision IDs
      and hosting claim from public-facing text") — mirror that standard.
- [ ] **Fix machine-specific paths** — `CLAUDE.md`/`AGENTS.md` point the
      spec at `/Users/lars/develop/margince/specs`; repoint at the
      foundation spec tree (or its public home) once §0 is decided.
- [ ] **CONTRIBUTING.md** — rewrite for external contributors: adopt the
      skeleton's OSS framing (welcome, A39 disclosure asymmetry,
      `Assisted`/`Generated` AI-disclosure levels) on top of our
      gate/DCO content.
- [x] **README** — ours is internal-build-flavored; add the skeleton-style
      "boot it / log in / verify" quickstart (depends on 1a compose +
      1c seed). (Done PR B — the quickstart; the broader
      internal-flavor scrub stays with PR E.)
- [ ] **decisions/ + feedback/ audit** — decisions/ is committed history;
      review for private references before public push. `feedback/` is
      git-ignored (fine).
- [ ] **git history** `DECISION (founder)` — the history contains internal
      session narration in commit messages. Publish full history vs
      squash-import into the public repo.

## 4. Suggested sequencing

1. **Ratify §0** (founder): poc-v1 is the baseline; spec tree reconciliation
   owned spec-side. Everything else is safe to start meanwhile.
2. **PR A (mechanical):** craft-sync v3 + SHA-pin actions + concurrency +
   image-pins gate + `.env.template` + `make tools`.
3. **PR B (dev experience):** docker-compose + seed harness +
   verify-boot + README quickstart.
4. **PR C (gate parity):** oasdiff breaking gate + TS drift gate +
   test-lanes + zero-skip + golangci `new-from-rev` expansion +
   file-length ratchet.
5. **PR D (frontend):** RBAC primitives + token-purity gates; Storybook/DS
   decisions separately.
6. **PR E (OSS packaging):** CONTRIBUTING/README/STATUS scrub + path fixes;
   then the publication decision (history, org, name).
7. **ADR track (parallel):** blobstore, keyvault, River, second-SPA,
   Forge-DS, LLM-review-job.
