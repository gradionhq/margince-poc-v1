# Contributing to Margince

Margince is built contract-first, largely by agents, under human
accountability. These rules keep the repository trustworthy at the three
horizons a reviewer judges it on — 90 seconds, 10 minutes, and adoption.

## Accountability

Whoever opens a pull request is **accountable** for every line in it and must be
able to **explain every line** — whether it was hand-written or AI-assisted. "The
model wrote it" is not an answer to a review question. Be honest in the PR's *AI
involvement* section about what was generated versus authored by hand; agent
assistance is expected and welcome, unexplained code is not.

## Developer Certificate of Origin (DCO)

Every commit must be signed off under the [Developer Certificate of
Origin](https://developercertificate.org/):

```
git commit -s
```

This appends a `Signed-off-by: Your Name <you@example.com>` trailer certifying
you have the right to submit the change under the project's license. The `dco`
CI job rejects any pull-request commit that is missing the trailer.

## The gates

`make check` is the merge gate — build, vet, lint, arch-lint, unit + fitness
tests, and generated-drift. `make test-integration` adds the real-Postgres RLS
and GDPR-erasure lanes. Both must be green before review.

The **craftsmanship gate** (`make craft-static`, ADR-0045) runs in CI after the
deterministic gates and is a required, no-override check: new or touched backend
code must be free of `BLOCKER` findings (swallowed errors, test sleeps). The
gate is foundation-owned and hash-pinned — fix it upstream in the skeleton, never
in this checkout (see [CLAUDE.md](CLAUDE.md) → *Craftsmanship*).

Write it right the first time: match the surrounding file, comments say *why*
not *what*, never swallow an error, and tests prove behaviour or they are noise.
The full anti-tell catalog lives in the spec's `architecture/15`.

## Where things go

- Implementation decisions → `decisions/`.
- Spec or ticket defects → a local note in `feedback/` (git-ignored scratch).
- Start every session at [STATUS.md](STATUS.md); update it when you finish.

The specification is the source of truth: when this code and the spec disagree,
the spec wins (principle P3).
