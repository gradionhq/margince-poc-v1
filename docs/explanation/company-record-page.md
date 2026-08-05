# The company record page — one gated read, per-viewer prose, honest omissions

The account page a rep opens on a company: who works there, what is open, what
moved, what to do next, and a written brief over all of it. It is the largest
composite surface in the product and it is assembled almost entirely in
`internal/compose` — thirteen gated section reads inside one transaction, plus
two cross-module orchestration groups that own view state of their own.

> **Name collision, worth clearing up once.**
> [company-context.md](company-context.md) is about the **installation's own**
> company — the singleton organization profile born in onboarding. This page is
> about the company **record** page: any customer, prospect or partner
> organization in the CRM. They share the word "company" and almost nothing
> else.

## The shape at a glance

Six endpoints serve one screen. Which one owns which part:

```text
                    the company record screen (organizations.tsx)
        ┌──────────────────────────────────────────────────────────┐
        │  header · state strip · health          suggestions card │
        │  contacts · deals · timeline            connections card │
        │  "since your last visit"                account brief    │
        └──────────────────────────────────────────────────────────┘
             ▲                    ▲                    ▲
 GET /organizations/{id}/360   GET …/graph        GET|POST …/brief
   ONE tx, gated per section,    one-hop            per-viewer, cached on
   a refused section is          node/edge set,     its inputs; POST …/ask
   NAMED in sections_omitted     per-group grants   reuses the same machinery
             │
 POST …/view-ack             advance the visit baseline (explicit, human-only)
 POST …/suggestions/dismiss  per-user, "not this, not now"
 GET  …/logo                 resolved from the site read; monogram floor
```

## One gated read

`GET /organizations/{id}/360` serves the whole page. Everything below is one
composite read assembled inside a single `database.WithWorkspaceTx`, and the
response carries the `as_of` stamp of that read. The isolation level is Read
Committed — the platform's posture — so a concurrent commit can land between two
sections; the stamp is what keeps that honest rather than hidden. No section
opens a second transaction, which is why every module store the assembly calls
exposes a transaction-taking variant of its read.

**Authorization is per section.** Reading the organization is mandatory, and its
refusal is the whole read's refusal (403/404 as usual). Every other section
needs its own object grant, and a section refused with
`apperrors.ErrPermissionDenied` is **omitted from the payload and named in
`sections_omitted`** — never returned as an empty array. That distinction is the
whole honesty mechanism of the page:

> "you may not see this" and "there is none" are different answers, and a UI
> that conflates them lies to the rep.

Empty arrays would be indistinguishable from an account with no contacts, no
deals and no history — and would be *believed*. The named-omission vocabulary
lets every card on the screen say **"hidden from you"** instead of drawing a
blank list. The thirteen section names are spelled once
(`org360/assemble.go`) and are simultaneously the contract's
`sections_omitted` enum and the keys the assembly reasons about, so a rename
cannot leave the two halves disagreeing:

`people` · `strength` · `deals` · `activities` · `last_touch` · `state_strip` ·
`health` · `next_steps` · `tags` · `list_memberships` · `pending_approvals` ·
`since_last_visit` · `suggestions`

They run in a fixed order, so two reads of the same account produce the same
`sections_omitted` list. Any error that is **not** a permission refusal fails
the whole read: a section that broke for a real reason must never be reported as
one the caller may not see.

Two further rules keep the read from lying by construction:

- **Nested collections are summaries, not paging surfaces** — and they are not
  all the same shape. The *paged* summaries (people, deals, activities, pending
  approvals, next steps) carry at most 25 rows with `page.has_more`, and
  `page.next_cursor` is always null: page two comes from the endpoint that owns
  that collection (`GET /activities`, `GET /deals`, `GET /relationships`,
  `GET /approvals`), each with its own cursor vocabulary. Tags and list
  memberships are **plain arrays** with no page metadata at all, and suggestions
  report what they left out through `suggestions_dropped` rather than a page
  object.
- **Every section prunes to the caller's row scope** with the same
  `platform/auth` predicates the module lists use, so a section can never
  out-see the dedicated endpoint it summarizes. Where a section reports linked
  ids, those ids carry *their own* scope — a task reachable through a visible
  contact must not hand back the id of a deal the caller may not read.

