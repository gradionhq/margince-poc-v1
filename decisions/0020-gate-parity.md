# 0020 — Gate parity with the foundation skeleton (worklist §1b)

Date: 2026-07-09. Implements PR C of
[docs/worklists/skeleton-baseline-2026-07-09.md](../docs/worklists/skeleton-baseline-2026-07-09.md)
§4.4: the skeleton's gate suite ported onto this repo's layout. PR A (#28)
brought the mechanical batch, PR B (#29) the dev experience; this record
ratifies the gate-design decisions PR C embodies.

## 1. Stricter golangci: expanded set, new-code-only (the §1b DECISION)

The worklist flagged a decision: adopt the skeleton's ~25-linter ruleset
wholesale (a big-bang backlog burn-down over ~14 modules) or gated to new
code. **Ratified: new code only, via a second config.**

- `backend/.golangci.yml` (baseline) is UNCHANGED and stays repo-wide. The
  depguard module DAG, the shared-layer default-deny, gosec, misspell, and
  revive package-comments are boundary/security invariants — an invariant
  diff-scoped is an invariant with holes, so the baseline must never gain
  `new-from-*`.
- `backend/.golangci.strict.yml` carries the skeleton's expanded set
  (gocritic, staticcheck-all, funlen/cyclop/gocognit/maintidx/dupl/nestif,
  rowserrcheck/sqlclosecheck/bodyclose/noctx/contextcheck, forcetypeassert,
  nilnil/nilerr, forbidigo, goconst/unparam/ireturn/tagliatelle/predeclared,
  nolintlint, gofumpt+gci as formatters) with
  `issues.new-from-merge-base: origin/main`: only findings introduced after
  the merge-base fail. It also lints the `integration`/`livesmoke` build
  tags, which the baseline never covered.
- `make lint` runs both passes. Locally the strict pass skips loudly if
  `origin/main` is unfetched; in CI the checkout uses `fetch-depth: 0`, so
  the anchor always exists (on a main push the merge-base is HEAD and the
  pass is trivially green — PRs are where the gate bites).
- Consequence to accept: the pre-existing backlog is paid down only as
  files are touched. That is the point — the skeleton's ruleset lands
  today instead of after a burn-down nobody scheduled.

## 2. Contract breaking-change gate: hard by default

`scripts/check-contract-breaking.sh` (root `make contract-breaking-check`,
in root `make check` and CI) runs pinned oasdiff over
`origin/main:backend/api/crm.yaml` vs the working tree and **fails on
ERR-severity (breaking) changes**; additive/deprecation changes pass. The
skeleton defaults this gate to advisory ("pre-live") until a first external
consumer exists; we default to **hard** — this repo is becoming the OSS
baseline and its MCP/REST surface is exercised by the frontend and agents
today. A deliberate breaking re-sync from the spec repo runs with
`CONTRACT_STABILITY=pre-live` (printed, not blocking) for that run, on the
explicit judgment of whoever lands the sync. In CI,
`CONTRACT_BREAKING_REQUIRE_BASE=1` turns a missing base ref (shallow
checkout regression) into a failure instead of a silent skip.

## 3. TS type drift joins the merge gate

`frontend/src/api/schema.d.ts` is generated from `crm.yaml` (`pnpm gen:api`)
but was never drift-gated — this PR found it 400+ lines stale, proving the
gap. `make frontend-check` now regenerates and `git diff --exit-code`s the
file, so a contract change that skips regeneration fails the frontend lane
(locally and in CI, which runs the same target). The regenerated file is
committed with the contract change, same rule as the Go `make drift` gate.

## 4. Test-lane separation, enforced

`scripts/check-test-lanes.sh` (root `make test-lanes`): no untagged test
file may carry real-infrastructure markers (`pgxpool.New`,
`sql.Open("pgx"/"postgres")`, the `MARGINCE_TEST_*` env reads,
`redis.ParseURL`); real-infra suites carry `//go:build integration` (or
`livesmoke`). Fakes carry none of the markers and pass. This turns the
lane convention `make test` has relied on into a gate.

## 5. Zero-skip integration lane

`make test-integration` now runs `-v`, captures the output, and fails if
any `--- SKIP` appears. The lane's harness already `t.Fatal`s on a missing
DSN ("fails loudly, never skips"), but that was convention — a future
`t.Skip` would have turned a security lane (RLS, erasure) silently green.
Now it reads as red.

## 6. File-length cap with a ratchet

`scripts/check-go-file-length.sh` (root `make go-file-length`): hard
500-LOC cap on hand-written Go (`backend/`, `dev/`; generated and `_test.go`
files exempt; `cli/craft` exempt as foundation-vendored).
`scripts/go-file-length-waivers.txt` freezes pre-existing offenders at
their current length — shrinking allowed, growing not; at ≤500 the entry
must be removed so the cap re-arms. Seeded with the single current
offender, `backend/internal/compose/report.go` (501). The worklist's named
offenders (`people/person.go`, `people/lead.go`, `deals/offer.go`) had
already been split under 500 by the Strojny workstream.

## 7. Container images pinned by digest

The compose dev stack and CI's service containers rode floating tags
(`pgvector/pgvector:pg16`, `redis:7`). Both now pin `tag@sha256:<digest>`
using the multi-arch INDEX digest (one pin serves arm64 dev machines and
amd64 CI). `scripts/check-image-pins.sh` grew a second, fail-closed pass:
every non-comment `image:` line in workflows and
`infra/docker-compose.dev.yml` must carry `@sha256:`; commented-out lines
(the parked MinIO block) are skipped. Renovate keeps digest bumps flowing;
bump tag+digest together, everywhere or nowhere.

## Not adopted here (tracked in the worklist)

`check-doc-style` waits on the §0 spec-reconciliation decision; the
parallel integration harness waits until lane time hurts; the LLM craft
review CI job is a §1e DECISION.
