# ADR-0045 — A craftsmanship gate blocks machine-written slop before it merges

**Status:** Active — the deterministic arm blocks every push and every pull
request. The reviewing-model arm and its learning loop are **built but not
wired into any gate**; see below.
**Decided:** 2026-06-25

## The decision

Code in this repository is held to a craftsmanship bar that a compiler and a
test suite cannot express, and the bar blocks a merge rather than advising one.
The rules are a named catalog of the tells machine-written code carries:
swallowed errors, sleeps in tests, assertion-free tests, bare escape-hatch
types, dead speculative code, oversized functions and files, unreferenced
markers. A finding graded blocker or major stops the change; a minor finding is
advisory. A genuine false positive is waived in the source with a stated reason,
and a waiver without a reason is itself a finding. There is no per-change human
override.

## Why

The existing gates prove the code works, is safe, and is consistent. None
proves it is readable, and machine-written code passes all of them while being
unreadable — it compiles, the tests are green, the linter is quiet. This source
is the customization layer a customer edits and the surface a stranger reads, so
unreadable core is a product defect. An override on a quality gate becomes the
default escape under deadline pressure, which is why there is none.

## What it binds in this repository

- `cli/craft` is the gate binary, its own Go module; `cli/craft/static/` is the
  deterministic arm, a syntax-tree linter needing no compile of the target.
- `.githooks/pre-push` runs `craft static --strict` over the Go files the push
  changes against `origin/main`, in `backend/`, `extensions/`, and `fixtures/`
  alike. A blocker or major finding stops the push. `make hooks` installs it.
- `make craft-static` sweeps every hand-written Go tree, and the `craftsmanship`
  job in `.github/workflows/ci.yml` runs that same target as a required check,
  only after the deterministic gates are green.
- `make craft-residue` and the `craft-residue` job in the same workflow forbid
  any review marker remaining in the tree, so review scaffolding never reaches a
  reader.
- `make check-craft-doc` asserts `AGENTS.md` still carries its
  `## Craftsmanship` section — the copy of these rules an authoring agent reads
  before it writes anything.
- The size ceilings live in `cli/craft/static/runner.go`: 80 code lines per
  function and 500 per file for product code, 160 and 1000 for tests. A
  comment-only line does not count, because the ceiling asks how much a reader
  must hold at once and an explanation reduces that.
  `scripts/check-go-file-length.sh` holds the whole-tree file ceiling
  independently.

## What is owed

The record decided a two-armed gate, and only one arm runs.

**The reviewing-model arm is not wired into anything.** `cli/craft` implements
`review`, `verdict`, `annotate`, `dispute`, `eval`, `version`, and `upstream` —
the whole heuristic half, including the marker-writing loop, the dispute route,
and the report that proposes promoting frequent blockers into the authoring
guardrails. No Makefile target, workflow job, or git hook calls any of them.
Only `craft static` and `craft residue` run. A change that is ugly in a way the
syntax tree cannot see merges today.

Three pieces are absent rather than merely unwired, and each must exist before
the arm could sensibly block. There is no golden set of judged examples to
measure the reviewer's precision against, so nothing can gate a version
promotion. There is no human adjudication queue for a disputed finding, which is
what the record offers instead of an override. There is no recorded verdict
history, so the trend report has nothing to read. Their design is not final.
Wiring the reviewing arm as a blocking gate without them would put an unmeasured
judge in the merge path.

## History

Adopted from the retired specification, decided 2026-06-25. Rewritten in plain
language 2026-08-19.

The source records one amendment, dated 2026-07-05, which is why the built half
is the half that runs. The original decision framed the gate as a single
reviewing agent. Much of the catalog turned out to be decidable straight from
the syntax tree, where a model is slower, costlier, and less reliable than a
mechanical check. The amendment added `craft static` as a first-class arm of the
same gate, in the same binary, running first.

The tree was swept to zero findings before this bar was armed, so there is no
grandfathered backlog: code you touch comes back clean.
