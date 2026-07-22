# CI pipeline

The merge gate as GitHub Actions. The workflow is
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml); this document
explains **how it is wired and why** — the job graph, the change classifier
that decides which jobs run, and how coverage flows into SonarCloud.

`make check` on its own runs only the no-database lane, so the
tenant-isolation and GDPR-erasure fitness tests (`//go:build integration`,
they need a real Postgres) never blocked a PR locally. CI runs **both** lanes
plus the vulnerability scan, the craftsmanship gate, and the frontend + UAT
lanes as required checks — so a migration that forgets `FORCE RLS`, an
erasure that misses a PII table, a vulnerable dependency, a swallowed error,
or a UI regression fails the merge instead of shipping.

## Triggers

- `pull_request` (`opened`, `synchronize`, `reopened`, `ready_for_review`)
- `push` to `main`
- `workflow_dispatch` (manual)

One live run per ref (`concurrency` group): a new push cancels the stale run —
except on `main`, where a merge must not kill the in-flight check of the
previous merge.

## Run only the checks a change can affect

The first job, **`changes`**, classifies the diff (dorny/paths-filter,
SHA-pinned) into three scopes; every downstream job gates on the relevant
output. A required job skipped this way still counts as passing.

| Scope | Paths | Gates |
|---|---|---|
| `backend` | `backend/**`, `infra/**`, `go.work[.sum]`, `Makefile`, `scripts/**`, `.github/workflows/**`, `AGENTS.md`, `sonar-project.properties` | Go build/gate, craftsmanship, integration, vuln |
| `frontend` | `frontend/**`, `backend/api/**` (the contract drives FE types) | frontend lane, UAT |
| `e2e` | `backend/**`, `frontend/**`, `infra/**` | full-stack live-boot |

Consequences:

- A **docs-only PR** matches no scope → every code gate skips.
- A **backend-only PR** skips the frontend + UAT lanes; a **frontend-only PR**
  skips the Go build/gate + the integration lane.
- **Draft PRs run nothing** until marked ready (`draft == false` guards every
  job) — the swarm pushes many WIP commits.
- `craft-residue` is the deliberate exception: it runs on **every** non-draft
  change (docs included) so a leaked `CRAFT-FIX`/`CRAFT-DISPUTE` marker in any
  file fails the merge.

## Job graph

```
changes ──┬─> deterministic-gates ──> craftsmanship
          ├─> integration-shards (×12) ─────┬─> integration (fan-in) ──┐
          ├─> integration-unit-coverage ────┘                          │
          ├─> vuln                                                     │
          ├─> frontend ──> uat                                         │
          ├─> live-boot                                                │
          v                                                            v
        deterministic-gates + integration + frontend ──────> sonarcloud
  dco  (PR-only, independent)
  craft-residue  (every non-draft change, independent)
```

Two deliberate shapes here. The Playwright `uat` lane is **fail-fast**: it
starts only after the cheaper `frontend` gate (biome + vitest + tsc + build)
passes. The real-Postgres integration lane is the opposite — it runs **beside**
`deterministic-gates`, not behind it: it is the longest lane in the pipeline,
so serializing the two slowest jobs dominated PR wall-clock, and a broken
build is still caught by `deterministic-gates` itself. And the lane is
**sharded**: twelve matrix runners each execute a deterministic per-test slice
(package-level splitting would floor at the heaviest package,
`compose/integration`), and the `integration` fan-in reassembles them into the
one required check.

## The jobs

