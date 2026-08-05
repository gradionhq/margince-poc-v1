# The relationship graph — participants, the interaction edge & deal coverage

Margince's answer to *"is this account cold?"* is not a guess from a last-touched timestamp. It is a
projection folded out of who was actually in each conversation: **`activity_participant`** records the
parties, **`graph_interaction_edge`** folds them into one row per (colleague, contact), and the
coverage read turns that into named, drillable risk findings on a deal (ADR-0078).

The substrate arrives from two places. Captured mail and hand-logged calls produce *participants* —
see [capture-connectors.md](capture-connectors.md) for the ingest side. A member's own LinkedIn export
produces *ghosts*, a strictly weaker tier that never becomes a contact — see
[how-to/import-your-linkedin-network.md](../how-to/import-your-linkedin-network.md). This page is
about the first: the graph built from recorded contact.

## The question it answers

A rep about to write into a new account wants one thing before they draft: **does anybody here already
know these people, and how well?** A cold account and a warm one look identical on a company page —
same fields, same logo, same empty pipeline — and the difference between them is entirely a matter of
who on the team has a real exchange on file.

Two surfaces answer it, and they are the same fold read two ways:

- `GET /people/{id}/network` — *who on our team knows this contact*, warmest first.
- `GET /deals/{id}/coverage` — *who covers this deal, and what is wrong with how it is covered*.

The agent surface asks the same questions through the same seams: `who_knows`, `account_coverage`,
`intro_path_to` and `at_risk_relationships` are all 🟢 read tools bound to `ScopeRead`, and they reach
records only through the row-scoped reads the HTTP surface uses — so a governed tool can never see
further than the human driving it ([agent-surface.md](agent-surface.md)).

## Participants — the fact the schema could not previously state

`activity_link` records which **records** an activity concerns. It has no user arm, so nothing anywhere
recorded which of *our* people was in the conversation. `activity_participant` (ACT-DDL-3, migration
`0157`) is a row per party, with three identity arms that are deliberately not interchangeable:

| Arm | What it means |
|---|---|
| `user_id` | **Our** side — the arm the interaction edge is keyed on. |
| `person_id` | A known counterparty, already a contact. |
| `address` | A party who never became a record. Kept, not dropped: an unresolved attendee is a fact about the meeting. |

A row must name somebody (`activity_participant_identity` CHECK), the role set is closed at the
database (`from`, `to`, `cc`, `attendee`, `organizer`), and a uniqueness index over
`(activity_id, role, user_id, person_id, address)` — coalescing the NULL arms so the constraint is not
vacuous for address-only rows — makes every writer's insert a free no-op on replay.

### Why capture must write it in the ingest transaction

The mailbox owner is knowable **only from the connector principal**. `capture_connection` is
per-user-per-provider, so the registry stamps the granting human onto the principal that runs the sync;
by the time any other module sees the activity, its `captured_by` reads `connector:gmail` and the human
behind it is unrecoverable. That is the ratified reason `capture` writes a table `activities` owns —
`backend/tableownership_test.go` carries it verbatim, and a reasonless or stale waiver fails the gate.

Capture writes the counterparty as an **address**, not a person: the tiered creation gate runs *after*
that transaction commits, and for a suppressed sender it never runs at all. Direction decides the roles
and nothing else — our user is `from` on outbound, `to` on inbound — which is exactly what lets the
fold tell a real exchange from a hundred unanswered sends.

### Which module writes which arm

Four modules touch the table, and each ratification in the ownership gate states its own reason:

