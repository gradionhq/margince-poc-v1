# The record is the code

**What this product does is defined by its code, tests, migrations and
`backend/api/crm.yaml` — not by any document describing them.** A document goes
stale silently. A test goes stale loudly, *if* it still exercises the obligation
it was written for — see
[derive the obligation](derive-the-obligation.md#writing-a-gate-that-holds)
for the ways a green one can prove nothing.

This replaces the older arrangement where a separate specification outranked the
tree. It is the principle behind the precedence order in
[CLAUDE.md](../../CLAUDE.md#what-decides-a-question-here), and the reason that
order puts the running software above every prose artefact except the current
request and the guardrails.

## Why the order is that order

1. **The explicit current request** — what someone is asking to change.
2. **Code, tests, migrations, the contract** — what the product does *today*.
   These are the record, not a description of it.
3. **Guardrails** — security, privacy, agent authority, auditability, contract
   compatibility, licensing, durability. Enforced by tests wherever possible,
   *and the test is the thing to read*: it states the obligation in a form that
   fails when the obligation stops holding.
4. **[docs/](../)** — how the product is built and operated.
5. **Retired material** — history. It never blocks work on its own.

The load-bearing step is 2 above 4. A doc that disagrees with a green test is
wrong about the product, and the fix is to change the doc — not to argue from
it.

## The method

**When two sources disagree, run the code.** Not "read both and decide which
sounds more authoritative" — execute the test, read the migration, open the
contract. One of them is the record.

**When you change behaviour, the change lands in the record first.** A migration
and a test, then the doc that describes them. A doc-only change that claims new
behaviour is a lie with a commit hash.

**Findings route by durability**, and this is the rule that keeps the record
readable:

| The thing you found | Where it goes |
|---|---|
| An implementation decision you made | the commit and the PR — git history is the record |
| A decision that binds future work | raised with the team, where the reasoning is kept |
| Something found but NOT fixed here | a GitHub issue in this repo, labelled on all three axes — **unless it is an exploitable weakness**, which goes to a private Security Advisory and never a public issue ([nothing here is private](nothing-here-is-private.md)) |
| Open work / the session pickup point | [STATUS.md](../../STATUS.md), kept to open work only |

**No build-process residue in comments.** No review-ticket numbers, no fix
narration, no "changed in response to". State the invariant so it stands alone.
The story of how the code got this way is in git; a comment retelling it is a
second, worse copy that cannot be queried and will not be updated. Same for test
names.

**Never rationalize a known gap in a comment.** Restructure it away or gate it
with a test. A comment explaining why something is broken is a defect with
documentation.

## What this does not ask for

- **Not "documentation does not matter".** `docs/` is step 4, not step none. It
  explains how the product is built and operated, and that is work the code
  cannot do.
- **Not "refuse to evolve because an older document says otherwise".** Name the
  conflict, say what it costs, and keep building. If the change touches a
  guardrail, say so in the PR so the decision behind it is updated with the
  code.
- **Not "tests are documentation".** A test states one obligation precisely. It
  does not orient a newcomer, and pretending it does is how a tree becomes
  unreadable to everyone who did not write it.