An overlay-mode workspace is refused outright with
`422 unsupported_in_overlay_mode`: the incumbent mirror holds records, not our
relationship edges, tags, approvals or visit marks, so there is no honest 360 to
assemble from it. A mode-resolution *failure* refuses too — serving native data
because the lookup broke is exactly the silent fallback the overlay module
exists to prevent. See [overlay-augmentation.md](overlay-augmentation.md).

## The account brief

`GET /organizations/{id}/brief` (and `POST` for the explicit refresh behind
"outdated — refresh") is a short written brief over the same records:
what this account is, where it stands, and what changed. It lives in
`internal/compose/orgbrief` and owns one table, `org_brief`.

**Assembled AS the caller.** The brief's input comes from running the 360
itself, as the requesting principal, inside the normal gates — the `Assembler`
seam is injected rather than imported, so the package composes one seam instead
of re-deriving the gated reads itself. A brief can therefore only describe records
that caller could open themselves, and `sections_omitted` rides into the input
so the writer is told to stay silent about those subjects rather than inferring
around the gap.

**Why not one shared brief per account?** It has no correct version. A shared
brief written from the union of everyone's visibility would leak scoped deals
and activities to a restricted reader; one written from the intersection would
degrade to the lowest common scope and tell the account owner *less* than the
page already shows them. Per-viewer is not caution, it is the only shape that is
both safe and useful — so the cache is keyed `(workspace_id, user_id,
organization_id)`.

**Cached on the inputs, not on the record.** The key is a SHA-256 over the
prompt version, the routing version, and the JSON-encoded assembled input
(`orgbrief/input.go`). Facts, deals, activities and grants all move without
touching the `organization` row, so a key derived from that row's version would
serve a brief describing a pipeline the account no longer has — indefinitely.
Folding the routing version in means re-pointing the model lane rewrites briefs
rather than leaving text attributed to a model that no longer writes it.

A cached brief whose fingerprint no longer matches is **rewritten before the
request answers**, so a brief that arrives is current. The faster alternative —
serve the stale one and refresh behind the request — trades that guarantee away
and needs a regeneration that outlives the request; it is not what this does and
nothing in the contract claims it.

**It degrades rather than fails.** With no model lane configured, or the
workspace's AI budget exhausted, or a reply the validator refuses, the brief
falls back to a deterministic structured summary over the same inputs
(`orgbrief/deterministic.go`) — identity, pipeline, each stalled deal on its own
line, the last touch, open tasks, then what the company *is* from its curated
profile fields. Every deterministic sentence cites the record it came from
exactly as the model path does, so the card renders and behaves identically
regardless of which path wrote it. `generated_by` names which one it was, because a reader
deciding how much to trust a sentence needs to know.

The prompt treats every activity subject and body as **untrusted quoted data**
behind a nonce fence — quoted between one-time random delimiters, so text inside
it cannot pose as part of the prompt; the same discipline the site-read prompts
use. This text
arrived from outside the workspace and must never be read as instruction. The
model runtime behind it is [ai-runtime.md](ai-runtime.md).

Both the brief and Ask are **human-only** (`x-agent-access: human-only`,
`security: [{ cookieAuth: [] }]`, and `auth.RequireHuman` at the service): a
brief is a reading aid for a person, and an agent reading records through a
passport has the records themselves.

## Ask

`POST /organizations/{id}/ask` answers **one of three prepared questions** about
the account — `whats_open`, `meeting_prep`, `whats_changed`. The question is
*chosen, not typed*, and that is the design rather than a stopgap: each prepared
question names the slice of the account its answer may be written from, which is
what lets every sentence carry a citation the reader can open. A free-text box
would need retrieval that can prove what it did **not** find, and a box that
quietly answered from a subset would read exactly like one that searched
everything.

An unknown question is a 422, never a default — silently answering a different
question than the one asked is indistinguishable from answering the asked one
badly.

Ask reuses the brief's machinery entirely: the same per-viewer input, the same
nonce fence, the same grounding filter, the same deterministic floor. The
difference between the three answers is one instruction string each. The shared
system prompt is strict about grounding — state only what the summary states;
never infer a cause, mood, intent or next step it does not contain; cite the ids
the summary gave; ids go in `evidence` and never in a sentence's text; if the
summary does not answer the question, return an empty array rather than a
sentence that talks around it; and say nothing at all about anything named in
`sections_omitted`. Nothing is cached: a question is asked and read once. The
company profile is deliberately withheld from Ask — those are approved prose
statements, and none of the three questions is about them.

