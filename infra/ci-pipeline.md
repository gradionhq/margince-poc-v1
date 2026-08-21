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
likewise advisory. Promoting the three of them is deliberate future work rather
than an oversight; the reason it is not bundled with the merge queue is in
[The `ci` aggregate](#the-ci-aggregate-is-the-only-required-context).

## Triggers

- `pull_request` (`opened`, `synchronize`, `reopened`, `ready_for_review`)
- `merge_group` — **the merge gate**
- `workflow_dispatch` (manual)

There is no `push` to `main` trigger. A push to `main` is the *record* of a
verdict already reached, because the merge queue gated that exact tree before it
landed.

### The merge queue is what gates

A `merge_group` run builds `main` + everything ahead of it in the queue + the
entry under test, on a throwaway `gh-readonly-queue/main/...` ref, and gates
**that** tree. Two properties follow, and they are the whole point:

- **Full tree, always.** The change classifier is overridden on `merge_group`
  (every scope reports `true`), so no job can be skipped there.
- **Every commit, not just the tip.** The tree that is measured is the tree that
  merges.

The queue merges in **batches**, because serialising one PR per ~20-minute lane
would cap merges at three an hour against a demonstrated rate of 70–82 a day.
Batching is what makes per-commit gating affordable at all.

Two separate limits govern that, and they are easy to conflate:

| Ruleset setting | Configured | What it bounds |
|---|---|---|
| `max_entries_to_merge` | **2** | how many entries may merge together as one group |
| `max_entries_to_build` | **2** | how many queued entries may request checks at once |
| `min_entries_to_merge` / `…_wait_minutes` | 1 / 2 | a lone entry still merges after a 2-minute wait |
| `grouping_strategy` | `HEADGREEN` | **which commits get checked** — see below |
| `check_response_timeout_minutes` | 60 | clears the 22-minute p90 with margin |
| `merge_method` | `SQUASH` | preserves `required_linear_history` |

`grouping_strategy` is not about partial merging. `HEADGREEN` checks only the
merge group's **head** commit — the combined changes of every entry in the group —
while `ALLGREEN` checks each entry's intermediate commit individually. The trade
is cost against attribution: `ALLGREEN` tells you *which* entry broke the batch
but multiplies the 28-job lane by the group size, which a 20-concurrent ceiling
cannot absorb. `HEADGREEN` pays one lane per group and leaves GitHub to work out
which entry to eject when the group fails. **Neither strategy merges a passing
prefix of a failing group.**

`max_entries_to_merge` starts at **2** rather than higher on purpose: a batched
queue multiplies the cost of a flaky job by the group size, and
[#1494](https://github.com/gradionhq/margince-poc-v1/issues/1494) is open against
exactly that noise. It is a live ruleset knob — raise it once the queue has a
measured baseline.

`concurrency` is keyed on `github.ref` with `cancel-in-progress` narrowed to
`pull_request`: a new push supersedes the review in flight, and a merge verdict
can never be cancelled. Each queue entry has its own ref, so entries never
collide in that group.

`release.yml` and `sbom.yml` do not contend for the runner budget at all — both
are **manual dispatch only**, so neither is triggered by a merge. They keep their
JOB-scoped groups for the case of two deliberate dispatches, where cancellation
must reach the expensive generation halves and never the step that publishes or
signs — see [The other workflows](#the-other-workflows). `scheduled.yml` groups
without cancelling; nothing supersedes a daily run. `cache-warm.yml` groups
without cancelling for a different reason: a cancelled run saves no cache, so
finishing is the entire point.

### What this replaced, and why it had to go

`main` pushes used to be gated, sharing one concurrency group with
`cancel-in-progress: true`. The stated invariant was that the commit under gate
is always the ref's tip, traded against a slot budget: a full-stack merge
schedules 28 jobs against an org-wide ceiling of 20 concurrent, so overlapping
merges stretched a 9-minute gate past two hours.

The run history contradicted the invariant. Across the last 25 pushes to `main`,
**16 were cancelled** and the survivors averaged **4.5 minutes** against a real
lane's 16 — the fast ones were classifier skips, not verdicts. Roughly one `main`
commit in twenty-five was ever gated, and a docs-only merge landing after a
breaking one matched no scope, skipped every job, and reported green. `main` went
red twice with the breakage masked exactly that way.

Both premises are void, and neither needed a bigger runner budget to void.

The invariant is contradicted outright by the numbers above. And the slot argument
was an argument against *racing* a lane after the merge — which a queue does not
do: it gates before the merge, and it **batches**, so a group of entries shares
one lane rather than each claiming their own. The 20-concurrent ceiling is
unchanged; what
changed is that `release.yml` and `sbom.yml` stopped drawing ~448 runs a week from
it (both are dispatch-only now), which is where the room for a queue lane came
from. If queue depth grows anyway, `max_entries_to_merge` is a ruleset knob that
costs nothing to turn.

A skipped required check also counts as *passing* on GitHub, which is why the nine
separately-required contexts collapsed into one `ci` job that refuses a skip — see
[The `ci` aggregate](#the-ci-aggregate-is-the-only-required-context).

## Run only the checks a change can affect

The first job, **`changes`**, classifies the diff (dorny/paths-filter,
SHA-pinned) into five scopes; every downstream job gates on the relevant
output.

**This applies to `pull_request` only.** On `merge_group` every scope is forced
`true`, so the queue lane is full-tree and nothing can be skipped there. The
distinction is the load-bearing one in this document: diff-scoping is honest as
*author feedback* and dishonest as a *merge verdict*. A PR-side skip is safe
precisely because the queue lane covers the ground it skipped — which is why the
override lives in the `changes` job's outputs, where it cannot be forgotten, and
not in each consumer's `if:`.

The classifier still *runs* on `merge_group`; only its answer is overridden. A
reader therefore sees the real diff in the log next to the reason it was ignored.

Note the `== 'true'` on each output expression. paths-filter emits the **string**
`true`/`false`, and GitHub's `||` coalesces on falsiness, where the non-empty
string `"false"` is truthy — without the comparison every scope would read as
true on every event.

`backend` and `backend_db` are the same set apart from the two agent
rulebooks. They are split because one flag was driving two unrelated things:
run the Go unit gates, and boot the sharded Postgres databases. `AGENTS.md` and
`CLAUDE.md` are read by `backend/agentsdocparity_test.go`, so an edit to
either has to run a unit lane — and by no integration test, so it must not
run the database lanes. The two shards move in lockstep with the
`integration` fan-in, which asserts `success` from them: skipping one alone
would report a documentation PR as a broken integration lane.

| Scope | Paths | Gates |
|---|---|---|
| `backend_db` | `backend/**`, `infra/**/!(*.md)`, `go.work`, `go.work.sum`, `Makefile`, `scripts/**`, `extensions/**`, `fixtures/**`, `composition/**`, `.github/workflows/ci.yml`, `.github/workflows/_lane-*.yml` (the caller plus every lane it invokes — globbed so a lane added later is covered the day it lands), `.github/actions/**`, `sonar-project.properties`, `frontend/src/mcp-apps/forbidden.json` | the integration shards and the `integration` fan-in — every lane that opens a database |
| `backend` | `backend_db` (by YAML anchor, so the two cannot drift) plus the agent rulebooks `AGENTS.md` and `CLAUDE.md` | Go build/gate, extension reference, craftsmanship, unit coverage, vuln |
| `frontend` | `frontend/**`, `backend/api/**` (the contract drives FE types), plus the composition inputs the lane now typechecks against — `extensions/**`, `fixtures/**`, `composition/**`, `backend/tools/gen-composition/**`, `Makefile` — and the install inputs `pnpm-lock.yaml` and `pnpm-workspace.yaml`, which decide *which* dependency the SPA builds on and which one `openapi-typescript` parses the contract with (`overrides` lives in the workspace file, so it resolves versions the lockfile then merely records) | frontend lane, UAT |
| `e2e` | `backend/**`, `frontend/**`, `infra/**/!(*.md)`, `extensions/**`, `fixtures/**`, `composition/**` | full-stack live-boot |
| `deps` | `go.work`, `go.work.sum`, `**/go.mod`, `**/go.sum`, `**/package.json`, `**/pnpm-lock.yaml`, `pnpm-workspace.yaml` (`overrides` lives there, so it decides resolved versions the way a manifest does), `.syft.yaml`, `.grant.yaml`, `sbom-schemas/**`, `Makefile`, `.github/workflows/**` (syft catalogs a `uses:` as a package, so any workflow gaining a reference changes what the gate judges — a pinned remote action brings its license, a local reusable workflow brings none), `.github/actions/**` | the license gate |

Consequences:

- A **docs-only PR** matches no scope → every code gate skips **on the PR**, and
  runs in full when the PR reaches the queue. That includes the prose under
  `infra/` — this file documents the classifier, it is not an input to any gate,
  so the two `!(*.md)` extglobs keep an edit to it from booting the sharded
  integration fleet on the PR side. Each is written as one positive pattern
  because the action ORs its patterns: a separate `!infra/**/*.md` entry would
  match every path outside `infra/` and fire the filter on everything.

  This is the case that used to be dangerous. A docs-only commit landing on
  `main` after a breaking one matched no scope, skipped every gate, and reported
  green — `main` went red twice with the breakage masked exactly that way. The
  queue closes it: the docs-only entry is gated against the full tree it is
  merging into.
- A **Dockerfile-only PR** (the root `Dockerfile`, `.dockerignore`,
  `docker-bake.hcl`) also matches no scope, and **nothing else builds the role
  images either**. `ci.yml` dropped its `docker images (api + web + worker)` job
  on the reasoning that `release.yml` baked the images on every push to `main`,
  so a break surfaced within a commit. That reasoning expired when `release.yml`
  became dispatch-only: the images are now built only when somebody cuts a
  release, so a broken `Dockerfile` can sit on `main` indefinitely and the person
  who finds it is whoever tries to release next. Stated plainly because it is a
  real regression in coverage, not a trade that still balances — restoring a
  build-only, push-nothing image job scoped to those three paths is
  https://github.com/gradionhq/margince-poc-v1/issues/1965.
- A **backend-only PR** skips the frontend + UAT lanes; a **frontend-only PR**
  skips the Go build/gate + the integration lane — except for
  `frontend/src/mcp-apps/forbidden.json`, which is authored under `frontend/`
  but copied into a Go package under a byte-equality test, so it is classified
  backend too.
- A **CI PR still runs the full backend lane** when it touches `ci.yml`, the
  `Makefile`, or `scripts/**`: those change what a gate *does*, so the gates
  re-run to prove they still pass under the new definition. `release.yml` and
  `sbom.yml` are outside the scope — neither runs a backend gate. Note that
  neither proves itself on a schedule either, now that both are dispatch-only: a
  change that breaks one is discovered by the next person to dispatch it, so a PR
  touching either is worth dispatching from its own branch before merging.
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
          ├─> integration  →  _lane-integration.yml ────────────────┐
          │                     integration-shards (×6) ──┐         │
          │                     integration-unit-coverage ┴─> fan-in│
          ├─> extension-reference ──────────────────────────────┐   │
          ├─> vuln                                              │   │
          ├─> license gate  (`deps` scope)                      │   │
          ├─> frontend  →  _lane-frontend.yml ──> uat           │   │
          │                  fe-quality ┐                       │   │
          │                  fe-unit    ├─> fan-in              │   │
          │                  fe-bundle  ┘                       │   │
          ├─> live-boot                                         │   │
          v                                                     v   v
 deterministic-gates + integration + extension-reference + frontend ──> sonarcloud
  dco            (independent — runs on merge_group too)
  craft-residue  (every non-draft change, independent)
  secret-scan    (every non-draft change, independent — + the image-pin gate)

  ci  ── the ONE required context. needs: dco, deterministic-gates,
         craftsmanship, craft-residue, secret-scan, extension-reference,
         integration, frontend, license-gate   (nine — vuln, live-boot and uat
         stay advisory and are NOT in the fan-in)
```

### Two lanes are called, not inlined

`integration` and `frontend` are `workflow_call` jobs: the caller decides whether
the lane runs, and the lane's jobs live in
[`_lane-integration.yml`](../.github/workflows/_lane-integration.yml) and
[`_lane-frontend.yml`](../.github/workflows/_lane-frontend.yml). Those two
clusters were a third of `ci.yml` — the six-way shard matrix, the coverage
plumbing, the two fan-ins — and none of it is read when the merge gate itself
changes.

Both were already **fan-in contexts**, which is why they are the two that moved:
`needs.integration.result` and `needs.frontend.result` mean exactly what they
meant before, so the `ci` aggregate and the `sonarcloud` conditions are unchanged.
Extracting a cluster whose members the aggregate names individually would have
coarsened its verdict from "craftsmanship failed" to "the Go lane failed".

**A lane carries no `if:` of its own.** The condition lives at the call site, and
that is load-bearing rather than stylistic: it makes
`needs.<lane>.result == 'skipped'` mean exactly one thing — the caller skipped the
whole lane — which is the distinction the aggregate reads when it refuses a skip
on the merge queue. An internal conditional would let a job inside skip while the
lane still reported `success`, reopening the skip-as-pass hole one level down.

**`defaults.run.working-directory` does not inherit** into a called workflow. Each
lane restates it; without that, every step would run from the repository root.

Check names inside a lane are reported as `<caller-job> / <lane-job>` — e.g.
`integration / integration shard (3/6)`. Only `ci` is a required context, so no
ruleset depends on those names.

Why the pipeline stays **one caller** rather than splitting into `pr.yml` and
`merge-queue.yml`: the `ci` aggregate is the single required check, and two
callers would mean two definitions of it that must stay byte-identical or the two
events gate differently. That is the "two hand-maintained copies of one list"
shape this repository refuses everywhere else, and it would be guarding the one
check everything depends on.

Two deliberate shapes here. The Playwright `uat` lane is **fail-fast**: it
starts only after the cheaper `frontend` gate (biome + vitest + tsc + build)
passes. The real-Postgres integration lane is the opposite — it runs **beside**
`deterministic-gates`, not behind it: it is the longest lane in the pipeline,
so serializing the two slowest jobs dominated PR wall-clock, and a broken
build is still caught by `deterministic-gates` itself. And the lane is
**sharded**: six matrix runners each execute a deterministic per-test slice
(package-level splitting would floor at the heaviest package,
`compose/integration`), and the `integration` fan-in reassembles them into the
single result the `ci` aggregate reads.

### The `ci` aggregate is the only required context

Branch protection requires exactly one check: **`ci`**. It reaches a verdict from
`needs.*.result` through [`scripts/ci-verdict.sh`](../scripts/ci-verdict.sh),
which is unit-tested by `make test-ci-verdict` and wired into `check-backend`.

The rule it exists to enforce: **a skipped job is not a passing one on
`merge_group`.** GitHub counts a skipped *required* check as passing, so nine
separately-required contexts were satisfiable by a run that did nothing — the
classifier skipped the jobs, and branch protection read the skips as green. The
aggregate refuses any result other than `success` on `merge_group`; on
`pull_request` it admits `skipped`, because there the classifier is doing its job
and the queue lane covers the remainder.

`if: always()`, deliberately: an aggregate that is skipped alongside a failed
upstream job reports a **green** required check, which is the same failure wearing
a different hat.

**`needs` is exactly the nine contexts the ruleset required before the aggregate
replaced them**, and that equality is the point: this change moved where the
verdict is computed, not what it covers. Widening the gate in the same step would
mean a red merge queue with two candidate explanations, during the week the queue
itself is on trial.

Ten jobs are deliberately **not** in `needs`:

- `changes` — the classifier produces no verdict.
- `fe-quality`, `fe-unit`, `fe-bundle` — absorbed by the `frontend` fan-in.
- `integration-shards`, `integration-unit-coverage` — absorbed by the
  `integration` fan-in, which already asserts on their results.
- `sonarcloud` — non-blocking by decision; listing it here would make it required
  by the back door.
- `vuln`, `live-boot`, `uat` — advisory, and left that way **on purpose**. They
  are the obvious additions: each runs on every qualifying change and a red one
  does not stop a merge, which is not a state worth keeping. What argues for
  waiting is the batching. A flaky job under a merge queue does not cost one
  re-run; it fails the whole group it was checked in and every entry in that group
  is re-queued, and nothing has ever exercised these three under a gate that
  blocks. Promote them once the queue
  has a measured baseline, as their own change, so a regression has exactly one
  explanation.

Six rather than twelve because the per-test slice is the cheap half of a shard.
Measured on a green run, one shard spent ~146s restoring the build cache and
~275s compiling against ~40s running its assigned tests — so each extra shard
divides the 40s again and pays another 420s. Halving the matrix costs about a
minute of wall clock and returns roughly half the lane's runner-minutes. The
compile half is a build-once problem, not a sharding one.

## The shared Go build cache

Every Go job restores
[`.github/actions/go-build-cache`](../.github/actions/go-build-cache/action.yml)
before it compiles.

It exists because `actions/setup-go` cannot do this job. Its cache key hashes
only `go.sum`, so the entry is written once and never refreshed — every later
run logs *"Cache hit occurred on the primary key … not saving cache"* and
restores that first snapshot forever. Measured on this repo, that blob is
**~25 MB** while a warm Go build cache is **~550 MB**: the module cache was
being restored, the build cache effectively was not. Eleven Go jobs per
backend run — the six shards, the merge gate, the composed-build lane, the
coverage pass, `live-boot`, `govulncheck` — each compiled the module from
scratch, every run. setup-go still owns the module cache; this action owns the
build cache beside it.

Two flavours, because a build tag and coverage instrumentation change the
package builds themselves and only the dependency builds underneath are common:

| Flavour | Written by | Read by |
|---|---|---|
| `plain` | `cache-warm.yml` job `plain` | `deterministic-gates`, `extension-reference`, `integration unit coverage`, `live-boot`, `vuln` |
| `integration` | `cache-warm.yml` job `integration` | all six shards |

### The writer lives in `cache-warm.yml`, on a schedule

`ci.yml` only ever **restores**. The writing lives in
[`.github/workflows/cache-warm.yml`](../.github/workflows/cache-warm.yml), which
runs on `main` every three hours plus `workflow_dispatch`, and **gates nothing** —
a red or cancelled run there costs latency on the next lane and nothing else.

Three constraints shaped that, and each rules out an alternative that looks
simpler:

- **The writer cannot live on the merge queue.** `actions/cache` scopes an entry
  to the branch that wrote it plus the default branch, and a `merge_group` run
  lives on a throwaway `gh-readonly-queue/main/...` ref — an entry saved there is
  invisible to every PR lane and dies with the ref. *Restoring* is unscoped
  (default-branch entries are readable from any branch), so only the write has to
  be on `main`.
- **The writer cannot be per-push.** Seeding a flavour is a byproduct of
  compiling: the `plain` job runs a full `make check-backend`, the `integration`
  job runs a shard against a live Postgres. At ~80 merges a day that pair would
  run ~80 times and mostly self-cancel — the same starvation the merge queue
  removed, merely made harmless.
- **A stale entry is not a wrong entry.** Go's build cache is content-addressed
  and the key falls back twice: `…-<deps-hash>-<sha>` → `…-<deps-hash>-` → `…-`.
  Dropping the dependency hash on the last hop is deliberate — a stale restore
  only misses the entries whose inputs changed, so a three-hour-old cache is
  substantially warm and a post-dependency-bump cache still beats a cold one.

What this replaced: both refresh steps used to ride inside gating jobs, gated on
`github.event_name == 'push' && github.ref == 'refs/heads/main'`. The cache was
therefore seeded only when a `main` push survived to completion — and 64% of them
were cancelled, so it was being seeded by accident, rarely.

**Exactly one writer per flavour** still holds, and for the original reason: every
shard compiles substantially the same set, so a second writer would add nothing
but a race for the same key, and every runner uploading ~550 MB would evict
the entry they meant to seed. `cache-warm.yml` runs one job per flavour and takes
`concurrency: cancel-in-progress: false`, because a cancelled run saves nothing
and the entire point is that a run finishes.

The refresh steps use `!cancelled()` rather than `success()`: a red gate still
compiled the tree, and those artifacts are exactly as reusable as a green run's.

`scripts/check-image-pins.sh` scans `.github/actions/` alongside the workflows.
The `./path` allowance waves a local action through on the grounds that the
repo versions its own code — true of the action's own ref, but not of the
third-party actions it calls, which would otherwise ride in unread.

## The jobs

| Job | What it enforces |
|---|---|
| `changes` | The scope classifier above (always runs first, on non-draft; its answer is overridden on `merge_group`) |
| `dco` | Every merging commit carries a Developer Certificate of Origin sign-off (`scripts/check-dco.sh`). Runs on `merge_group` as well as `pull_request`, taking its base/head SHAs from whichever event fired — the queue's are the ones that count, since that is the tree that lands. It cannot be `pull_request`-only: the `ci` aggregate refuses a skip on `merge_group`, so a PR-only job there would stop merging outright, and exempting it would have reopened the skip-as-pass hole for the one gate that proves provenance |
| `deterministic-gates` | `make check-backend`: build, vet, lint (baseline + new-code strict), arch-lint, unit + root fitness tests (incl. `audit_log` enum coherence + the contract `$ref` pre-flight), generated-drift, and the script gates (craft-doc floor, image pins, contract-breaking, test-lanes, file-length, RLS store-path, jurisdiction isolation, and the `backend/pkg` published-surface freeze). Fetches full history so the diff-scoped gates have a base ref |
| `extension-reference` | The composed-build lane (ADR-0069): proves the **empty** extension set still composes byte-identically to the committed `composition/` stub, then enables the reference fixture and runs the backend build + unit lane + `check-composition` against the composed workspace, plus every enabled unit's own module lane. Emits its own coverage profile — extension units are separate Go modules, unreachable by the shard profiles |
| `craftsmanship` | `make craft-static` — strict: BLOCKER **and** MAJOR findings fail it, MINOR is advisory. Runs **after** `deterministic-gates` — a red build is never judged on style |
| `craft-residue` | No unresolved `CRAFT-FIX`/`CRAFT-DISPUTE` markers reach `main` |
| `secret-scan` | `make secret-scan` — gitleaks over a clean `git archive HEAD` export, policy in `.gitleaks.toml`. Scans the **committed** tree, never the working tree: gitleaks does not honour `.gitignore`, so an in-place scan reads sibling worktrees and local `.env` files and reaches a different verdict per machine. The job has no install step: `scripts/gitleaks-pin.sh` fetches the version- **and** checksum-pinned binary itself, so CI and a laptop resolve the same scanner through the same code — a scan's verdict is a function of its rule set, so a different version would be a different gate. The official gitleaks action is not used: it needs a paid licence key for organization repositories. Findings print redacted — CI logs on a public repo are public. Followed immediately by `make test-secret-scan`, which plants a token in each exempted file and requires the scan to fail anyway: an allowlist that grew too broad reports "no leaks found" exactly like a clean tree. Then `make test-api-entrypoint`, which is the same class of check one layer out: the container entrypoint must write the bootstrap admin credential only onto an unprovisioned installation, retire one an earlier boot left, and refuse to start when it cannot tell which it is — ADR-0061 §2 consumes bootstrap values exactly once, and a credential written to a live installation is as invisible as an over-broad allowlist. It stubs `margince-migrate`/`margince-api` on `PATH`, so it needs no container and no database. Then `make test-dev-dsn`, in this job for the same reason: a dev stack that ignored `MARGINCE_DSN` looked exactly like one that honoured it, and a slugged stack that took its database name from a supplied DSN looks isolated while sharing the base database. Pure shell, no Docker. Ends with `make check-image-pins` (pure bash and grep, no toolchain), which lives here rather than in the classifier-gated backend lane because it reads the whole workflow directory — see the classifier exceptions above. `make check-backend` keeps running it too, so a laptop `make check` reproduces this verdict |
| `integration shard (k/12)` | `make test-integration` with `INTEGRATION_SHARD=k/12`: a deterministic per-test round-robin slice of the whole integration lane. Slices are count-based, not duration-based; the heavy e2e tail lands on whichever shard draws it, and `INTEGRATION_JOBS=16` (the tests wait on Postgres, not cores) lets that shard chew through its slice instead of running minutes over its siblings. Boots the dev compose stack (`make db-up`: digest-pinned Postgres 16 (pgvector) + Redis 7 + MinIO + the app role — one stack definition, no hand-mirrored GH services); each shard builds its own migrated `margince_test` template and clones per package. Uploads its slice manifests + binary coverage pods |
| `integration unit coverage` | The unit `-cover` pass over every package, binary coverage pods only. Needed because the shards run just the integration-tagged packages, and without it SonarCloud would see the unit-only packages at a false ~0% new-code coverage. No services (the test-lanes gate guarantees untagged tests open no real DB) |
| `integration` | The fan-in the `ci` aggregate reads — it stands for the whole sharded lane, so the aggregate needs one entry rather than six. Asserts every shard + the unit pass succeeded (a failed shard must turn this check red, not skipped), then `scripts/test-integration-reconcile.sh` proves the slices add up: every shard present, identical discovery, union complete + disjoint. Merges all coverage pods into `coverage.out`, uploads `go-coverage` |
| `vuln` | `make vuln` (govulncheck over all packages). **Advisory** — outside the `ci` aggregate, so a red one does not stop a merge. It still runs on every backend change, so a vulnerable dependency a PR *introduces* is reported before merge; what it cannot report is a vulnerability disclosed after one, which is why `scheduled.yml` runs it daily on `main` as well |
| `license gate` | `make sbom` then `make sbom-check` — the dependency-license policy (`grant`, policy in `.grant.yaml`) over the resolved dependency graph, not the manifests. Lives here rather than in `sbom.yml` because it is a **gate** and that workflow is an artifact producer: `sbom.yml` filters at the workflow level, so on a PR touching no dependency it produces no check run at all, and a required context that never posts blocks the merge forever. Job-level gating makes a path skip report as passing instead. Runs on `merge_group` as well as `pull_request`, and it is the **only** automatic run of this policy — `sbom.yml` is dispatch-only, so the copy of the gate inside it fires just before a signing run. The merge-queue run is what makes that sufficient, and makes it a stronger claim than it was: `main` receives a dependency change only through a queue build this job passed, so the policy is judged against the tree that lands rather than a PR head that may not match it |
| `fe-quality` | `make fe-quality` — the design-system script gates, the contract type-drift check, Biome, the composed-SPA typecheck (ADR-0069) and the unit screens' own vitest suites. The only frontend job carrying a Go toolchain: the composed lane needs `gen-composition` output, which nothing else produces |
| `fe-unit` | `make fe-unit FE_COVERAGE=1` — the vitest suite, instrumented so the run that decides the verdict also writes the lcov. Emits `fe-coverage`, after `frontend/scripts/check-lcov-paths.sh` has proved every path in it resolves from the repo root (see below). Not sharded: the v8 provider's branch records cannot be merged across shards without skewing condition coverage — issue #966 has the measurements and the fix |
| `fe-bundle` | `make fe-bundle` — the Vite production build plus the Storybook catalog build (stories must compile & register) |
| `frontend` | The fan-in the `ci` aggregate reads, standing for all three SPA jobs. Asserts all three succeeded: a failed lane must turn this fan-in **red, not skipped**, because a skip is what the aggregate reads as "this area was legitimately out of scope". The three run concurrently because they share no state; serially the lane was ~340s, of which vitest alone was ~207s, so the greps and the type gates sat behind a test run that could tell them nothing |
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
- **Off a pull request the scan additionally requires `integration` to have
  genuinely succeeded**, not merely not-failed. The scanner's Zero Coverage
  Sensor scores every executable line it holds no report for as *uncovered*, so
  a scan without the Go coverage producer publishes that code at 0% rather than
  declining to answer — and measuring nothing is not a measurement of zero. On a
  pull request the rule does not bite: new code there is the diff, and a diff
  that skipped an area has no lines of that area to cover. On `merge_group` the
  classifier is overridden and every producer runs, so the clause is satisfied
  whenever the lane is green; it is kept rather than simplified away because it
  is what would catch a future event, or a future skip, that reintroduces a
  partial scan.
- **A `merge_group` scan publishes as `main`** (`-Dsonar.branch.name=main`), and
  that is not a fiction: the queue builds `main` + the queued entries, which is
  byte-for-byte the tree `main` becomes when the batch merges. It is the same
  measurement the old push-to-`main` scan took, one step earlier and once per
  batch.

  Without the override the scanner reads `GITHUB_REF` and files the analysis
  under `gh-readonly-queue/main/pr-N-<sha>` — a branch deleted minutes later —
  and nothing would ever update `main` again. A stored analysis does not vanish
  when it stops being refreshed; it **freezes**. The nightly `quality-gate` job
  in `scheduled.yml` reads `?branch=main` and would keep reporting that stale
  verdict indefinitely. Its own comment says a *missing* analysis "reads
  identically to green on every dashboard"; a frozen one reads the same way.

  **Publication waits for the `ci` aggregate verdict**, and nothing weaker would
  do. This job's other conditions cover only its *coverage producers*, so without
  that clause a `merge_group` build whose `secret-scan`, `license-gate`,
  `craft-residue` or `dco` failed would still publish — a tree that does not
  merge, replacing the stored analysis every scheduled check reads for `main`.
  Requiring `needs.ci.result == 'success'` on `merge_group` is what makes
  "published as `main`" mean "became `main`". Nothing waits on this job in turn:
  the aggregate deliberately omits `sonarcloud` from its own `needs`, so the queue
  merges on `ci` alone and the scan may finish after the merge.
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

`ci.yml` is the merge gate, and `_lane-integration.yml` / `_lane-frontend.yml` are
part of it — called by it, never triggered on their own (see
[Two lanes are called](#two-lanes-are-called-not-inlined)). Seven workflows sit
beside the gate, deliberately outside it:

- **`cache-warm.yml`** — the Go build cache's only writer, on `main` every three
  hours plus manual dispatch. **Gates nothing**: a red or cancelled run costs
  latency on the next lane and nothing else. It exists as a separate workflow
  because the two things a `main` push used to do — reach a verdict, and seed the
  cache — have different homes now: the verdict moved to the merge queue, and the
  cache cannot follow it there (`actions/cache` scopes a write to the writing
  branch plus the default branch, and a queue ref is throwaway). See
  [The shared Go build cache](#the-shared-go-build-cache) for why it is scheduled
  rather than per-push.

- **`main-health.yml`** — every two hours on `main`: the backend gate plus the
  real-Postgres lane (called, not copied — it `uses:` `_lane-integration.yml`),
  and `main`'s SonarCloud analysis published from the coverage that lane produces.
  **It is not a gate and never will be**: it reports on a tree that has already
  landed.

  It exists because of a deliberate asymmetry. A merge can land over a red `ci` —
  a repository-role bypass is sanctioned here, to keep the fastest contributor
  fast — so breakage on `main` will keep happening and nothing in this workflow
  tries to prevent it. What it changes is the **delay and the attribution**:
  without it, a breakage is discovered when somebody else's unrelated pull request
  goes red for a reason they did not cause. On failure it files one issue per
  broken lane carrying the commits that landed since the health check was last
  green, with authors ([`scripts/main-health-range.sh`](../scripts/main-health-range.sh)).
  That range is a deliberate over-approximation: naming a dozen candidates is
  useful, guessing one sends the wrong person looking.

  It is also the **only** publisher of `main`'s SonarCloud analysis. The
  push-to-`main` scan is gone and the `merge_group` scan that replaced it only
  runs while the queue rule is enabled, which it is not — and a stored analysis
  does not vanish when it stops being refreshed, it FREEZES, while the nightly
  quality-gate job goes on reporting that frozen verdict as current.

  The cadence is the knob: two hours costs ~11 jobs a run and narrows the suspect
  range to roughly a dozen commits at eight merges an hour.

- **`scheduled.yml`** — daily on `main`, the checks whose answer changes when
  nothing is being merged. `ci.yml` asks "is this diff sound?" and runs because a
  diff exists; these ask "is `main` still sound?", which a PR gate structurally
  cannot answer. `govulncheck` runs against a vulnerability database that changes
  daily, so a per-PR scan proves the day it merged and nothing since. The
  **SonarCloud quality gate** is read through the API (not re-scanned) because it
  is no longer a required PR check — a gate nobody is blocked by is a gate nobody
  reads; the analysis it reads is published by the `merge_group` scan, per batch.
  And the **backend lane** re-runs unconditionally — the reason it was written is
  that `main`'s last-known-green was not evidence `main` was green, because a
  docs-only commit landing after a breaking one matched no classifier scope, so
  every gate skipped and the run reported green over a broken tree. That happened
  more than once. **The merge queue closes that hole, which makes this job
  redundant on paper** — it is kept deliberately as the one instrument that does
  not trust the queue. If it goes red while every `merge_group` build was green,
  the queue has a hole and this is how anyone finds out.
  The **frontend clock-drift** lane is the same argument at its purest: it runs
  the vitest suite as if it were 200 days from now and requires the same verdict,
  because a fixture whose absolute date the component compares to `now` is broken
  by the CALENDAR rather than by a diff — three tests began failing on a day
  nobody edited anything (#1977), and the classifier's frontend skip kept `main`
  green over them for a month. No static rule finds the next one: "an absolute
  date in a file that never pins the clock" matches 129 files, nearly all
  harmless, so the gate is a second run rather than a pattern.
  Findings become **issues** (`scripts/scheduled-report.sh`), one open issue per
  check keyed on an exact title, because a red scheduled run notifies nobody and
  these checks exist precisely for the case where nothing prompts a human to look.
  The reporting job is the sole holder of `issues: write` and runs no build code —
  the same permission isolation `sbom.yml` uses for signing.

- **`sbom.yml`** — **manual dispatch only; no automatic trigger at all** (the
  `sbom` job runs on any ref, `sign` only on `main`). Regenerates the source-tree
  SBOMs, license-gates them, and signs them from a separate job that is the sole
  holder of `id-token: write`. Signing is isolated from all branch-controlled code
  because a keyless signature lands permanently in a public transparency log and
  cannot be retracted, so a feature branch must never produce one — and the
  license gate stays on this path because `sign`'s `needs: sbom` is what keeps a
  policy-failing SBOM from reaching it.
  It previously ran on a path-filtered push to `main`, about 48 runs a week. That
  was dropped for the same reason as `release.yml` below: the runs drew on the
  20-concurrent ceiling the PR gates queue in, and with no releases yet they
  published bundles and burned irretractable Rekor signatures for trees no
  consumer would fetch. **No license enforcement was lost** — the `license gate`
  job in `ci.yml` (above) is job-gated on the `deps` scope, and it now runs on the
  merge queue as well as the pull request, so `main` receives a dependency change
  only through a queue build that gate passed. Not itself a required
  check; the mechanics are in
  [docs/reference/supply-chain.md](../docs/reference/supply-chain.md).
  Cancellation is scoped to the **`sbom` job**, not the workflow: a newer run
  supersedes a lane still cataloguing an older tree, but `sign` carries no group
  and cannot be interrupted — it writes to Rekor before the bundles upload, and a
  lane cut between the two would leave a permanent signature for a tree whose
  bundles nobody can fetch. Superseding therefore only takes effect *before*
  signing begins — while `sbom` is pending or running.
- **`release.yml`** — **manual dispatch only**, cuts a margince-constellation
  release versioned `1970.<build>` (the year pinned to the epoch while the
  flow is a PoC, so these releases order below any real dated release; the
  build is the workflow run number) in the dist service of the constellation
  deployment at test.margince.com. A constellation release is a server
  deployment, which GitHub does not host, so this is not a GitHub release —
  with one exception, the desktop bundles, below.
  It used to run on **every push to `main`**: about 400 runs a week, ~10
  runner-minutes each on arm64, three jobs apiece drawn from the same
  20-concurrent org ceiling the PR gates queue in — a full-stack merge already
  schedules 28 jobs against it. Releasing per commit spent that budget on
  versions nobody asked for, which the epoch-pinned `1970.*` scheme says out
  loud: the repository is under heavy development and has no real releases yet.
  A release is now a decision somebody makes. Two consequences are recorded
  where they bite rather than here — the role images lose their only build
  (the Dockerfile-only bullet above,
  https://github.com/gradionhq/margince-poc-v1/issues/1965) and the patch range
  degenerates to one commit (below).
  The release-management CLI cuts the
  incremental patch and uploads it with `draft-release`
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
  dist publisher token (the `MARGINCE_DIST_PUBLISHER_TOKEN` secret).
  **The patch range is now always `HEAD~1..HEAD`.** A dispatch carries no push
  range, so the base falls back to the parent commit — meaning a dispatched
  release's patch describes **one commit**, however many landed since the last
  release, and a consumer applying patches in order cannot use this stream to
  move forward at all. That is strictly worse than it was under the push trigger,
  where the range at least spanned the push; it is recorded rather than blocking
  because nothing consumes the stream today. Deriving the base from the last
  **published** release is what fixes it, and is the prerequisite for any
  automatic trigger ever coming back
  ([#1798](https://github.com/gradionhq/margince-poc-v1/issues/1798)).
  Concurrency still matters only for two deliberate dispatches: `draft` and
  `docker-image` each carry a cancelling group so a superseded bake stops, while
  `publish` carries a group that **serializes instead of cancelling** — a publish
  that has started always finishes, and a publish still pending when a newer one
  arrives gives up its place. That is mutual exclusion, not ordering: nothing on
  this path rejects a stale version, so a re-run or a dispatch of an older commit
  can still publish after a newer one
  ([#1810](https://github.com/gradionhq/margince-poc-v1/issues/1810)) — a
  sharper edge now that dispatching an arbitrary ref is the only way in.
  Not a gate — it never blocks a merge.

  **When the `desktop` dispatch input is set** (a checkbox on the Run-workflow
  form, default **off**), three further jobs attach the desktop bundles
  to a **GitHub** release under the same `1970.<build>` version, which is the
  page a person browses to download a build: `desktop-macos` and
  `desktop-windows` are *called*, not copied — the same reusable workflows the
  pull-request check runs, so a release bundle cannot differ from the bundle CI
  blessed — and `github-release` re-names the two artifacts after the version,
  re-zips the Windows tree that `download-artifact` expanded, and creates the
  release as a **prerelease** (a `1970.*` build must not present itself as the
  product's latest). It carries the only `contents: write` in the workflow. It
  needs `draft` and the two build jobs but deliberately **not** `publish`: the
  dist completeness gate is about the patch and the SBOMs, so a dist-side
  failure must not withhold bundles that already built correctly.
  The input exists because the trigger used to carry this distinction — a push
  got the dist release, a dispatch also got the bundles — and with the push
  trigger gone, `github.event_name == 'workflow_dispatch'` is true on every run,
  so it would have made every release compile Postgres from source twice. Default
  off keeps a dist-only release cheap; all three jobs share the one input so the
  GitHub release appears exactly when the bundles it would hold do.
- **`desktop-macos.yml` / `desktop-windows.yml`** — build the self-contained
  desktop folder for their own platform, which is the only platform it can be
  built on: pgvector has no build system but `nmake` against MSVC, the event bus
  needs MSYS2, and the macOS half rewrites every Mach-O load command to `@rpath`
  and re-signs each patched file. Path-scoped to `desktop/**` on pull requests
  so an ordinary change never pays for a Postgres compile, plus manual dispatch,
  plus `workflow_call` from `release.yml`. Neither is a required check. The
  macOS lane uploads a **tarball** because `upload-artifact` does not preserve
  the executable bit, and a `margince` a tester cannot run is worse than no
  artifact; the Windows lane has no such bit and uploads the folder.