| Module | What it writes | Why it, and not the owner |
|---|---|---|
| `activities` | **Owner.** The hand-logged path: the human who logged it plus the people they linked, with the same direction-derived roles capture stamps. | — |
| `capture` | Both arms of a captured message, in the ingest transaction. | The connector principal is the only place the mailbox owner is known. |
| `people` | Promotes the address arm to a `person_id` at `linkActivityToPerson`. | That is the one chokepoint every ensure path reaches **and** the one that has already settled the person against a merge — so naming the party is the same write, on the same row, in the same transaction as the link. |
| `privacy` | Erasure deletes rows whose only identity is the subject, and nulls the subject's arms on rows that also name one of our users. | The address arm exists precisely for a party who never became a record, so it survives the `person_email` purge and would keep an erased address readable and re-matchable. |

One party gets one row: `namePersonAmongParticipants` **updates** the address row rather than adding a
second, and leaves it alone if a person row for the same `(activity, role)` already exists — that is a
second address for a party already recorded, not a second party.

### Recovering history

Every message already in the timeline predates ACT-DDL-3, so a workspace with years of mail would read
empty — indistinguishable, to the person looking at it, from a broken feature.
`BackfillParticipantsBatch` recovers two classes, class 1 winning over class 2:

- **Class 1** — `captured_by` reads `human:<uuid>`. Exact, no inference.
- **Class 2a** — `captured_by` reads `connector:<provider>:<user>`. Exact; every row captured since
  that provenance shipped.
- **Class 2b** — older rows stamped `connector:<provider>` alone, attributable **only** when the
  workspace has exactly one connection for that provider. With two, the row stays unattributed rather
  than attributed to a coin flip: a wrong edge tells someone to ask a colleague who has never met the
  contact.

It carries no cursor. The predicate is "an activity with no participant rows from which at least one
participant is derivable", and every selected activity gains a row — so the remaining set strictly
shrinks, the caller runs it until it returns zero, and a batch that dies half-committed is simply
re-selected. The `participant_backfill` dispatcher fans out per workspace every `24h`; one tick writes
25 transactions of 500 activities and then yields, and a caught-up workspace stops at the first empty
batch. **Deliberately not recovered:** parsing raw `From`/`To`/attendee headers out of the stored
originals — that pass reads message bodies, needs its own address-matching rules, and its failure mode
is silently mis-attributing a meeting.

## The projection — one fold, two paths onto it

`graph_interaction_edge` (CG-DDL-1, migration `0158`) is one row per `(workspace, user, person)`
holding counted facts and exact moments: `last_at`, `last_inbound_at`, `last_outbound_at`, the 90-day
counts, and the lifetime total. It is a **projection** — it holds no fact of its own, carries no id, no
version, no `audit_log` row and no `event_outbox` row, and can be thrown away and rebuilt at any time.
That rebuild *is* the corruption remedy, and it is why the table can afford to carry no audit trail.

**The 0–100 strength is deliberately not stored.** It is a pure function of `(row, now)` computed at
read by `relstrength.Compute` — a decayed score is wrong the moment the clock moves, and storing it
would mean either a lie or a nightly job rewriting every row to change nothing anyone cannot derive.

The maintenance rule is **recompute, never increment**. The bus is at-least-once, so an increment
double-counts on redelivery; and merge, archive and erasure all correct history *backwards*, which an
increment cannot express at all. Recomputing a pair from the base tables is idempotent by construction.

Two paths keep it true, and they converge on the same statement:

- **Incremental** — the `cg:graph-edge` consumer. `activity.captured` / `.updated` / `.archived` and
  `retention.applied` refold the pairs the activity's participants imply — resolved from the
  participant rows themselves, so a **relink** also refolds the pair the activity *used* to belong to,
  which is the pair the event could not have named. `person.merged` drops the source's edges and
  refolds the survivor; `person.archived` / `.restored` / `.updated` / `.created` refold that contact.
  `user.deactivated` does nothing at all — reads filter through the live-member join, so a departure
  takes effect without rewriting a row.
