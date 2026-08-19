# ADR-0121 — The audit trail is what makes an agent's change reversible

**Status:** Active as a direction, **not built**. The trail records who changed
what; it does not reliably record what the value was before, and nothing in the
product puts a value back. Both halves are outstanding work — see
*What still has to happen*.
**Decided:** 2026-08-19

## The decision

An agent acts as the human who authorized it. A change an agent makes is
recorded, attributed and reversible the same way a change that human made by
hand would be. The audit row is what makes that true, so it must carry the prior
value of every field the change touched, and there must be a way to put those
values back.

This replaces asking a person to approve the change before it happens. Staging
thirty-six tool operations for confirmation is the wrong trade: nobody reads the queue, the
agent becomes useless for real work, and an approval nobody actually considers
is not a control. Attribution plus reversal is the control that survives contact
with daily use.

Three things stay confirm-first permanently, because no trail can undo them:

- **Outbound sends** — email, messages, meeting invitations. The message has
  left the building.
- **Erasure** — the law requires it to be irreversible. An undo here would be a
  defect.
- **Merges** — merging destroys the separation a reversal would need. The
  product already refuses field-level un-merge for this reason: `POST
  /leads/{id}/demote` calls it lossy and ambiguous and declines to attempt it.

Everything else — create, update, archive, promote, enrich, advance — executes
directly once the trail beneath it is sound.

Reversal is a new, audited change, never an edit of history. Putting a value
back writes its own audit row naming the row it reverses. The undo can itself be
undone, and an auditor sees both events, which is the truth of what happened.

## Why

The confirmation tier was designed when an agent's change could not be seen or
undone. Given that, holding the change for a human was the only available
control. Neither premise has to stay true, and the cost of keeping them is
high: a queue people click through without reading gives the appearance of
governance and none of the substance.

The alternative most products reach for is versioning every table — keep every
prior row, reconstruct any record as of any moment. That is rejected here, and
not on cost grounds. A history table holding every prior version of a person's
data is a second copy of exactly what erasure exists to destroy. We anonymize in
place so nothing survives; full versioning would mean building erasure a second
time, into the history, and the two would have to agree forever. The real need
is "put back what this one change did", which is a much smaller thing.

The product already has three working instances of the smaller thing, none of
them reachable for an ordinary record.

`voice_profile_version` keeps every prior version of a voice profile and rolls
back to a chosen one, writing a `restore` audit row and a delta row classified
`rollback` (`backend/internal/modules/ai/voice_versions.go`). That is real
version history, built once, for one entity.

The dedupe queue restores a resolved decision the same way
(`backend/internal/modules/people/dedupequeue.go`).

And `POST /leads/{id}/demote` reverses a promotion. It reads the original outcome
from the promotion's audit row rather than re-deriving it, because re-running
the ladder would answer about today's data instead of what actually happened.
And it **refuses rather than guesses**: if the promoted person owns a deal it
returns 422 and changes nothing, and it declines field-level un-merge outright
as lossy. That is the shape every other reversal should copy.

Three entities can be rolled back and the records a person actually works with
cannot. The mechanism is not missing — it has been built three times. What is
missing is the trail underneath it being good enough to build it a fourth time
generically.

## What it binds in this repository

- `audit_log` carries `before` and `after` as `jsonb`, plus `actor_type`,
  `actor_id`, `passport_id` and `on_behalf_of` — created in
  `backend/migrations/core/0012_audit_log.up.sql`. The attribution half of this
  decision is already true: an agent's row names the human it acted for.
- `restore` is a declared value of `audit_log.action` and of the
  `AuditLogEntry.action` enum in `backend/api/crm.yaml`, and two paths already
  write it: `backend/internal/modules/ai/voice_versions.go` rolls a voice
  profile back to a chosen earlier version, and
  `backend/internal/modules/people/dedupequeue.go` restores a dedupe decision.
  No ORDINARY record — person, deal, lead, activity, organization — can be
  restored by anything.
