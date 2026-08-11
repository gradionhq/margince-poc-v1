# CI pipeline

The merge gate as GitHub Actions. The workflow is
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml); this document
explains **how it is wired and why** — the job graph, the change classifier
that decides which jobs run, and how coverage flows into SonarCloud.

`make check` on its own runs only the no-database lane, so the
tenant-isolation and GDPR-erasure fitness tests (`//go:build integration`,
they need a real Postgres) never blocked a PR locally. CI runs **both** lanes,
plus the craftsmanship gate, the license gate and the frontend lane, as required
checks — so a migration that forgets `FORCE RLS`, an erasure that misses a PII
table, a denied dependency license, a swallowed error, or a UI regression fails
the merge instead of shipping.

Two lanes run but deliberately do **not** block: `vuln` and the SonarCloud scan.
Both were traded off the required set for merge speed during heavy development,
and both are re-checked daily on `main` by `scheduled.yml` — see below for why a
non-blocking gate needs that backstop to stay honest. `uat` and `live-boot` are
likewise advisory.

## Triggers

- `pull_request` (`opened`, `synchronize`, `reopened`, `ready_for_review`)
- `push` to `main`
- `workflow_dispatch` (manual)

One live run per ref (`concurrency` group): a new push cancels the stale run —
except on `main`, where a merge must not kill the in-flight check of the
previous merge.

## Run only the checks a change can affect

The first job, **`changes`**, classifies the diff (dorny/paths-filter,
SHA-pinned) into four scopes; every downstream job gates on the relevant
output. A required job skipped this way still counts as passing.

| Scope | Paths | Gates |
|---|---|---|
| `backend` | `backend/**`, `infra/**/!(*.md)`, `go.work[.sum]`, `Makefile`, `scripts/**`, `extensions/**`, `fixtures/**`, `composition/**`, `.github/workflows/ci.yml`, `.github/actions/**`, `AGENTS.md`, `sonar-project.properties`, `frontend/src/mcp-apps/forbidden.json` | Go build/gate, extension reference, craftsmanship, integration, vuln |
| `frontend` | `frontend/**`, `backend/api/**` (the contract drives FE types) | frontend lane, UAT |
| `e2e` | `backend/**`, `frontend/**`, `infra/**/!(*.md)`, `extensions/**`, `fixtures/**`, `composition/**` | full-stack live-boot |
| `docker` | root `Dockerfile.*`, `.dockerignore` | the three image builds |
| `deps` | `go.work[.sum]`, `**/go.mod`, `**/go.sum`, `**/package.json`, `**/pnpm-lock.yaml`, `.syft.yaml`, `.grant.yaml`, `sbom-schemas/**`, `Makefile`, `.github/workflows/ci.yml`, `.github/actions/**` | the license gate |

Consequences:

- A **docs-only PR** matches no scope → every code gate skips. That includes
  the prose under `infra/` — this file documents the classifier, it is not an
  input to any gate, so the two `!(*.md)` extglobs keep an edit to it from
  booting the twelve-shard integration fleet. Each is written as one positive
  pattern because the action ORs its patterns: a separate `!infra/**/*.md`
  entry would match every path outside `infra/` and fire the filter on
  everything.
- A **backend-only PR** skips the frontend + UAT lanes; a **frontend-only PR**
  skips the Go build/gate + the integration lane — except for
  `frontend/src/mcp-apps/forbidden.json`, which is authored under `frontend/`
  but copied into a Go package under a byte-equality test, so it is classified
  backend too.
- A **CI PR still runs the full backend lane** when it touches `ci.yml`, the
  `Makefile`, or `scripts/**`: those change what a gate *does*, so the gates
  re-run to prove they still pass under the new definition. `patch.yml` and
  `sbom.yml` are outside the scope — neither runs a backend gate, and each
  proves itself when it runs.
- **Draft PRs run nothing** until marked ready (`draft == false` guards every
  job) — the swarm pushes many WIP commits.