- **Nightly reconcile** — the `graph_edge_reconcile` dispatcher (`24h`) fans out `graph_edge_workspace`,
  which clears and refills the whole projection in **one transaction**, so a reader never sees an empty
  graph. It runs daily for a reason no event can supply: the 90-day window counts go stale purely by
  the passage of time. The migration states that bound out loud — a count may be up to 24h
  over-inclusive, while recency, which dominates the score, is exact.

A determinism fixture is what keeps the two from drifting: a rebuild and a stream of incremental
recomputes over the same history must agree.

Deleting matters as much as writing. A pair whose last qualifying interaction was just archived loses
its row — an edge that outlives its evidence is a colleague being recommended for an introduction they
can no longer make.

Counting is over **distinct activities**, never over join rows. One message produces a participant row
per party per role, so a contact who is both a `to` and a `cc` would otherwise count that single
message twice — inflating frequency, and so the score, on exactly the busy threads a relationship score
is meant to read.

## Every participant role feeds an edge, cc included

The qualifying role set is **every role there is**:

```go
const interactionRoles = `('from','to','cc','attendee','organizer')`
```

This is a deliberate reversal, recorded in the contract (ADR-0078, founder decision 2026-07-31). The
market convention is to exclude `cc`, on the argument that being copied is not a relationship. It was
excluded here first and then reversed, and the reasoning is worth keeping intact: **in the accounts
this product is built for, the person who is always in copy on the thread is frequently the one who
actually knows the customer** — the account lead cc'd on their team's correspondence, the partner
copied on every exchange. Dropping `cc` did not remove noise so much as remove exactly those people
from the answer to "who here knows them".

The quality bar sits in the **reciprocity term**, not in a role filter. A colleague who is only ever
copied has one-directional traffic, so reciprocity floors them well below someone in a real exchange.
They appear, ranked where they belong, instead of vanishing.

**What does not make an edge**, and why:

- **Anything outside `email`, `call`, `meeting`** (`relstrength.IsInteractionKind`, rendered into SQL
  from the same list so the four producers cannot drift). A task is *intent* and a note is a record of
  *thinking*; neither means two people spoke, and counting them would let a rep's own to-do list score
  as a relationship. When the two paths disagreed, a captured note became an interaction while an
  identical hand-logged one did not — the same conversation scoring differently depending on how it
  arrived.
- **An archived activity.** The fold joins `activity … AND a.archived_at IS NULL`, and the prune step
  removes a pair that has lost all its evidence.
- **A seat on a deal.** A stakeholder row is a statement about *who ought to be involved*. Only a
  recorded interaction is evidence of *contact* — which is exactly why a deal can carry five seats and
  still be single-threaded.
- **An unlinked note.** The hand-logged path is silent for an activity with no person link: that is a
  workspace-shared thought, not a conversation with anybody.

## Warmth is per-user, and `none` is not zero

Warmth on these surfaces is the **per-user relationship strength** (PO-F-3b) — the same recency ×
frequency × reciprocity arithmetic as the contact's workspace-wide score, over only the interactions
*that colleague* was in. The contract states the consequence plainly: the two **are not comparable by
addition and are never merged**. A contact can be warm to the company while the colleague beside them
has barely met them, and that gap *is* the answer to "who should make the introduction".

The vocabularies are deliberately different on screen too — the per-user bands are
`none / weak / moderate / strong`, the workspace-wide card's are `dormant / weak / warm / strong` — so
nobody is invited to compare two numbers that are not comparable.

**A `none` band carries no number at all.** Not a strength, not a 90-day count. *"We have never spoken"*
and *"we spoke and it went cold"* are different facts about an account, and a zero renders them
identically; the wire type makes `strength` optional and omits it, and the card omits the count to
match.

Ranking cannot be pushed into SQL — the score is a function of `(row, now)`, so the database would have
to reimplement the formula the `relstrength` leaf exists to keep single. `EdgesForPerson` returns
**last-contact** order; a caller promising "warmest first" over-fetches (100), calls `SortByStrength`,
and only then caps (10). Capping in SQL would cap by recency, and a one-line reply would evict the
colleague who has worked the account for a year.