| Job | What it enforces |
|---|---|
| `changes` | The scope classifier above (always runs first, on non-draft) |
| `dco` | Every PR commit carries a Developer Certificate of Origin sign-off (`scripts/check-dco.sh`). PR-only |
| `deterministic-gates` | `make check-backend`: build, vet, lint (baseline + new-code strict), arch-lint, unit + root fitness tests (incl. `audit_log` enum coherence + the contract `$ref` pre-flight), generated-drift, and the script gates (image pins, contract-breaking, test-lanes, file-length, RLS store-path, jurisdiction isolation). Fetches full history so the diff-scoped gates have a base ref |
| `craftsmanship` | `make craft-static` (blocker-only). Runs **after** `deterministic-gates` — a red build is never judged on style |
| `craft-residue` | No unresolved `CRAFT-FIX`/`CRAFT-DISPUTE` markers reach `main` |
| `integration shard (k/12)` | `make test-integration` with `INTEGRATION_SHARD=k/12`: a deterministic per-test round-robin slice of the whole integration lane. Slices are count-based, not duration-based; the heavy e2e tail lands on whichever shard draws it, and `INTEGRATION_JOBS=16` (the tests wait on Postgres, not cores) lets that shard chew through its slice instead of running minutes over its siblings. Boots the dev compose stack (`make db-up`: digest-pinned Postgres 16 (pgvector) + Redis 7 + MinIO + the app role — one stack definition, no hand-mirrored GH services); each shard builds its own migrated `margince_test` template and clones per package. Uploads its slice manifests + binary coverage pods |
| `integration unit coverage` | The unit `-cover` pass over every package, binary coverage pods only. Needed because the shards run just the integration-tagged packages, and without it SonarCloud would see the unit-only packages at a false ~0% new-code coverage. No services (the test-lanes gate guarantees untagged tests open no real DB) |
| `integration` | The fan-in — and the required check, under the same name the single-runner lane carried, so branch protection is unchanged. Asserts every shard + the unit pass succeeded (a failed shard must turn this check red, not skipped), then `scripts/test-integration-reconcile.sh` proves the slices add up: every shard present, identical discovery, union complete + disjoint. Merges all coverage pods into `coverage.out`, uploads `go-coverage` |
| `vuln` | `make vuln` (govulncheck over all packages) |
| `frontend` | `make frontend-check` (biome + vitest + tsc + Vite build) + a Storybook catalog build (stories must compile & register). Emits `fe-coverage` (lcov) |
| `uat` | `make frontend-e2e`: the AC-`<screen>`-N screen-acceptance criteria as named Playwright tests + axe WCAG 2.2 AA + the 390px no-horizontal-scroll sweep + the PERF-1 record-open budget. Mocks the API at the network edge, so it is self-contained |
| `live-boot` | The README quickstart run literally: compose up → migrate → api → `seed-dev` → `verify-boot`. Keeps the API-driven seed and the boot proof honest — the integration shards never boot the api or run the seed script, so those would rot invisibly without this job |
| `sonarcloud` | The CI-based scan (below) |

## Coverage → SonarCloud

The `sonarcloud` job runs **last** and does **not** re-run any suite. It
downloads the coverage artifacts the `integration` fan-in (Go, `coverage.out`,
merged from the shard + unit binary pods) and `frontend` (lcov) jobs already
produced, then runs only the scanner — so there is no second
Postgres/Redis/MinIO stack and no duplicated test run.

Why CI-based rather than SonarCloud's Automatic Analysis: the scanner reads the
committed [`sonar-project.properties`](../sonar-project.properties)
(exclusions + rule tuning + coverage report paths), so that file is the single
source of truth for analysis scope. Disable Automatic Analysis in SonarCloud →
project → Administration → Analysis Method so the two don't compete.

Wiring details:

- The scan step is guarded on the `SONAR_TOKEN` secret. With no token it is a
  clean no-op (green); with the token present it runs and posts the required
  **"SonarCloud Code Analysis"** check.
- The job is **not** gated by the `changes` path filter — the required check
  must post on every ready PR, or a path-skipped job would block doc-only PRs
  forever. Its `needs` condition admits `success` **or** `skipped` for each
  upstream (an area-scoped skip produced no artifact; the scan proceeds
  without it), but a real `failure` of `deterministic-gates` skips the scan so
  it never posts a green check over a broken build.

## Security posture

- `permissions: contents: read` at the workflow root (least privilege; no job
  pushes).
- `persist-credentials: false` on the checkouts of the jobs that execute
  PR-authored code (the `integration` shards, unit-coverage pass and fan-in,
  `live-boot`, `frontend`, `uat`) — so a
  malicious PR running `make test-integration` / `make frontend-e2e` can't read
  the persisted `GITHUB_TOKEN`. The diff-scoped gate jobs
  (`deterministic-gates`, `craftsmanship`, `craft-residue`) keep the token on
  purpose: they diff against `origin/main` and need it to fetch.
- Every `uses:` and container `image:` is pinned to an immutable SHA (the
  `check-image-pins` gate enforces it).
