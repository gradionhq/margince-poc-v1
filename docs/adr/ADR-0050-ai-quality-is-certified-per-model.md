# ADR-0050 — AI quality is certified per model, not promised uniformly

**Status:** Active
**Decided:** 2026-06-29

## The decision

The product does not claim that every supported model produces equally good
results. Each AI task is certified separately against each model binding an
operator may configure. A certification run drives the real task against a
fixed scenario corpus several times, scores each run against the scenario's
expected outcome, and folds the runs into one of three verdicts: certified,
supported-degraded, or not-supported. Governance is excluded from this
grading — approval tiers, scope checks and audit run below the model and
hold identically for every provider, so a model that fails a governance
check is simply not supported.

## Why

A deterministic assertion cannot certify a non-deterministic model, and
"does this feature work" has no single answer when the feature may run on
any of several models the product does not control. Picking one blessed
model would break the promise that an operator supplies their own
inference. Promising that all models work equally would be unprovable and
would hide a regression when a provider ships a new version.

## What it binds in this repository

- `backend/internal/compose/aicert/` holds the whole certification machinery.
- `make e2e-ai` runs the certification suite (`TestE2ECertify`) against the
  binding in the operator's own `ai-routing.yaml`, which is created from
  `config/ai-routing.example.yaml` and never committed. It calls real models over the
  network, so it is opt-in and not part of `make check`.
- `make e2e-ai-report` prints the readiness report — every shipped task,
  its band, and whether its record is current, stale or absent.
- `make ai-probe` drives one task site against input an operator supplies,
  through the same certification case, to answer whether a site survives a
  specific input rather than whether a model is good enough overall.
- `backend/internal/compose/aicert/score.go` defines the three verdicts as
  `VerdictCertified`, `VerdictSupportedDegraded` and `VerdictNotSupported`,
  and folds an odd number of runs by median score against the scenario's
  bands.
- Each scenario declares three thresholds — a certified minimum, a degraded
  minimum, and a floor. `bands_test.go` refuses a corpus whose degraded
  minimum sits above its certified minimum, and refuses one with the bands
  omitted.
- `backend/internal/compose/aicert/corpus/` carries the hand-authored
  scenarios, twenty task directories today including `capture_classify`,
  `draft_reply`, `enrich`, `summarize`, `site_extract` and `agent_loop`.
- `backend/internal/compose/aicert/records/` carries the signed-off
  certification records. `corpus_test.go` fails when a record claims a band
  for a task whose prompt does not ship.
- `backend/internal/compose/aicert/promptversion.go` ties a record to the
  prompt it was earned against, so editing a prompt invalidates the record
  rather than silently inheriting its verdict.
- `backend/internal/compose/aicert/judge.go` runs the graded half; the
  scored run's own pass or fail against its expected outcome and latency cap
  is the hard half.

## History

Adopted from the retired specification, decided 2026-06-29. Rewritten in
plain language 2026-08-19. The source is marked proposed; the machinery it
describes has shipped, so this record is active.

The source frames certification as a matrix over `{use case} × {supported
AI}` published as a customer-facing supported-agents page. What shipped is
per-task certification records held in the repository for the operator's own
binding, with no published matrix. The source also names a task-completion
harness that drives an external agent over the MCP tools and asserts the
resulting database state; the `agent_loop` corpus entry is the nearest
thing, and it runs inside the same certification suite rather than as a
separate harness.
