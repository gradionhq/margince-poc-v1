# Contributing to Margince

Margince is source-available (BUSL-1.1) and AI-native: most of this code
is authored by agents under human accountability. Contributions are
welcome — held to the same craftsmanship bar as our own AI-authored code.

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

## The gates

Every change — code, docs, and config alike — lands through the same
loop your PR will run:

1. **`make check`** is the merge gate: build, vet, lint (baseline +
   new-code strict), arch-lint, unit + fitness tests, generated-code
   drift, contract breaking-change, test-lane hygiene, image pins, and
   the file-length ratchet. Add `make frontend-check` when `frontend/`
   changed; `make test-integration` runs the real-Postgres RLS and
   GDPR-erasure lanes (needs `make db-up`).
2. The **craftsmanship gate** (`craft static`) runs diff-scoped on
   every push once hooks are installed (`make hooks`): new or touched
   backend code must be free of `BLOCKER` findings (swallowed errors,
   sleeps in tests). A *genuine* false positive is waived in-source
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
- Defects and proposals go to GitHub issues. Security vulnerabilities
  go through [SECURITY.md](SECURITY.md) (private reporting), never a
  public issue.

Margince is built contract-first: `backend/api/crm.yaml` is the
authoritative surface, and when this code and the specification
disagree, the specification wins.

## Before you open a PR

- Keep the PR scoped, and let it tell a story: what, why, and how it
  was verified.
- Run the pre-submit self-check in [AGENTS.md](AGENTS.md) →
  *Craftsmanship*.
- `make check` is green locally and every commit is signed off.
