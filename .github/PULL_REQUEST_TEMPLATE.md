## What

<!-- The change, in one or two sentences. What behaviour is different after this PR? -->

## Why

<!-- The reason this change exists. Link the issue or decision record it
     implements. State the invariant, not the fix narration. -->

## How verified

<!-- The gates you ran and what they proved. At minimum: `make check` green, and
     `make test-integration` if this touches tenant data, RLS, or the write shape.
     Name the manual flow you drove if the change has a runtime surface. -->

- [ ] `make check` is green
- [ ] `make test-integration` is green (or: this change does not touch tenant data / RLS / the write shape)
- [ ] `make craft-static` reports no new blockers
- [ ] This diff and description carry no secrets, no customer data, no local machine paths, and nothing quoted from or pointing at a private document — a decision is cited by its number (this repository is public)

## AI involvement

<!-- Which parts were AI-assisted, and how. This repo is built by agents under
     human accountability — be honest about what was generated vs. hand-written. -->

## Accountability

By opening this PR I confirm I am **accountable** for this change and can
**explain every line** in it — human-written or AI-assisted. Commits are signed
off (`git commit -s`, DCO). See [CONTRIBUTING.md](/CONTRIBUTING.md) and the
[Code of Conduct](/CODE_OF_CONDUCT.md).