- `craft-residue` and `secret-scan` are the deliberate exceptions: both run on
  **every** non-draft change, docs included. A leaked `CRAFT-FIX`/`CRAFT-DISPUTE`
  marker, or a hardcoded credential, can land in any file type — neither can be
  gated on the scope classifier. The **image-pin gate rides in `secret-scan`**
  for the same reason: it reads the whole workflow directory while the `backend`
  scope names one file, so gated on the classifier it would skip on exactly the
  PR that unpins an action in `sbom.yml` or `patch.yml` — and Renovate, which
  bumps `uses:` across all three workflows, auto-merges on green.

## Job graph

```
changes ──┬─> deterministic-gates ──> craftsmanship
          ├─> integration-shards (×12) ─────┬─> integration (fan-in) ──┐
          ├─> integration-unit-coverage ────┘                          │
          ├─> extension-reference ──────────────────────────────────┐  │
          ├─> vuln                                                  │  │
          ├─> license gate  (PR-only, `deps` scope)                 │  │
          ├─> frontend ──> uat                                      │  │
          ├─> live-boot                                             │  │
          ├─> docker-image (×3: api, web, worker) ─> docker-images  │  │
          v                                                         v  v
 deterministic-gates + integration + extension-reference + frontend ──> sonarcloud
  dco  (PR-only, independent)
  craft-residue  (every non-draft change, independent)
  secret-scan    (every non-draft change, independent — + the image-pin gate)
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

## The shared Go build cache

Every Go job restores
[`.github/actions/go-build-cache`](../.github/actions/go-build-cache/action.yml)
before it compiles.

It exists because `actions/setup-go` cannot do this job. Its cache key hashes
only `go.sum`, so the entry is written once and never refreshed — every later
run logs *"Cache hit occurred on the primary key … not saving cache"* and
restores that first snapshot forever. Measured on this repo, that blob is
**~25 MB** while a warm Go build cache is **~550 MB**: the module cache was
being restored, the build cache effectively was not. Seventeen Go jobs per
backend run — the twelve shards, the merge gate, the composed-build lane, the
coverage pass, `live-boot`, `govulncheck` — each compiled the module from
scratch, every run. setup-go still owns the module cache; this action owns the
build cache beside it.

Two flavours, because a build tag and coverage instrumentation change the
package builds themselves and only the dependency builds underneath are common:

| Flavour | Written by | Read by |
|---|---|---|
| `plain` | `deterministic-gates` | `deterministic-gates`, `extension-reference`, `integration unit coverage`, `live-boot`, `vuln` |
| `integration` | `integration shard (1/12)` | all twelve shards |

Three properties worth keeping:

- **Only `main` writes.** Both refresh steps are gated on
  `github.event_name == 'push' && github.ref == 'refs/heads/main'`, so a PR
  restores and never saves. That is what stops twelve shards racing to upload
  the same ~550 MB key, and what stops one PR's cache from reaching the next.
- **Exactly one writer per flavour.** Every shard compiles substantially the
  same set, so a second writer would add nothing but contention — hence the
  `matrix.shard == 1` guard.
- **The key falls back twice.** `…-<deps-hash>-<sha>` → `…-<deps-hash>-` →
  `…-`. Dropping the dependency hash on the last hop is deliberate: Go's build
  cache is content-addressed, so a stale restore is never *wrong*, it only
  misses the entries whose inputs changed. After a dependency bump a
  mostly-warm cache still beats a cold one.

The refresh steps use `!cancelled()` rather than `success()`: a red lint or a
failed test still compiled the tree, and those artifacts are exactly as
reusable as a green run's.

`scripts/check-image-pins.sh` scans `.github/actions/` alongside the workflows.
The `./path` allowance waves a local action through on the grounds that the
repo versions its own code — true of the action's own ref, but not of the
third-party actions it calls, which would otherwise ride in unread.

## The jobs

| Job | What it enforces |
|---|---|
| `changes` | The scope classifier above (always runs first, on non-draft) |
| `dco` | Every PR commit carries a Developer Certificate of Origin sign-off (`scripts/check-dco.sh`). PR-only |
| `deterministic-gates` | `make check-backend`: build, vet, lint (baseline + new-code strict), arch-lint, unit + root fitness tests (incl. `audit_log` enum coherence + the contract `$ref` pre-flight), generated-drift, and the script gates (craft-doc floor, image pins, contract-breaking, test-lanes, file-length, RLS store-path, jurisdiction isolation, and the `backend/pkg` published-surface freeze). Fetches full history so the diff-scoped gates have a base ref |
| `extension-reference` | The composed-build lane (ADR-0069): proves the **empty** extension set still composes byte-identically to the committed `composition/` stub, then enables the reference fixture and runs the backend build + unit lane + `check-composition` against the composed workspace, plus every enabled unit's own module lane. Emits its own coverage profile — extension units are separate Go modules, unreachable by the shard profiles |
| `craftsmanship` | `make craft-static` — strict: BLOCKER **and** MAJOR findings fail it, MINOR is advisory. Runs **after** `deterministic-gates` — a red build is never judged on style |
| `craft-residue` | No unresolved `CRAFT-FIX`/`CRAFT-DISPUTE` markers reach `main` |
| `secret-scan` | `make secret-scan` — gitleaks over a clean `git archive HEAD` export, policy in `.gitleaks.toml`. Scans the **committed** tree, never the working tree: gitleaks does not honour `.gitignore`, so an in-place scan reads sibling worktrees and local `.env` files and reaches a different verdict per machine. The job has no install step: `scripts/gitleaks-pin.sh` fetches the version- **and** checksum-pinned binary itself, so CI and a laptop resolve the same scanner through the same code — a scan's verdict is a function of its rule set, so a different version would be a different gate. The official gitleaks action is not used: it needs a paid licence key for organization repositories. Findings print redacted — CI logs on a public repo are public. Followed immediately by `make test-secret-scan`, which plants a token in each exempted file and requires the scan to fail anyway: an allowlist that grew too broad reports "no leaks found" exactly like a clean tree. Ends with `make check-image-pins` (pure bash and grep, no toolchain), which lives here rather than in the classifier-gated backend lane because it reads the whole workflow directory — see the classifier exceptions above. `make check-backend` keeps running it too, so a laptop `make check` reproduces this verdict |
| `integration shard (k/12)` | `make test-integration` with `INTEGRATION_SHARD=k/12`: a deterministic per-test round-robin slice of the whole integration lane. Slices are count-based, not duration-based; the heavy e2e tail lands on whichever shard draws it, and `INTEGRATION_JOBS=16` (the tests wait on Postgres, not cores) lets that shard chew through its slice instead of running minutes over its siblings. Boots the dev compose stack (`make db-up`: digest-pinned Postgres 16 (pgvector) + Redis 7 + MinIO + the app role — one stack definition, no hand-mirrored GH services); each shard builds its own migrated `margince_test` template and clones per package. Uploads its slice manifests + binary coverage pods |
| `integration unit coverage` | The unit `-cover` pass over every package, binary coverage pods only. Needed because the shards run just the integration-tagged packages, and without it SonarCloud would see the unit-only packages at a false ~0% new-code coverage. No services (the test-lanes gate guarantees untagged tests open no real DB) |
| `integration` | The fan-in — and the required check, under the same name the single-runner lane carried, so branch protection is unchanged. Asserts every shard + the unit pass succeeded (a failed shard must turn this check red, not skipped), then `scripts/test-integration-reconcile.sh` proves the slices add up: every shard present, identical discovery, union complete + disjoint. Merges all coverage pods into `coverage.out`, uploads `go-coverage` |
| `vuln` | `make vuln` (govulncheck over all packages). **Advisory** — not a required context. It still runs on every backend PR, so a vulnerable dependency a PR *introduces* is reported before merge; what it cannot report is a vulnerability disclosed after one, which is why `scheduled.yml` runs it daily on `main` as well |
| `license gate` | `make sbom` then `make sbom-check` — the dependency-license policy (`grant`, policy in `.grant.yaml`) over the resolved dependency graph, not the manifests. Lives here rather than in `sbom.yml` because it is a **gate** and that workflow is an artifact producer: `sbom.yml` filters at the workflow level, so on a PR touching no dependency it produces no check run at all, and a required context that never posts blocks the merge forever. Job-level gating makes a path skip report as passing instead. PR-only — on `main` the same gate runs inside `sbom.yml`, where it is the precondition for signing, so each path runs it exactly once |
| `frontend` | `make frontend-check` (biome + vitest + tsc + Vite build) + a Storybook catalog build (stories must compile & register). Emits `fe-coverage` (lcov) |
| `uat` | `make frontend-e2e`: the AC-`<screen>`-N screen-acceptance criteria as named Playwright tests + axe WCAG 2.2 AA + the 390px no-horizontal-scroll sweep + the PERF-1 record-open budget. Mocks the API at the network edge, so it is self-contained |
| `live-boot` | The README quickstart run literally: compose up → migrate → api → `seed-dev` → `verify-boot`. Keeps the API-driven seed and the boot proof honest — the integration shards never boot the api or run the seed script, so those would rot invisibly without this job |
| `docker image (api\|web\|worker)` | `docker build` of the root Dockerfiles, which only downstream deploy tooling otherwise consumes. Without it a `FROM` bump or stage restructure matches no other classifier scope — every meaningful job skips and the PR looks green — which matters doubly now that Renovate auto-merges green dependency PRs, `dockerfile` manager included |
| `docker images (api + web + worker)` | The fan-in — and the required check. A path-skipped matrix job reports one check run under its **unexpanded** name, so the leg names can't be required contexts; this static-named job asserts every build leg succeeded (same shape as the `integration` fan-in: it must run and fail, not skip, when a leg fails) |
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
  `live-boot`, `frontend`, `uat`, the `docker image` builds) — so a
  malicious PR running `make test-integration` / `make frontend-e2e` can't read
  the persisted `GITHUB_TOKEN`. The diff-scoped gate jobs
  (`deterministic-gates`, `craftsmanship`, `craft-residue`) keep the token on
  purpose: they diff against `origin/main` and need it to fetch.
- Every `uses:` and container `image:` is pinned to an immutable SHA (the
  `check-image-pins` gate enforces it).

## The other workflows

`ci.yml` is the merge gate. Three workflows sit beside it, deliberately outside it:

- **`scheduled.yml`** — daily on `main`, the checks whose answer changes when
  nothing is being merged. `ci.yml` asks "is this diff sound?" and runs because a
  diff exists; these ask "is `main` still sound?", which a PR gate structurally
  cannot answer. `govulncheck` runs against a vulnerability database that changes
  daily, so a per-PR scan proves the day it merged and nothing since. The
  **SonarCloud quality gate** is read through the API (not re-scanned) because it
  is no longer a required PR check — a gate nobody is blocked by is a gate nobody
  reads. And the **backend lane** re-runs unconditionally, because `main`'s
  last-known-green is not evidence `main` is green: a docs-only commit landing
  after a breaking one matches no classifier scope, so every gate skips and the
  run reports green over a broken tree. That has happened more than once.
  Findings become **issues** (`scripts/scheduled-report.sh`), one open issue per
  check keyed on an exact title, because a red scheduled run notifies nobody and
  these checks exist precisely for the case where nothing prompts a human to look.
  The reporting job is the sole holder of `issues: write` and runs no build code —
  the same permission isolation `sbom.yml` uses for signing.

- **`sbom.yml`** — **no `pull_request` trigger**, so its automatic path is `main`
  (a manual dispatch still runs the `sbom` job on any ref; only `sign` is guarded
  to `main`). Regenerates the source-tree SBOMs whenever a
  dependency set or the SBOM pipeline itself changes, license-gates them, and signs
  them from a separate job that is the sole holder of `id-token: write`. Signing is
  isolated from all PR-controlled code because a keyless signature lands permanently
  in a public transparency log and cannot be retracted, so a PR preview must never
  produce one — and the license gate stays on this path because `sign`'s `needs:
  sbom` is what keeps a policy-failing SBOM from reaching it. The PR-side gate is
  the `license gate` job in `ci.yml` (above), so each event path runs the policy
  exactly once. Not itself a required check; the mechanics are in
  [docs/reference/supply-chain.md](../docs/reference/supply-chain.md).
- **`patch.yml`** — on every push to `main`, runs the release-management CLI over
  the push's range and uploads the incremental patch as a short-retention
  artifact for the distribution pipeline. The two degenerate cases go opposite
  ways: a branch creation or force-push has an all-zeros `before` — no ancestor
  to diff from — so the job **no-ops**, while a **manual dispatch** carries no
  push range at all and falls back to the parent commit (`HEAD~1..HEAD`). Not a
  gate — it never blocks a merge.