## Suggestions

The `suggestions` section is what the account looks like it needs, computed from
its own records. **There is no model in this path.** Four rules, derived from the
contract enum rather than re-spelled:

| Kind | The rule |
|---|---|
| `no_reply` | an outbound message on a thread nobody answered (7-day window) |
| `stalled_deal` | an open deal idle past the 60-day stall window |
| `no_next_step` | an active account with no open task on it |
| `lifecycle_conflict` | a standing `contract_ended` signal while the record still reads as a live customer or an open opportunity |

Every rule is a comparison a rep could make themselves, and **each suggestion
carries the rule in the words they read** (`reason` — "the rule that fired, in
the words the rep reads. Never a score."), plus the `evidence` records it fired
on and, where the server can name one, an `action` that opens a governed surface
prefilled from that evidence (`draft_reply`, `open_deal`, `add_task`). A rule
that cannot name an action carries `null` and the card advises without offering
a button — a control that does nothing teaches the reader to stop pressing them.
A model could phrase these more warmly; it could not make them **checkable**, and
checkable is what lets a rep disagree with the *reason* rather than with a
verdict they cannot inspect.

Nothing is staged and nothing is sent. Each rule runs under the same row-scope
predicates as the section it concerns and only when the caller holds that
section's grant, so a suggestion can only point at records they can open, and a
missing grant produces silence rather than advice inferred from the gap. The
card offers at most three, and the remainder is reported in
`suggestions_dropped` — a silent cap reads as "that is everything". The cap is
applied server-side so the dropped count and the rows shown can never describe
different lists.

**Dismissals are per user.** `POST /organizations/{id}/suggestions/dismiss`
takes the suggestion's `fingerprint` — a hash over the kind, subject and records
it fired on, not over the kind alone. So advice stays gone *while the situation
holds* and **re-arms by itself when the evidence changes**, because the situation
is then genuinely a new one. A row is written only for a fingerprint the rules
currently produce for this account and this caller, which is what bounds the
table: one row per suggestion a human actually clicked. The two obvious
alternatives both fail — accepting any well-formed fingerprint makes the
endpoint an authenticated write sink, and capping the stored count silently
deletes the earliest judgments so a rep working through a long list has
dismissed advice come back.

## The state strip and health

Both replaced a single 0-100 relationship score the header used to lead with.
That number was a MAX over the account's contacts, so one talkative contact
spoke for the whole account, and nobody could scale it.

**The state strip** is the three readings the overview leads with. The *account*
half (lifecycle, relationship types) needs no grant beyond the organization the
caller already read. *Engagement* (last inbound, last outbound, a derived state)
rides the timeline grant; *commercial* (open count, stalled count) rides the deal
grant; the *signal* slot carries the worst thing standing open. Each is **null
when refused rather than zero** — "no open deals" and "you may not see the deals"
are different facts, and only one of them is about the account. Null on the
signal slot deliberately covers both "nothing is wrong" and "you may not read
signals": a strip that reassured someone who cannot look would be answering a
question it has no standing to answer.

Last-inbound and last-outbound are two timestamps rather than one "last touch"
because **which side wrote last is the question** — an account we mailed a
fortnight ago with no reply and one that wrote to us this morning have the same
last-touch date and opposite meanings. Both walk the same three links the
timeline walks, so the header can never disagree with the list beneath it, and
both carry the caller's activity row scope, so a rep sees the last message *they*
may read.

**Health** decomposes the relationship into parts a reader can act on: days
since last inbound, active contacts (contacts who have actually interacted —
a roster of ten who never replied is not ten ways in), reply balance over the
90-day window, `single_threaded` (one contact carrying the whole relationship is
the one shape a rep can fix before it costs them the account, so it is named
rather than scored), and open commitments. Same rule again: every part is null
when it cannot be computed, because zero is a claim about the account.

The signals *card* is separate — it reads `GET /signals` itself with
`status=open`, so it owns its own loading/unavailable/empty states rather than
inheriting the 360's.

## The visit baseline

`since_last_visit` is what changed on the account since **this caller** last
acknowledged seeing it: new activities, deal stage moves, pending proposals.
Each is null when the corresponding grant is absent — "not counted" stays
distinct from "counted as zero".

**The baseline moves forward only through an explicit operation**,
`POST /organizations/{id}/view-ack`. A GET that advanced it as a side effect
would destroy the very answer the caller opened the page to read, and would make
a prefetch indistinguishable from a visit. The upsert is monotonic —
`GREATEST(stored, now)` — so a slow tab's late-arriving ack can never rewind a
newer one; two tabs on the same account converge on the later visit instead of
racing the baseline backwards.

It is **human-only, and that gate is load-bearing rather than defence in depth**.
An agent principal carries the granting human's id as its `UserID` (that is how
row scope works for passports), so "resolve the acting user" would happily write
a baseline marking an account as *seen* by a human who never opened it —
consuming their unread marker on their behalf.

