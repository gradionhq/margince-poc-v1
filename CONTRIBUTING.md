# Contributing to Margince

Margince is source-available (BUSL-1.1) and AI-native: most of this code
is authored by agents under human accountability. Contributions are
welcome — held to the same craftsmanship bar as our own AI-authored code.

Participation is covered by our [Code of Conduct](CODE_OF_CONDUCT.md),
which includes one clause specific to a repository built this way:
submitting volume you cannot explain is treated as disrespect for
reviewers' time, not as enthusiasm.

## Human accountability

**You are accountable for every line you submit, and must be able to
explain every line.** AI assistance is welcome and expected;
unexplainable, slop-flooded contributions are not. If you cannot explain
why a line is there, what it does, and why it is correct, it is not
ready. "The model wrote it" is not an answer to a review question.

This is the project's one non-negotiable. It is why we ask for the
disclosures below — not to discourage AI use, but to keep a human
answerable for the result.

## AI disclosure

Disclose AI involvement proportionately in the PR description:

- **Assisted** — you wrote/directed it with AI help (autocomplete,
  review, refactor). The default.
- **Generated** — AI produced substantial portions you then reviewed
  and own.

There is a deliberate **internal/external asymmetry**: Margince's own
build agents author by design and do not disclose per-PR (it is the
stated practice, A39); external contributors disclose so a human
reviewer knows what they are accountable for. Either way, the same
gates apply (below).

## Developer Certificate of Origin (DCO)

Every commit must be signed off under the [Developer Certificate of
Origin](https://developercertificate.org/):

```
git commit -s
```

This appends a `Signed-off-by: Your Name <you@example.com>` trailer
certifying you have the right to submit the change under the project's
license. The DCO check is required — a pull-request commit without the
trailer blocks the merge. Amend with `git commit --amend -s` if you
forget.

## A working tree in four commands

You need **Go ≥ 1.26**, **Docker**, `golangci-lint`, and Node with pnpm
for the frontend half.

```
make install    # FE deps, gate tools, and the git hooks — run once
make db-up      # PG16 + Redis 7 containers, and the app role
make migrate    # core + custom migrations
make dev        # the whole stack: API on :8080, worker, Vite
```

Two things about `make dev` that save an hour of confusion. It **sweeps
first** — exactly one dev stack may run on the machine, so a bare
`make dev` kills every other margince process before booting, and is
therefore always safe to run. And the API is **compiled**: Vite
hot-reloads the frontend, the binary does not, so every backend change
needs `make dev` again. A stale binary on :8080 is indistinguishable from
a broken feature.

Full target list, including `DEV_SLUG` for an isolated parallel stack:
[docs/reference/make-targets.md](docs/reference/make-targets.md).

## Branch, commit, merge

- **Branch off `main`**: `git switch -c <type>/<slug> origin/main`.
  Direct pushes to `main` are blocked; there is no other path to merge.
- **Conventional commit subjects**, scoped to the module:
  `fix(overlay): a mirrored deal reports the incumbent's last-modified`.
  Write the subject as the behaviour after the change, not as the task
  you performed.
- **Sign off every commit** — `git commit -s`; see DCO above.
- **Squash-merge** is the house style, and only over green checks.

### Contributing from a fork

Be aware before you start: **a pull request from a fork cannot currently
be merged here.** One of the required checks is a SonarCloud analysis,
and its token is deliberately withheld from fork-triggered workflows, so
the check can never report and the merge stays blocked — a limitation of
our gate wiring, not a judgement about your change. If you plan more than
a drive-by fix, open an issue first and ask for write access so you can
push a branch in this repository instead. Small fixes are still welcome
as fork PRs; a maintainer will land them for you.

## The gates

Every change — code, docs, and config alike — lands through the same
loop your PR will run:

1. **`make check`** is the merge gate: build, vet, lint (baseline +
   new-code strict), arch-lint, unit + fitness tests, generated-code
   drift, contract breaking-change, test-lane hygiene, image pins, and
   the file-length ratchet. Add `make frontend-check` when `frontend/`
   changed; `make test-integration` runs the real-Postgres RLS and
   GDPR-erasure lanes (needs `make db-up`).
2. The **craftsmanship gate** (`craft static --strict`) runs
   diff-scoped on every push once hooks are installed (`make hooks`):
   new or touched backend code must be free of `BLOCKER` **and**
   `MAJOR` findings — a swallowed error, a sleep in a test, a bare
   `any` in a signature, a two-bool signature, an assertion-free test,
   or a function over 80 code lines (160 in `*_test.go`; a comment-only
   line is not counted, but a trailing comment does not make its code
   line free) all stop the
   push. `MINOR` is advisory. There is no grandfathered backlog: the
   tree was cleared to zero findings before this bar was armed, so
   `make craft-static` is green and CI runs the same bar as a required
   check. A *genuine* false positive is waived in-source
   with a reason: `//craft:ignore <check> <reason>`. The gate tool
   (`cli/craft/`) is part of this repo — don't edit it to silence a
   finding on your own PR; fix the gate in its own reviewed change.
3. **CI must be all green before merge**: the same deterministic gates
   plus DCO, automated review, and static analysis. Address findings
   rather than dismissing them; squash-merge is the house style.

Write it right the first time: match the surrounding file, comments say
*why* not *what*, never swallow an error, and tests prove behaviour or
they are noise.

## Where things go

- Implementation decisions — anything the specification left open that
  the code had to decide — are explained in the commit message and PR
  description that makes the change; git history is the record.
- Session state and pickup points live in [STATUS.md](STATUS.md);
  start there, and read [AGENTS.md](AGENTS.md) for the binding
  engineering rules.
- Defects and proposals go to GitHub issues, which are templated: a bug,
  a **deferred follow-up** (anything you found but deliberately did not
  fix in the change at hand — that becomes an issue rather than a
  comment in the source), a capability gap or proposal, or a
  documentation defect. Security vulnerabilities go through
  [SECURITY.md](SECURITY.md) (private reporting), never a public issue.
- **This repository is public.** Nothing you write in an issue, a PR, or
  a commit may carry a secret, customer or personal data, a local
  machine path, or a private specification path or its contents — cite
  the spec by chapter, ADR, or pin ID instead.

Margince is built contract-first: `backend/api/crm.yaml` is the
authoritative surface, and when this code and the specification
disagree, the specification wins.

## Before you open a PR

- Keep the PR scoped, and let it tell a story: what, why, and how it
  was verified.
- Run the pre-submit self-check in [AGENTS.md](AGENTS.md) →
  *Craftsmanship*.
- `make check` is green locally and every commit is signed off.