- `storekit.Audit` in
  `backend/internal/platform/database/storekit/storekit.go` takes `before` and
  `after` as `any`. Both are optional, and that is the defect this record names:
  the shape is each caller's choice.
- `GET /audit-log` returns `before` and `after` in `AuditLogEntry`, and
  `AuditLogRow` in `frontend/src/screens/settings.tsx` renders a field-level
  diff from them. The read path exists end to end.
- `POST /leads/{id}/demote` is the worked example of an audited reversal, and
  `backend/internal/modules/people/demote.go` implements it.
- The confirmation tiers are declared per operation as
  `x-mcp-tool: { tier: confirmation_required }` in `backend/api/crm.yaml`, and
  generated into `backend/internal/compose/agentpolicy_gen.go`. Thirty-six tool
  operations carry it today, plus one (`advance_deal`) that resolves its tier at
  run time from the stage it moves to.

## What still has to happen

Nothing below is built. It is written out because the direction is settled and
the work is not, and because a decision recorded without its gap invites
somebody to assume the gap is closed.

**1. Find out what the trail actually holds.** There are 236 `Audit` call sites.
Each author chose what to pass. Some pass matched before-and-after maps —
`auditChannelIdentityChange` in
`backend/internal/modules/people/channelidentity.go` is one. Many pass `nil` for
`before` and a hand-built map for `after`; the enrichment write at
`backend/internal/modules/people/enrich.go` is one of those. Nothing has ever
validated the shape.

This is why the audit screen looks the way it does. Expanding an entry whose
`before` is nil produces an empty panel — `diffKeys` finds no keys, so the row
opens and shows nothing, without saying why. The screen is working; the data
under it is thin.

The first piece of work is therefore a survey, not a fix: read what is stored
across the call sites and report which changes could be reversed today and which
could not. Estimates are not a substitute — this decision was drafted three
times on estimates of that data and each was wrong.

**2. Make `before` complete and mandatory.** Every audit row for an `update`
carries the prior value of every field it changed. Enforced by a test, the way
`backend/writeshape_test.go` already enforces that an audit row and an outbox
event commit together. The hard half is completeness rather than presence: a
write touching five columns whose audit names three yields a reversal that
restores three and silently drops two.

**3. Build the reversal path**, following `demote`: read the `before` image,
write it back as a new change, stamp a `restore` audit row naming the row it
reverses. Refuse rather than guess wherever the reversal is ambiguous.

**4. Put it on the screen.** The record's history with an undo control on each
entry, and a plain statement on any entry that cannot be reversed, saying which
of the reasons applies.

**5. Only then, cut the tiers.** Thirty-six operations down to the handful that
stay confirm-first — the outbound sends, erasure and merge named above. Dropping the confirmation before the reversal exists removes
the brake before the reverse gear is fitted.

The order matters and steps 1 and 2 are the long pole. Step 2 earns its cost
even if the rest is never built: "what did this record look like before the
agent touched it" is the question an auditor asks, and today the answer depends
on which developer wrote the call site.

**Deliberately not in scope: reconstructing a record as of an arbitrary past
moment.** Reversal is per change. Undoing ten changes means ten reversals, in
order, and where another person edited in between the result is ambiguous and
the product should say so rather than resolve it. If "show me this deal as of
last Tuesday" is ever wanted, it is a reporting feature with cheaper answers
than versioning every table.

## History

New decision, 2026-08-19. Not adopted from the retired specification — the
confirmation tiers it revises were, but the reversal direction is a change of
course taken here.

It was drafted against three successive wrong readings of the audit trail,
which is why the survey is step 1 rather than an assumption: first that every
prior state was on disk, then that a quarter of writers dropped it, then that
nothing read the columns back at all. The read path is in fact complete —
`GET /audit-log` returns both images and the settings screen diffs them. What is
uneven is what the 236 writers put in.