**The client dwell-gates it.** `useAcknowledgeOrganizationView` waits
`VIEW_ACK_DWELL_MS` — 5 seconds — with the account open before firing, and
leaving cancels the timer: opening a record and bouncing straight back out is
not reading it, and an ack from that would mark unread activity as seen. Only an
*assembled* 360 counts as a visit (in overlay mode there is no baseline to
advance). Success deliberately does **not** invalidate the 360 query: the "new
since your last visit" line describes the visit in progress, and refetching it
out from under the reader would erase the thing they opened the page to see.
When in doubt the baseline stays put — showing an item twice is a smaller wrong
than hiding one — so a failed ack is not even surfaced as an error.

## The logo

`Organization.logo_url` points at `GET /organizations/{id}/logo`. The mark is
resolved during a deep read from the page that read already fetched — its
`og:image` and its declared icons — so a face for every company costs no
third-party logo API and no new egress beyond the asset itself. Candidates are
tried in a fixed order (at most 8), sized between 32px and 300px on the long
edge, and rejected past an aspect ratio that says "banner" rather than "mark".

Everything stored is **re-encoded once, at store time**: the endpoint always
answers `image/png`, whatever the source format was, so no third-party markup is
ever served from this origin. The response is served `nosniff` with
`Content-Security-Policy: default-src 'none'; sandbox` and a short private
cache. A human's uploaded logo is never replaced by one a machine found — the
write takes a row lock and checks `field_provenance` under it, so the
precedence rule holds on the run where it matters and not merely on the quiet
ones.

**This requires object storage.** `MARGINCE_BLOBSTORE_ENDPOINT` and its
companions (see [../reference/configuration.md](../reference/configuration.md))
are what enable it. With no blob store configured the resolve lane returns
before it fetches anything, so no `logo_object_key` is ever written and the
endpoint answers 404 for every company; a deployment that *had* a store and lost
it gets `501 not_implemented` on the records that still name an object. **All
three answers render the same thing**, and that is the point: 404 also covers
"invisible to the caller" and "does not exist", so distinguishing them would
leak which organizations exist.

The floor is the **deterministic monogram** — `Avatar` derives initials from the
name and, when tinted, picks one of six tone pairs from a stable hash over the
name's code points, so the same record reads the same colour everywhere without
storing one. The monogram is not a fallback of last resort: it renders
*underneath* the image, so it is what shows while the logo loads, what is left if
the image fails, and what a company without a resolved logo simply has. A
company is never a broken image or an empty slot.

## The connections card

`GET /organizations/{id}/graph` is a **second** read serving the same page: the
account's one-hop neighbourhood as an explicit node/edge set the browser draws.
Separate from the 360 because a client that wants the profile does not always
want the graph, and because its unit of authorization is a **node group** rather
than a section — but the same posture: one transaction, one instant, per-group
grants, and a cap that reports what it left out. Groups are `contacts`, `deals`,
`intro_path` and `our_side`, named in `groups_omitted` when refused.

**One hop means one edge from the account.** A contact's other employers, a
deal's other accounts and a partner's own partners are not walked: a second hop
is a different read with a different cost, and a card that sometimes went two
hops would have no honest cap.

The display caps are what fits a picture a rep reads at a glance — 15 contacts,
10 deals, 10 related organizations, 10 colleagues — with a scan bound of 500 on
the one group whose display order the database cannot know (contacts are ordered
by a relationship strength computed after the read). Stakeholder contacts have
no cap of their own; they are bounded by the deals already selected.

`dropped_count` is why the read stays proportional to the account rather than to
the caps: it is counted over each group's **whole membership**, not over the rows
in hand. A truncated graph reporting no count reads as the whole neighbourhood,
and a count taken from a bounded read would understate it — so the caps bound
the rows returned and the per-contact work done on them (the part that grows
fast), while an exact remainder costs one index range scan per account. It stays
true past the 500-contact scan bound too.