Two gates ride every edge read, and both are load-bearing:

- **The person gate.** `auth.Require("person", read)` plus `auth.EnsureVisibleLive` — not
  `EnsureVisible`, which returns early for an unbounded caller without probing. Either gap would let a
  known id return a contact's colleagues and interaction counts after the record itself stopped being
  readable.
- **The live-member join.** `app_user.status = 'active' AND archived_at IS NULL`. Both halves matter:
  deactivation sets `status` and leaves `archived_at` NULL, so filtering on `archived_at` alone keeps
  offering a departed colleague as a route in. The surface exists to name someone who can *act*.

## Deal coverage and its risk rules

`CoverageFor` reads inside **one transaction at one instant** — a view whose stakeholder list and
engagement test came from different snapshots can report a deal as single-threaded while listing three
engaged contacts — then folds the gathered facts with a pure function against an injected clock.

**Engaged means a real two-way exchange**, not a seat on a list: both an inbound *and* an outbound
qualifying interaction inside a 90-day window (`deals.EngagementWindowDays`). A one-way broadcast
target is not engaged however many messages we sent them. It is the **same** `deals.EngagedStakeholders`
the deal-health composite reads — one definition, so two screens cannot disagree about the same deal,
which is precisely what a reconcilable flag requires. (The engagement test walks the deal's
stakeholders' linked activities directly; it does not read the interaction projection.)

Every rule is a **pipeline** rule: a coverage view whose deal is not `open` folds to no findings at
all. Telling a rep their delivered business is single-threaded is how a flag stops being read.

| Kind | Identifier | The rule, as coded |
|---|---|---|
| `single_threaded_theirs` | **REPORT-PARAM-1**, verbatim | Fewer than two engaged contacts (`reportThreadingFloor = 2`). Their side: the customer is represented by one person. |
| `single_threaded_ours` | **GRAPH-RISK-1** | One colleague holds ≥ `ourSideDominanceShare` (0.8) of at least `ourSideMinInteractions` (5) 90-day interactions. Genuinely new rather than a re-reading of REPORT-PARAM-1, which is why it carries its own id. |
| `coverage_gap` | — | Seats on the deal, but no *engaged* champion. Distinct from single-threading: three engaged contacts and no champion among them is a deal nobody inside is arguing for. |
| `champion_left` | — | The canonical `champion` seat has left the account. |
| `stakeholder_left` | — | Another seat has left. Two kinds rather than one because they are different sentences to a rep — collapsing them makes the milder case shout and the severe one whisper. |
| `going_cold` | **REPORT-PARAM-2** | No captured touch for `goingColdDays` (30) days, reporting the actual day count beside it. One threshold ships, not two: the 60-day view is the same finding filtered on `days_since_touch`, and a second kind would let a deal at 61 days appear on one surface and not the other. |

The minimum on GRAPH-RISK-1 matters as much as the share: without it, a deal where one person sent the
only two messages that have ever been exchanged would flag as concentrated, when it is simply new.

Two facts the gather deliberately refuses to guess at:

- **Departure needs evidence.** A person counts as departed only when *both* halves hold: an
  employment at this account with an end date that has **passed**, and no live employment there now. An
  **archived** employment is not evidence of leaving — archiving retracts a statement (somebody
  recorded the job by mistake) while an end date records a fact about the world. Announcing a
  resignation because a colleague fixed a data-entry error is the false alarm that teaches a rep to
  ignore the flag. "Still employed there" is spelled identically here and in the two places that ask
  the opposite question.
- **A zero `LastTouchAt` means "do not judge".** The difference between *we did not look* and *nobody
  has spoken* is the whole finding, and reading the first as the second would flag every deal in a
  fixture that never described one. On a real read the last touch coalesces to the deal's creation — a
  deal nothing has ever touched has been silent since the day somebody wrote it down.

## Every risk carries the ids behind it

A risk without evidence is an opinion, and a flag a human cannot drill into is a red dot nobody can act
on (REPORT-AC-3). Each finding carries `person_ids` and `user_ids` — the unengaged stakeholder, the
colleague carrying the thread — as **ids rather than names**, so the caller renders them under its own
row scope.

Only `going_cold` carries `days_since_touch`. Sending a zero on the others would read as "touched
today", which is the opposite of the truth on a departure finding that says nothing about recency at
all.

The server also owns the **wording**: `summary` is the rule's own explanation, so the same flag reads
identically on the deal card and in the assistant. The client re-sorts nothing and re-words nothing —
either would be a second implementation of the decay formula or the rule text, and the two would
disagree the moment either changed.

## Privacy — dropped in the transaction, not by a consumer

The graph structures exist precisely to hold a party who *never became a record* — that is what the
address arm of a participant row and a LinkedIn ghost both are. A person-keyed sweep alone leaves the
subject named, reachable and re-matchable. So every clause in `privacy/erasure_graph.go` reaches the
subject by **identifier** as well as by person id, inside the same Art. 17 transaction as the rest of
the cascade:

- **Participants** — delete rows whose only identity is the subject (a participant row must name
  somebody, so it cannot be blanked); null the subject's arms on rows that also name one of our users,
  because the colleague was in that conversation and that is not the subject's data to erase.
- **Ghosts** — delete on *suggestion-grade* evidence, not just a confirmed match. The asymmetry is
  deliberate: matching errs toward caution because a wrong link attaches a stranger to a customer
  record, while deletion errs the other way, because deleting one ghost too many costs a re-import of a
  file the colleague still has and keeping one too few leaves a named person's data behind after we
  certified it destroyed.
- **Edges** — `DELETE FROM graph_interaction_edge WHERE person_id = $1`, **here** rather than in the
  `cg:graph-edge` consumer.

That last one is the load-bearing lesson, and the ownership gate records it as the ratification for
`privacy` writing `search`'s table: *an Art. 17 obligation discharged by an event is one that fails
silently when the bus is behind.* It was in fact wrong — the consumer listened for a `person.erased`
event this path has never emitted, so **every erasure left its edges standing** while looking exactly
like a passing one. The projection holds who corresponded with the subject, how often and how recently.

Two neighbouring lifecycle rules follow the same shape:

- **Deactivation** deletes the departing member's imported LinkedIn network in the single deactivation
  transaction, atomic with session and passport revocation — deleted rather than tombstoned, because a
  tombstone still holds the names.
- **Retention** reuses the *one* fold rather than writing a second statement: the time-based sweep
  archives and erases under `retention.applied`, which the `cg:graph-edge` consumer handles by name.
  The alternative duplicated the arithmetic and, being written as a delete, left a surviving pair's
  counts stale whenever the activity was not its last evidence.

Both tables are `ENABLE` + `FORCE ROW LEVEL SECURITY` with the ordinary `app.workspace_id` tenant
policy, reached only through `database.WithWorkspaceTx` — see
[authorization.md](authorization.md) and [privacy-and-consent.md](privacy-and-consent.md).

## Honest limitations

- **Overlay mode has no graph.** The projection is folded from natively captured participants, which
  the incumbent mirror does not hold. Both cards render the honest unavailable state rather than a
  doomed fetch that would read as "nobody knows them" ([overlay-augmentation.md](overlay-augmentation.md)).
- **The 90-day counts are bounded-stale by contract** — up to 24h over-inclusive between reconciles.
  Stated in the migration rather than hidden; recency is exact and dominates the score.
- **Calendar attendees are not backfilled.** The historical pass recovers the mailbox owner and the
  linked counterparty; parsing attendees out of stored originals is its own slice.
- **`OrganizationLinkedInReach` is wired to nothing.** The per-colleague, per-account ghost count
  exists in `people` and is exercised by tests, but no HTTP surface, agent tool or screen reads it
  today. The shipped account-level answer is the member's own
  [`/me/linkedin-reach`](../how-to/import-your-linkedin-network.md).

## Rules of thumb

- **Recompute, never increment.** The bus is at-least-once and history is corrected backwards.
- **The projection holds no fact of its own.** Throwing it away and rebuilding is always safe, and is
  the corruption remedy.
- **The score is computed at read**, never stored — a decayed number is wrong the moment the clock moves.
- **Every role makes an edge, cc included.** The quality bar is reciprocity, not a role filter.
- **Anything that returns a record is a read**, and carries the person gate plus `EnsureVisibleLive`.
- **A departed colleague disappears from the answer via the live-member join**, not by rewriting rows.
- **An erasure obligation is discharged in its own transaction**, never by an event.

## Where the code lives

| | |
|---|---|
| Participant rows for captured mail (both arms, ingest transaction) | `internal/modules/capture/participant.go` |
| Participant rows for a hand-logged call/meeting | `internal/modules/activities/participantlog.go` |
| Address → person promotion at the link chokepoint | `internal/modules/people/participant.go` |
| Historical participant recovery (class 1 / 2a / 2b) | `internal/modules/activities/participantbackfill.go`, job in `internal/compose/participantbackfilljob.go` |
| The interaction projection: fold, prune, rebuild, reads | `internal/modules/search/graphedge.go` |
| The `cg:graph-edge` consumer + its invalidation set | `internal/modules/search/graphedgegen.go` |
| The score (recency × frequency × reciprocity, bands, the 90-day window) | `internal/shared/kernel/relstrength/` |
| Coverage gather (deal facts, departures) and the pure risk fold | `internal/compose/network/coveragefacts.go`, `risk.go` |
| The network/coverage HTTP surface | `internal/compose/network/handlers.go` |
| The one definition of an engaged stakeholder | `internal/modules/deals/engagement.go` |
| Agent-tool seams (`who_knows`, `account_coverage`, `intro_path_to`, `at_risk_relationships`) | `internal/compose/networkseams.go`, `introseams.go`; `internal/modules/agents/tools_network.go` |
| Erasure of participants, ghosts and edges (one transaction) | `internal/modules/privacy/erasure_graph.go`, `retention_graph.go` |
| Deactivation deleting a departing member's network | `internal/modules/identity/users.go` |
| Cross-store ratifications for every table above | `backend/tableownership_test.go` |
| The tables + RLS | `activity_participant` (`migrations/core/0157_*`), `graph_interaction_edge` (`0158_*`), `linkedin_connection` (`0159_*`), `linkedin_account` (`0160_*`) |
| The REST contract | `backend/api/crm.yaml` (`getPersonNetwork`, `getDealCoverage`) |
| The job contract (cadence, fan-out, batch sizes) | `backend/api/jobs.yaml` (`graph_edge_reconcile`, `participant_backfill`, `linkedin_rematch`) |
| The two cards | `frontend/src/screens/network.tsx` (rendered from `people.tsx` and `deals.tsx`) |

## Where to go next

- Importing a personal network as the weaker, clearly-labelled evidence tier beside this one:
  [how-to/import-your-linkedin-network.md](../how-to/import-your-linkedin-network.md).
- Where the participant rows come from — the connector seam, the one Sink, the three ingestion modes:
  [capture-connectors.md](capture-connectors.md).
- The write shape every base-table mutation commits through, and the outbox both consumers ride:
  [write-backbone.md](write-backbone.md).
- Why the erasure cascade writes tables it does not own, and how each write is ratified:
  [privacy-and-consent.md](privacy-and-consent.md).
- Where the cross-module edges above are injected: [composition-layer.md](composition-layer.md).
- What every module owns, including these tables: [reference/modules.md](../reference/modules.md).
