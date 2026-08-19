# ADR-0046 — A contributor must be able to explain every line they submit

**Status:** Active
**Decided:** 2026-06-25

## The decision

Anyone opening a pull request against this repository is accountable for the
code
in it and must be able to explain any line on request, however it was produced.
"An agent wrote it" does not answer a review question, and a maintainer may
close a
pull request whose author cannot explain it. Every commit carries a
`Signed-off-by` trailer under the Developer Certificate of Origin, certifying
the
contributor has the right to submit the code under this project's license.
Meaningful AI assistance is disclosed in the pull request — which tool and what
it
shaped — while trivial autocomplete needs no mention. Commits made inside the
project by its own agents are exempt from the disclosure step, because AI
authorship
is the standing assumption there; they meet the same quality bar through the
craftsmanship gate.

## Why

Maintainers across the open-source ecosystem are receiving AI-generated pull
requests in volumes that break code review. The reviewer can no longer assume
the
submitter understands what they submitted, which is the assumption review rests
on.
The sign-off is the lever: a contributor cannot honestly certify provenance for
code
they neither wrote nor understand. Without this, maintainer attention — the
scarce
resource — is spent explaining a change back to the person who opened it.

## What it binds in this repository

- `CONTRIBUTING.md` carries the three rules under the headings *Human
  accountability*, *AI disclosure*, and *Developer Certificate of Origin (DCO)*.
- `.github/PULL_REQUEST_TEMPLATE.md` makes them a structural step: it asks what
  changed, why, how it was verified, which parts were AI-assisted, and closes
with
  an explicit statement that the author can explain every line.
- The `dco` job in `.github/workflows/ci.yml` runs `scripts/check-dco.sh`, which
  fails the pull request when any commit in it lacks a `Signed-off-by` trailer.
- `cli/craft` and the `craftsmanship` CI job are the automated first line for
  external and internal changes alike; `make craft-static` runs the same bar
  locally, and `.githooks/pre-push` runs it diff-scoped before a push.
- `SECURITY.md` handles the one class of contribution that must not arrive as a
  public pull request: an exploitable weakness goes to a private advisory.

## History

Adopted from the retired specification, decided 2026-06-25. Rewritten in plain
language 2026-08-19. It is the external-facing sibling of the craftsmanship gate
recorded in ADR-0045, which sets the same quality bar for changes made inside
the
project.