Graph mechanics beyond the card — the strength model, the warm-intro resolver,
our-side edges — are [relationship-graph.md](relationship-graph.md).

## View state is not record fact

Three tables behind this page live in `compose` subpackages, and all three are
written **without an audit row and without an outbox event** — the saved-view
ruling, gated by `backend/tableownership_test.go`:

| Table | Owner | What it is |
|---|---|---|
| `user_record_view` | `internal/compose/org360` | the per-user visit baseline |
| `suggestion_dismissal` | `internal/compose/org360` | the rep's "not this, not now" |
| `org_brief` | `internal/compose/orgbrief` | the per-user brief cache |

This is the exception to [write-backbone.md](write-backbone.md)'s
non-negotiable domain-row + `audit_log` + `event_outbox` shape, and it is narrow
by construction. Each of the three is written on a *view* action (a visit, a
click, a regeneration), readable by nobody but its own user, actionable by no
consumer, and — in the brief's case — derived content regenerable at any time.
None of them is a fact about the record. An audit trail of who looked at what,
emitted onto the bus, would be surveillance rather than provenance; the ruling is
recorded inline against each entry so the gate is self-contained on a clean
checkout.

Both `compose` subpackages otherwise obey the composition-layer charter: they
coordinate modules (organization, person, relationship, deal, activity, tag,
list, approval, signal) and durably own no business entity. See
[composition-layer.md](composition-layer.md).

## Where the code lives

| | |
|---|---|
| The composite read + its section registry | `backend/internal/compose/org360/assemble.go` |
| Section vocabulary, caps, row-scope predicates, next steps | `backend/internal/compose/org360/sections.go` |
| Contacts / deals / tags + lists / signal facts | `backend/internal/compose/org360/{contacts,deals,collections,signalfacts}.go` |
| The state strip, last touch, health | `backend/internal/compose/org360/accountstate.go` |
| Suggestion rules and their reads | `backend/internal/compose/org360/{suggestions,suggestionreads}.go` |
| Dismissals (`suggestion_dismissal`) | `backend/internal/compose/org360/dismissal.go` |
| The visit baseline (`user_record_view`) | `backend/internal/compose/org360/viewbaseline.go` |
| The connections graph | `backend/internal/compose/org360/{graph,graphreads,graphplace,graphourside}.go` |
| HTTP transport + the overlay refusal | `backend/internal/compose/org360/handlers.go` |
| The brief: cache, input, fingerprint | `backend/internal/compose/orgbrief/{service,input}.go` |
| The brief: model path and its validator | `backend/internal/compose/orgbrief/write.go` |
| The deterministic floor | `backend/internal/compose/orgbrief/deterministic.go` |
| The prepared questions | `backend/internal/compose/orgbrief/ask.go` |
| Logo resolve (candidates, normalize, store) | `backend/internal/compose/{sitelogo,sitelogocandidates}.go` |
| Logo row, provenance precedence, `LogoURL` | `backend/internal/modules/people/organizationlogo.go` |
| Logo streaming handler | `backend/internal/modules/people/handlers_organization.go` |
| Contract | `backend/api/crm.yaml` — `/organizations/{id}/{360,graph,brief,ask,view-ack,suggestions/dismiss,logo}` |
| Table-ownership ruling | `backend/tableownership_test.go` |
| The screen | `frontend/src/screens/organizations.tsx` (`CompanyScreen`) |
| Data layer + right-rail cards | `frontend/src/screens/company360.tsx`, `company360.css` |
| The connections card | `frontend/src/screens/connections.tsx` |
| Header actions (new deal, tag, list) | `frontend/src/screens/companyactions.tsx` |

## Where to go next

[relationship-graph.md](relationship-graph.md) (the graph beneath
the connections card) · [company-context.md](company-context.md) (the
*installation's* own company — a different subject with a similar name) ·
[authorization.md](authorization.md) (the grants and row scopes every section
asks) · [composition-layer.md](composition-layer.md) (why this lives in compose)
· [ai-runtime.md](ai-runtime.md) (the model lane behind the brief and Ask) ·
[overlay-augmentation.md](overlay-augmentation.md) (why an overlay workspace is
refused) · [../reference/configuration.md](../reference/configuration.md) (object
storage for the logo).
