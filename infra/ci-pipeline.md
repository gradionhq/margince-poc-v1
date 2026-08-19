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

One live run per ref, `main` included (`concurrency` group keyed on
`github.ref`, `cancel-in-progress: true`): a new push cancels the stale lane, so
the commit under gate is always the ref's tip. `release.yml` and `sbom.yml` are
superseded the same way, but scope their groups to the JOB rather than the
workflow, so cancellation reaches the expensive generation halves and never the
step that publishes or signs — see [The other workflows](#the-other-workflows).
`scheduled.yml` groups without cancelling; nothing supersedes a daily run.

Gating every `main` commit — a group keyed by commit — is what this replaced,
and the slot budget is the reason, not a smaller appetite for coverage. A
full-stack merge schedules 28 jobs against an org-wide ceiling of 20 concurrent
jobs, so two overlapping merges stretched a 9-minute gate past two hours and
starved every PR lane behind them. A verdict that lands hours after the merge
gates nothing.

`cancel-in-progress: false` is not the conservative choice here, which is the
trap worth remembering. It protects a run that is already *running*, and a group
holds **one** pending run: an arriving run cancels the pending one and takes its
place, so while every `main` push shared one group the *intermediate* merges were
the ones dropped — reported `cancelled`, zero jobs, indistinguishable from a
green skip on any dashboard — while the tip still waited out a lane gating an
older tree. `Release` shows the mechanism plainly in its own history: with run
463 still running, run 464 sat pending from 02:52:53 and was cancelled at
**02:55:24** — the second run 465 arrived, which then ran to success. `queue: max` lifts the pending limit to 100 and would gate every commit,
but it cannot be combined with cancellation and it puts the tip's verdict behind
every lane ahead of it — the latency this setting exists to remove.

What tip-only gating gives up is per-commit attribution. `main` is a linear
history of squash merges, so the tip's tree contains every merge below it and
"`main` is green" stays a true statement about all of them — but a bisect cannot
assume every commit was gated, and a red lane can be superseded before anyone
reads it. A `workflow_dispatch` on that commit brings the verdict back, and the
daily `backend lane (main)` in `scheduled.yml` stays the backstop for a tree
nothing is being merged into.

## Run only the checks a change can affect

The first job, **`changes`**, classifies the diff (dorny/paths-filter,
SHA-pinned) into five scopes; every downstream job gates on the relevant
output. A required job skipped this way still counts as passing.

`backend` and `backend_db` are the same set apart from the two agent
rulebooks. They are split because one flag was driving two unrelated things:
run the Go unit gates, and boot twelve Postgres databases. `AGENTS.md` and
`CLAUDE.md` are read by `backend/agentsdocparity_test.go`, so an edit to
either has to run a unit lane — and by no integration test, so it must not
run the database lanes. The two shards move in lockstep with the
`integration` fan-in, which asserts `success` from them: skipping one alone
would report a documentation PR as a broken integration lane.

| Scope | Paths | Gates |
|---|---|---|
| `backend_db` | `backend/**`, `infra/**/!(*.md)`, `go.work`, `go.work.sum`, `Makefile`, `scripts/**`, `extensions/**`, `fixtures/**`, `composition/**`, `.github/workflows/ci.yml`, `.github/actions/**`, `sonar-project.properties`, `frontend/src/mcp-apps/forbidden.json` | the twelve integration shards and the `integration` fan-in — every lane that opens a database |
| `backend` | `backend_db` (by YAML anchor, so the two cannot drift) plus the agent rulebooks `AGENTS.md` and `CLAUDE.md` | Go build/gate, extension reference, craftsmanship, unit coverage, vuln |
| `frontend` | `frontend/**`, `backend/api/**` (the contract drives FE types), plus the composition inputs the lane now typechecks against — `extensions/**`, `fixtures/**`, `composition/**`, `backend/tools/gen-composition/**`, `Makefile` — and the install inputs `pnpm-lock.yaml` and `pnpm-workspace.yaml`, which decide *which* dependency the SPA builds on and which one `openapi-typescript` parses the contract with (`overrides` lives in the workspace file, so it resolves versions the lockfile then merely records) | frontend lane, UAT |
| `e2e` | `backend/**`, `frontend/**`, `infra/**/!(*.md)`, `extensions/**`, `fixtures/**`, `composition/**` | full-stack live-boot |
| `deps` | `go.work`, `go.work.sum`, `**/go.mod`, `**/go.sum`, `**/package.json`, `**/pnpm-lock.yaml`, `pnpm-workspace.yaml` (`overrides` lives there, so it decides resolved versions the way a manifest does), `.syft.yaml`, `.grant.yaml`, `sbom-schemas/**`, `Makefile`, `.github/workflows/ci.yml`, `.github/actions/**` | the license gate |

Consequences:

- A **docs-only PR** matches no scope → every code gate skips. That includes
  the prose under `infra/` — this file documents the classifier, it is not an
  input to any gate, so the two `!(*.md)` extglobs keep an edit to it from
  booting the twelve-shard integration fleet. Each is written as one positive
  pattern because the action ORs its patterns: a separate `!infra/**/*.md`
  entry would match every path outside `infra/` and fire the filter on
  everything.
- A **Dockerfile-only PR** (the root `Dockerfile`, `.dockerignore`,
  `docker-bake.hcl`) also matches no scope. The role images are built, pushed
  and digest-pinned into the release by `release.yml` on every push to `main`
  — that build is the gate now, so an image break surfaces in the release run
  rather than the merge gate. Stated plainly because it is a real trade: a
  Dockerfile change merges without any image being built, and the first thing
  to notice is the release. `ci.yml` no longer carries a job for it, and the
  `docker images (api + web + worker)` context is no longer required.
- A **backend-only PR** skips the frontend + UAT lanes; a **frontend-only PR**
  skips the Go build/gate + the integration lane — except for
  `frontend/src/mcp-apps/forbidden.json`, which is authored under `frontend/`
  but copied into a Go package under a byte-equality test, so it is classified
  backend too.
- A **CI PR still runs the full backend lane** when it touches `ci.yml`, the
  `Makefile`, or `scripts/**`: those change what a gate *does*, so the gates
  re-run to prove they still pass under the new definition. `release.yml` and
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
  PR that unpins an action in `sbom.yml` or `release.yml` — and Renovate, which
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
| `secret-scan` | `make secret-scan` — gitleaks over a clean `git archive HEAD` export, policy in `.gitleaks.toml`. Scans the **committed** tree, never the working tree: gitleaks does not honour `.gitignore`, so an in-place scan reads sibling worktrees and local `.env` files and reaches a different verdict per machine. The job has no install step: `scripts/gitleaks-pin.sh` fetches the version- **and** checksum-pinned binary itself, so CI and a laptop resolve the same scanner through the same code — a scan's verdict is a function of its rule set, so a different version would be a different gate. The official gitleaks action is not used: it needs a paid licence key for organization repositories. Findings print redacted — CI logs on a public repo are public. Followed immediately by `make test-secret-scan`, which plants a token in each exempted file and requires the scan to fail anyway: an allowlist that grew too broad reports "no leaks found" exactly like a clean tree. Then `make test-api-entrypoint`, which is the same class of check one layer out: the container entrypoint must write the bootstrap admin credential only onto an unprovisioned installation, retire one an earlier boot left, and refuse to start when it cannot tell which it is — ADR-0061 §2 consumes bootstrap values exactly once, and a credential written to a live installation is as invisible as an over-broad allowlist. It stubs `margince-migrate`/`margince-api` on `PATH`, so it needs no container and no database. Then `make test-dev-dsn`, in this job for the same reason: a dev stack that ignored `MARGINCE_DSN` looked exactly like one that honoured it, and a slugged stack that took its database name from a supplied DSN looks isolated while sharing the base database. Pure shell, no Docker. Then `make check-workflow-timeouts`, which asserts every job in `.github/workflows/` carries `timeout-minutes` — a job without one inherits GitHub's **six-hour** default, so a hang holds a required check for a working day while reading as a queue backlog rather than a failure. It is derived from the workflow tree rather than a list of job names, and it rides in this job for the same reason the pin gate does: it reads the whole workflow directory, so gated on the classifier it would skip on exactly the PR that adds an unbounded job. Ends with `make check-image-pins` (pure bash and grep, no toolchain), which lives here rather than in the classifier-gated backend lane because it reads the whole workflow directory — see the classifier exceptions above. `make check-backend` keeps running it too, so a laptop `make check` reproduces this verdict |
| `integration shard (k/12)` | `make test-integration` with `INTEGRATION_SHARD=k/12`: a deterministic per-test round-robin slice of the whole integration lane. Slices are count-based, not duration-based; the heavy e2e tail lands on whichever shard draws it, and `INTEGRATION_JOBS=16` (the tests wait on Postgres, not cores) lets that shard chew through its slice instead of running minutes over its siblings. Boots the dev compose stack (`make db-up`: digest-pinned Postgres 16 (pgvector) + Redis 7 + MinIO + the app role — one stack definition, no hand-mirrored GH services); each shard builds its own migrated `margince_test` template and clones per package. Uploads its slice manifests + binary coverage pods |
| `integration unit coverage` | The unit `-cover` pass over every package, binary coverage pods only. Needed because the shards run just the integration-tagged packages, and without it SonarCloud would see the unit-only packages at a false ~0% new-code coverage. No services (the test-lanes gate guarantees untagged tests open no real DB) |
| `integration` | The fan-in — and the required check, under the same name the single-runner lane carried, so branch protection is unchanged. Asserts every shard + the unit pass succeeded (a failed shard must turn this check red, not skipped), then `scripts/test-integration-reconcile.sh` proves the slices add up: every shard present, identical discovery, union complete + disjoint. Merges all coverage pods into `coverage.out`, uploads `go-coverage` |
| `vuln` | `make vuln` (govulncheck over all packages). **Advisory** — not a required context. It still runs on every backend PR, so a vulnerable dependency a PR *introduces* is reported before merge; what it cannot report is a vulnerability disclosed after one, which is why `scheduled.yml` runs it daily on `main` as well |
| `license gate` | `make sbom` then `make sbom-check` — the dependency-license policy (`grant`, policy in `.grant.yaml`) over the resolved dependency graph, not the manifests. Lives here rather than in `sbom.yml` because it is a **gate** and that workflow is an artifact producer: `sbom.yml` filters at the workflow level, so on a PR touching no dependency it produces no check run at all, and a required context that never posts blocks the merge forever. Job-level gating makes a path skip report as passing instead. PR-only — on `main` the same gate runs inside `sbom.yml`, where it is the precondition for signing, so each path runs it exactly once |
| `fe-quality` | `make fe-quality` — the design-system script gates, the contract type-drift check, Biome, the composed-SPA typecheck (ADR-0069) and the unit screens' own vitest suites. The only frontend job carrying a Go toolchain: the composed lane needs `gen-composition` output, which nothing else produces |
| `fe-unit` | `make fe-unit FE_COVERAGE=1` — the vitest suite, instrumented so the run that decides the verdict also writes the lcov. Emits `fe-coverage`, after `frontend/scripts/check-lcov-paths.sh` has proved every path in it resolves from the repo root (see below). Not sharded: the v8 provider's branch records cannot be merged across shards without skewing condition coverage — issue #966 has the measurements and the fix |
| `fe-bundle` | `make fe-bundle` — the Vite production build plus the Storybook catalog build (stories must compile & register) |
| `frontend` | The fan-in — and the required check, under the same name the single-runner lane carried, so branch protection is unchanged. Asserts all three jobs above succeeded: a failed lane must turn this check **red, not skipped**, because GitHub counts a skipped required check as passing. The three run concurrently because they share no state; serially the lane was ~340s, of which vitest alone was ~207s, so the greps and the type gates sat behind a test run that could tell them nothing |
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
- On a **push** the scan additionally requires `integration` to have
  succeeded. A push replaces main's stored analysis, main's new-code period is
  `previous_version` (weeks of Go, not this commit's diff), and the scanner's
  Zero Coverage Sensor scores every executable line it has no report for as
  uncovered — so a push carrying no Go coverage would publish those weeks at
  0% and turn the quality gate red until a later push rescanned them. A
  docs-only merge skips every coverage producer and is the common way in. Such
  a push now skips the scan instead, and main keeps its last real reading. On a
  pull request the rule does not apply: new code there is the diff, and a diff
  that skipped an area has no lines of that area to cover.
- **A report's paths are read from the repo root, not from the directory that
  wrote it.** The scanner resolves every `SF:` entry in an lcov against its own
  base directory; vitest's root is `frontend/`, so the default report named
  `src/App.tsx` and the scanner — which drops an unresolvable record in
  silence — held no frontend coverage at all, from the first run that handed it
  one (#38) until #1541, while the project reported the backend's 84% as the
  whole measurement. `coverage.reporter`
  in `frontend/vite.config.ts` now sets the reporter's `projectRoot` to the repo
  root, and `frontend/scripts/check-lcov-paths.sh` fails `fe-unit` if any record
  stops resolving. The Go profiles carry package import paths and were never
  affected.

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
  Cancellation is scoped to the **`sbom` job**, not the workflow: a newer push
  supersedes a lane still cataloguing an older tree, but `sign` carries no group
  and cannot be interrupted — it writes to Rekor before the bundles upload, and a
  lane cut between the two would leave a permanent signature for a tree whose
  bundles nobody can fetch. Superseding therefore only takes effect *before*
  signing begins — while `sbom` is pending or running. A push arriving after
  generation finished does not stop the signature it has already earned.
- **`release.yml`** — on a push to `main`, cuts a margince-constellation
  release versioned `1970.<build>` (the year pinned to the epoch while the
  flow is a PoC, so these releases order below any real dated release; the
  build is the workflow run number) in the dist service of the constellation
  deployment at test.margince.com — not a GitHub release: the release-management CLI cuts the
  incremental patch over the push's range and uploads it with `draft-release`
  together with the three source-tree SBOMs regenerated at the release commit
  (`make sbom` — the dist service verifies the SBOMs attest every file the
  patch produces, so the possibly-lagging committed `sboms/` are never
  uploaded), then the three role images are built through the bake file
  (`docker-bake.hcl`, linux/amd64 + linux/arm64 with `mode=max` provenance
  attestations — the builder stages cross-compile natively, only runtime
  layers run emulated). The bake warms up from two Actions caches, because
  the runner is ephemeral: `CACHE=gha` exports the layer cache per role
  (its durable win is the dependency-download layer, which busts only on a
  module-pin change), and buildkit-cache-dance + actions/cache carry the
  BuildKit cache-mount contents (Go compile cache, pnpm store, Corepack's
  pnpm download, tsc `.tsbuildinfo`) across runs — mounts are not layers, so no layer cache
  covers them. Both live in the repo's 10 GB Actions cache, which the CI
  lanes' Go caches keep near the cap, so entries older than a few hours are
  routinely LRU-evicted: the caches bridge releases that land close
  together — the busy-day case where they matter — and a release after a
  quiet night simply bakes cold. The images are pushed to the constellation
  registry
  (`registry.test.margince.com/margince/<role>`, authenticated as the
  registry publisher via the `MARGINCE_AUTH_PUBLISHER_TOKEN` secret), added to
  the draft as digest-pinned references with `add-artifacts`, and the release
  is published with `publish-release`. The dist uploads authenticate with the
  dist publisher token (the `MARGINCE_DIST_PUBLISHER_TOKEN` secret). The two
  degenerate patch cases go opposite ways: a branch creation (all-zeros
  `before`) or a force-push (a `before` the fetched history no longer reaches)
  has no ancestor to diff from, so the release drafts **without a patch** and
  **stays an unpublished draft** (the dist completeness gate requires the
  patch), while a **manual dispatch** carries no push range at all and falls
  back to the parent commit (`HEAD~1..HEAD`). Merges that land close together
  release only the tip: `draft` and `docker-image` each carry a cancelling group
  so a bake for a superseded commit stops, while `publish` carries a group that
  **serializes instead of cancelling** — a publish that has started always
  finishes, and a publish still pending when a newer one arrives gives up its
  place. That is mutual exclusion, not ordering: nothing on this path rejects a
  stale version, so a re-run or a dispatch of an older commit can still publish
  after a newer one
  ([#1810](https://github.com/gradionhq/margince-poc-v1/issues/1810)). The patch
  range is what makes that consequential:
  each push's range starts at the ref's previous tip, so when commit *N*'s lane
  is cancelled the next release's patch runs *N..N+1* and the files *N* changed
  appear in no published patch at all. A consumer applying patches in order is
  therefore one increment short. Deriving the base from the last **published**
  release instead of the push's `before` is what closes that
  ([#1798](https://github.com/gradionhq/margince-poc-v1/issues/1798)). Not a
  gate — it never blocks a merge.
