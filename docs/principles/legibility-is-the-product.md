# Legibility is the product

**Code that reads best to a human reads best to the next agent that edits it.**
Legibility is not polish applied at the end — it is the property that decides
whether the next change is cheap or dangerous.

The binding form is [*Craftsmanship*](../../CLAUDE.md#craftsmanship) in the
rulebook: the anti-tell catalog T1–T11 and the deterministic `craft static` gate,
diff-scoped and strict. It judges the **Go files a push changes** — a docs-only
push exits before it runs at all. This page is the reasoning
under it.

## Why this repo takes it as a principle rather than taste

Most of this codebase is read by someone — or something — that was not present
when it was written, holding no context beyond the file. Under that constraint
the usual trade-off inverts: a clever compression that saves a human author ten
minutes costs every later reader the reconstruction. The catalog exists because
each of its entries has already cost this tree a defect.

The sharpest instance: **a comment that reaches a machine reader needs its
caveat spelled out.** A rule about deriving SQL placeholders was drafted showing
`fmt.Sprintf("%s = $%d", col, len(args))` without saying what may fill `%s` — a
human would have inferred "not a request-body string"; the text as written would
have taught identifier injection to every future agent, because `AGENTS.md` goes
verbatim into the gate prompt.

## The method

**Comments say why, not what** (T1). The what is on the next line.

**Domain names, not `data`/`tmp`/`helper`** (T4). A name that could belong to any
function belongs to none.

**Never swallow an error** (T2). No `_ = f()`, no empty catch, no ignored
return. Errors flow through the sentinels; messages say what went wrong *and*
what to do, and never leak internals — no stack, no SQL, no table names to a
client.

**No dead or speculative code** (T3/T8). No abstraction without a second
concrete caller *today*. No `TODO` without an issue reference — an unreferenced
TODO is a decision nobody will make.

**Handle the honest hard cases** (T7): the empty page, version skew, a
cross-tenant id, an unset GUC. The happy path is the part that was never in
doubt.

**Tests prove behaviour or they are noise** (T11). No assertion-free test — it
can only fail by panicking. In a **unit** test: no `time.Sleep`, no real clock,
no real network — those are the flakiness sources, not virtues in themselves.
The integration lane is the opposite case and uses a real Postgres and a real
Redis on purpose, because a boundary faked on both sides proves nothing about
the boundary. Mock only true boundaries (DB, HTTP, clock, queue) and inject a `Clock`;
over-mocking that asserts call order tests your mock. Tests read as specs, and
the integration lane fails loudly without a database, because **a skipped
security gate looks exactly like a passing one**.

**Size ceilings measure what a reader must hold at once**: 80 code lines / 500
file lines for product code, 160 / 1000 for tests. The two ceilings count
differently, and it matters: `craft static`'s **function** ceiling discounts a
comment-only line, because an explanation reduces what a reader carries. The
whole-tree **file** cap in `scripts/check-go-file-length.sh` is a plain `wc -l`
and counts every line, with a ratchet file freezing each pre-existing offender.
A long scenario test
that sets up, acts and asserts once is not the god-function smell; a suite still
splits when it stops being navigable.

**Waive a genuine false positive in-source, with a reason**:
`//craft:ignore <check> <reason>`. A reasonless waiver is itself a finding.

**The pre-submit self-check**, in the author's own words: would a senior write it
this way? does it match the surrounding file? do the errors say what went wrong
*and* what to do? would a stranger find where this change lives without a guide?
is this the smallest diff that does the job?

## What this does not ask for

- **Not a style debate.** Formatting is `gofumpt`'s job and is not interesting.
- **Not uniformity across the tree.** The bar is "indistinguishable from a senior
  human's edit to *the surrounding file*" — match the file you are in.
- **Not a backlog to work through.** The tree was cleared to zero findings
  before the gate was armed. The rule is only that touched code is clean.
