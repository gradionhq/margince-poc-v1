# Email ingestion (capture): findings + ratified design

Date: 2026-07-17
Build repo: margince-poc-v1 @ main (12f2546)
Spec repo: margince-foundation — spec tree is `specs/` (ADRs in `specs/adr/`, backlog at repo root)

Scope: what happens when a user connects a mailbox during onboarding — designed, built, and
decided. Founder decisions of 2026-07-17 recorded inline. No code changed yet.

Contract-first (P3): spec defects go upstream, never worked around here. Where the spec
contradicts itself or is silent, the founder decision below is the tiebreak and needs an
upstream amendment before build.

---

## 0. The headline

Most of the intended design is **already pinned in the spec and simply unbuilt**. Only one
element is a genuine amendment (spam), and two are spec *gaps to fill* rather than change
(the free-mail blocklist, the backfill window). The largest build defect — three
incompatible dedupe policies — is a violation of a spec that is already clear.

---

## 1. Ratified target design

### 1.1 Connect (onboarding step 4)

1. User picks provider and **an ingestion window of N months**.
2. **Scope preview before spend** — message-count estimate + AI cost estimate for N, shown
   *before* the job runs. This is binding, not polish: `capture.md:159` — "previewing scope
   before it spends"; UC-ADMIN-05:64 — backfill spend is "metered usage attributed to its
   task, **not invisible background spend**". The window picker IS the consent surface for
   the customer's BYOK bill (ADR-0020: the customer pays the provider directly).
3. Connect → probe → show honest stats.
4. Explain the model: background, incremental, roughly X.
5. Move on. Records land progressively.

### 1.2 Ingestion pipeline

```
provider (spam folder NOT synced — provider already filtered it, free)
  ↓
RC-2 exclusion rules            [BUILT: sink.go:110,178 — pre-write, zero rows, capture.skipped]
  ↓
deterministic gates             [PARTLY BUILT]
  ├─ Auto-Submitted / Precedence headers   [BUILT: mailmap.go:252,268]
  ├─ free-mail blocklist                   [NEW — see §3.1]
  └─ no-reply / one-way-no-reply
  ↓
capture-classify  → commitment | meeting | noise    [NEW]
  L-S (local/small) model, River-BATCHED, on survivors only
  ↓ noise → raw only, no person/company
  ↓
enrich (signature → fields)     [NEW] evidence-or-omit, field-level provenance
  ↓
people.Resolver                 [NEW — the ONE place, §2]
  ↓
person (+ org via domain) + relationship(employment) + activity + activity_link(person)
```

**Founder decision — spam (2026-07-17): deterministic gates, THEN classify survivors.**
Free gates drop obvious junk; `capture-classify` runs batched on what survives. Rationale:
lowest spend on the customer's own key over an N-month window, and the provider's spam
filtering is already paid for. Avoids the amendment entirely — `noise` is the pinned bucket.

Constraint that bites: `ai-operational-spec.md:405` — "capture-classify and enrich run as
**River-batched** jobs … so high-volume `L-S` calls are batched rather than one-per-event."
One-call-per-message is NOT the sanctioned shape. Sizing: `ai-operational-spec.md:46` puts
capture-classify at ~600 events/seat/mo, "highest volume", `L-S`, escalating `L-S → C-C`
on low confidence only.

**Founder decision — activity links (2026-07-17): person only; org via roll-up.**
Matches the spec's existing grain (`people-and-organizations.md:375`: org strength = "max
over the org's people's strengths"; `ACT-AC-1`'s example is person-and-*deal*, not
person-and-org). Halves `activity_link` rows.

⚠️ **Consequence to hold:** company history now derives entirely from the
`relationship(employment)` edge. If domain→org resolution is wrong, the company timeline is
wrong, with no independent link to fall back on. The employment edge becomes load-bearing.

### 1.3 Company from mail

Deterministic, NOT AI: domain → free-mail blocklist → PO-F-2 exact (`organization_domain`)
→ link, or create. `people-and-organizations.md:85-89`: "the domain lookup is the
employer-inference path, and a proposed employer link is shown with its evidence for the
user to accept or correct."

AI touches only *classification* and the logo, and both are **proposals with evidence**:
- fixed enum prospect/customer/agency/reseller/tech_vendor/platform/partner/competitor/other
- shown "with the evidence it was inferred from … never a bare label she cannot trace"
  (UC-E02-02:47)
- a later differing classification surfaces a 🟡 and **never** silently overwrites a human
  value (the A26 guard, UC-E02-02:53)
- logo from the already-scraped page, "zero new egress and no third-party logo API" (:48)

### 1.4 Relations — already spec'd AND built

`relationship` table, `kind='employment'`, `role`, `is_current_primary`, dates.
Many employments over time, at most one current primary (partial unique index, DB-enforced).
A job change ADDS an edge with dates — never overwrites a company field. Same edge table
carries deal stakeholders and org↔org partner kinds.
Built: `migrations/core/0007_relationship.up.sql:4`, `people/relationship.go:157`.

---

## 2. The ONE dedupe chokepoint

**Founder decision (2026-07-17): full chokepoint, all paths — HTTP, MCP/agents, capture,
promote, cold-start — and for BOTH people and organizations.**

This is not an amendment. The spec already mandates it and the code violates it:
- `people-and-organizations.md:91` — "Dedupe runs in two tiers, **on every create and every capture**."
- `import-export-migration.md:641` — "**one dedupe implementation, not two**"
- `people-and-organizations.md:107` — "there is **one merge in the system**"

### 2.1 Current state — 6 INSERT sites, 3 policies, no shared helper

| INSERT | Entry | RBAC at insert | Dedupe probe | Policy on hit |
|---|---|---|---|---|
| `people/person.go:104` | HTTP, MCP | ✅ | `ensurePersonEmailsUnclaimed` (`person_children.go:133`) | **409** |
| `people/promote.go:322` | lead promote | ❌ **none at insert** | inline `person_email` (`promote.go:290`) | **auto-merge** |
| `people/organization.go:85` | HTTP, MCP | ✅ | `ensureOrgDomainsUnclaimed` (`organization_domains.go:59`) | **409** |
| `people/company.go:246` | anchor | ✅ (at `:220`) | none — relies on `uq_organization_anchor` | index error |
| `people/coldstartprofile.go:145` | scrape accept | ❌ **none at insert** | inline domain probe (`:130`) | link existing |
| `capture/sink.go:393` | connector | ✅ | own probe (`sink.go:307`) | **stage 🟡** |

Three near-identical email SELECTs, three incompatible resolutions. This is exactly the
"fixed the call site, missed the sibling copy" failure the repo's own rule #1 names.

`PO-F-1`/`PO-F-2` (fuzzy tier): **not implemented anywhere.** All five probes are exact-match.
Nearest prior art is `signals/resolver.go:275` (tiered confidence: domain 0.95, name 0.85,
`low_confidence` outcome) — right shape, wrong module, cannot be imported.

### 2.2 Target shape

`people.Resolver` — `EnsurePerson` / `EnsureOrganization`, one policy parameter:

- **Tier 1 — exact, deterministic, no score.** `person_email.email = lower(candidate)` /
  `organization_domain.domain`. → `EXACT_COLLISION`.
  Resolution by policy: `BLOCK` (API 409 + existing id, PO-AC-16) | `MERGE_ONTO` (capture:
  land on existing person).
- **Tier 2 — fuzzy.** `confidence = DEDUPE_NAME_WEIGHT * jaro_winkler(full_name)
  + DEDUPE_ORGDOMAIN_WEIGHT * org_match`, where org_match = 1.0 same current-primary org /
  0.8 shared org via `organization_domain` / 0.5 free-text company normalize-equal / 0.0.
  JW p=0.1, max prefix 4, casefold+unaccent (PO-PARAM-JW-1/2).
  `>= 0.72` (DEDUPE_REVIEW_THRESHOLD) → `FUZZY_REVIEW` → **🟡 review queue, both records
  side by side**.
  Org fuzzy = name similarity only (no domain to anchor), legal suffixes stripped (PO-PARAM-1).
- **NO_MATCH → create.**

**Fuzzy NEVER auto-merges and never auto-declines.** Registry pin,
`people-and-organizations.md:255`: `| DEDUPE_FUZZY_AUTOMERGE | *(never)* | Fuzzy never
auto-merges; exact-key only. |` (This corrects the founder's initial "declines/approves if
duplicate" framing: exact resolves automatically, fuzzy always goes to a human.)

Structural backstop stays the DB index, not the service: `uq_person_email_dedupe` (PO-DDL,
:512) — "the structural anti-duplicate guarantee".

### 2.3 Architecture

Modules never import siblings, so capture reaches the resolver through a **consumer-declared
port injected at compose** — the pattern is already proven three times:
- `capture.MergeStager` (`sink.go:51`) injected at `compose/capture.go:37`
- `capture.ExclusionRules` (`sink.go:36`) injected at `compose/capture.go:40`
- `deals.CorrectionStager` (`compose/closedate.go:32`), `deals.FollowUpStager` (`compose/reconcile.go:37`)

Keep the seam leaf-pure as the existing ones do (`MergeProposal.TargetType/TargetID` is a
string+UUID pair, not a typed id — `sink.go:57-59`).

**MCP/agents come along for free**: that surface already routes through
`CreatePerson`/`CreateOrganization` via `people/provider.go:128`. No separate wiring.

No merge-candidate staging exists on the people side — reuse `approvals` through the port,
as capture already does.

### 2.4 Where the risk actually is

Not the SQL. Two of six INSERT sites have **no `auth.Require` at the insert**
(`promote.go:322`, `coldstartprofile.go:145`). Routing them through one gated chokepoint
**changes their effective RBAC surface**. Also: `promote`'s auto-merge and capture's
stage-merge are both **contract-visible behaviors** (§1.3, features/01 §6.2) — unifying is
choosing a policy parameter, not flattening to one behavior.

Realistic size: **days, not hours.**

---

## 3. Spec gaps to FILL (no amendment — nobody pinned these)

### 3.1 Free-mail blocklist — asserted, never homed

`people-and-organizations.md:88` delegates it: "The free-mail-domain blocklist that prevents
gmail-class domains becoming organizations is **the capture chapter's**."
`capture.md` never receives it — no pin, no CAP-PARAM row (registry runs 1–3: latency,
refresh, manual-entry rate), no AC. **Load-bearing**: without it, company-from-domain
creates an organization called `gmail.com` on day one.

**Founder decision (2026-07-17): pinned constant list + additions via config file. No admin
UI — config only.** Fits the existing config-driven bootstrap posture (A107/ADR-0061,
`margince.yaml` / `--config` / `MARGINCE_CONFIG`).
Person is still created; only the *company* derivation is suppressed.
Do NOT confuse with RC-2 exclusion rules — different mechanism, different purpose (those are
per-user, pre-ingest, and produce zero rows).

### 3.2 The backfill window — unpinned white space

No user-chosen window AND no system constant, anywhere. `capture_connection` (CAP-DDL-2) has
`sync_cursor` + `watch_expires_at`, **no window/since column**. `onboarding-and-coldstart.md`
has zero hits for backfill/window/month; ONBOARD-PARAM-3 pins "5 steps: Read · Confirm ·
Voice · Results · Connect" — no window step.

**Founder decision (2026-07-17): user-chosen N months at connect.** Contradicts nothing.
Needs: a CAP-DDL-2 column, an ONBOARD-PARAM-3 step, and — because it drives AI spend — the
`capture.md:159` scope-preview obligation (§1.1.2).

---

## 4. Spec defects for upstream reconciliation

### D1 — BLOCKER: capture.md contradicts itself on backfill

**Against** — `capture.md:95`: "Sync is incremental from provider push and delta feeds,
**never a full re-scan**." Reinforced at `:422` (CAP-WIRE-N-1).

**Requiring** — S-E02.4 (`capture.md:471`): "on **backfill completion** the workspace is
demonstrably non-empty"; and all of `UC-E02-01`: ":47 Backfill begins … counts climbing",
":53 Backfill completes … one-line summary", ":76 F2 Connector degraded **mid-backfill**".

**Founder decision (2026-07-17): bounded backfill on connect.** `:95` is amended to read as
the **steady-state** rule; connect performs a one-time bounded backfill over the user's
chosen window. `UC-E02-01` F2's resumable/honest-progress semantics must survive the
amendment.

Do NOT mistake Gmail's existing 50-message pull (`capture/gmail/gmail.go:202`) for a
head start — it is `ErrHistoryGone` cursor recovery, not a product backfill.
Also distinct: the ADR-0020 *retroactive derivation* over an already-captured corpus
(`capture.md:84,158`) is a different feature; don't conflate the two backfills.

### D2 — the onboarding contacts count violates UC-E02-01

`UC-E02-01:47`: "Every count and record shown corresponds to a row actually persisted at
that moment." S-E02.4: "never a fake-populated screen."
Built: the step-4 number is `len(distinct counterparties)` (`capture/imap/imap.go:285,312`)
— addresses *observed*. Mail persists no person or lead (D3), so it corresponds to zero rows.
Build defect against a clear spec. Fixed as a consequence of D3.

### D3 — mail capture never creates a person

`capture.md:139` + AC3.1 (`:489`: 5-email thread, one new sender → 1 person, 1 org).
Built: mail produces only `EntityActivity` (`capture/mailmap.go:141`).
The lead-only dedupe at `sink.go:308` has the right posture but is unreachable from mail.

Adjacent constraints already pinned, must be respected:
- untrusted-by-default, injection neutralized at capture time (`capture.md:167`, threat-model T2)
- connector-created records default to the **originating user's visibility scope, not
  workspace-global**, until a human promotes them
- a suspicious auto-created record **quarantines pending review** (threat-model D8) — note
  this is a review state, NOT an AI spam verdict
- auto-create sits inside CAP-PARAM-1 (60s p95 receipt→visible) and must not block render (AC3.4)
- `person.created`/`organization.created` carry source + captured-by (`capture.md:449`)

### D4 — UC-E02-02 E4: unconfident classification (pre-existing, tracked)

Story says unset/"other"; `data-model §4` says `classification text NOT NULL DEFAULT
'prospect'`. Flagged upstream, untested, unreconciled. Company-from-mail walks into it.

### D5 — build gaps needing no spec decision

| Gap | Detail |
|---|---|
| No backfill job exists | Gmail path is incremental-only (`compose/jobs.go`: `gmail_sync`, `gmail_watch_renew`). |
| Gmail has no UI entry point | Complete OAuth + History + Pub/Sub watch pipeline built and tested; `frontend/src/screens/onboarding.tsx` never calls `/connectors/gmail/connect`. Provider tabs (`:1386-1406`) set state read only for highlighting; `:1409-1411` renders literal `ob.s4.oauthSoon`; IMAP form renders unconditionally at `:1413`. |
| IMAP ingests synchronously in-request | Against `capture.md:118-126` (connectors hand to an async pipeline, never touch the DB) and CAP-PARAM-1's "never satisfied by blocking the inbound render". Gmail is already correct. |
| Microsoft Graph absent | A51 puts M365 + Outlook at V1 **parity**, Microsoft-first for regulated DACH — "Outlook is not a fast-follow." No Graph code; `compose/connectors.go:106` → 422. Largest divergence from A51. |
| Onboarding step 2 "Build voice" is theater | `onboarding.tsx:833` emails source `locked: true`; 18,000-word figure hardcoded; `build()` (`:962`) is `setTimeout(1100)`, no backend call. Same honesty problem as D2. |
| Live progress surface unpinned | "See the database build itself" has no contract op in `crm.yaml`. |

---

## 5. Correctly built — do not re-litigate

- **River fully wired** — 5 periodic workers (`compose/jobs.go`), leader election so replicas
  never double-sweep, own migration namespace per ADR-0017 (`platform/jobs/migrate.go`),
  `cmd/worker` drains bounded at 30s. The async substrate the spec asks for exists.
- **capture.Sink is the one audited writer** (`sink.go:29`) — connector principal required,
  single tx, raw payload append-once, audit + outbox only when the row is new.
- **RC-2 exclusion gate runs before any write** (`sink.go:110,178`) — `capture_skip`
  system_log + entity-less `capture.skipped` event, so `capture.md:103`'s "machine-verifiable,
  not asserted" actually holds.
- **Erasure suppression** — an erased address refuses re-capture (`sink.go:295`).
- **OAuth CSRF** — signed state + SameSite=Lax nonce, constant-time compare (`connectors.go:171`).
- **`activity_link` is already polymorphic** (`migrations/core/0008_activity.up.sql:59`) —
  person/organization/deal/lead, unique per (activity, endpoint), visibility derives from
  links (`platform/auth/rbac.go:299`). "Mails in both histories" is wiring, not schema.
- **`relationship`** employment edge — see §1.4.

---

## 6. Ticketing reality

- **Capture has ZERO tickets.** `backlog/README.md:160`: capture is "pin-complete and
  ticketable", but tickets "derive in a separate step **on the founder's schedule**".
- **`onboarding-and-coldstart` is NOT yet ticketable** — "ONBOARD-AC-14 resumable wizard
  state machine has no table and no contract op; the cold-start seeding surfaces are
  unpinned." The onboarding half of this work is blocked on doc hardening.
- Whole backlog: "complete and pinned but NOT scheduled for dispatch."

Ticketed neighbors that touch capture output: `people-and-organizations` (unchecked
`AC-contacts-1`/`AC-companies-1` capture-banner counts — that's D2; `PO-AC-10`
pending/proposed captured-contact state), `activities-and-timeline` (AT-T01..T05),
`data-hygiene` (the merge-candidate review queue — §2.2's 🟡 destination),
`signals-and-warm-room` (SW-T01..T08).

---

## 7. Recommended order

1. **Upstream first** — ratify D1 (amend `capture.md:95` to steady-state + pin the window as
   a CAP-PARAM), home the free-mail blocklist in capture (§3.1), pin the window column
   (CAP-DDL-2) and the onboarding step (ONBOARD-PARAM-3). Resolve D4 while in there.
2. **Derive capture tickets** (founder-scheduled). Connector half is unblocked; onboarding
   half still needs ONBOARD-AC-14.
3. **`people.Resolver` chokepoint** (§2) — independent of the mail work, fixes a live
   product defect, unblocks everything else. Re-gating `promote.go` + `coldstartprofile.go`
   is the risk to watch.
4. **D3 + D2 together** — mail→person/company through the resolver; the count becomes honest
   as a consequence.
5. **Bounded backfill job** + scope preview + progress surface (needs a contract op).
6. **D5 wiring** — Gmail UI, IMAP→River. No spec decision needed; can land anytime.
7. **Graph connector** — the A51 parity gap. Own epic.

---

## 7b. Found by building it (2026-07-17) — the upstream work-list

Branch `refactor/people-resolver-chokepoint` (commit `e5a4911`) implements the PO-F-1/PO-F-2
resolver: `people/dedupe.go` (both tiers, trigram-restricted candidate sets, deterministic
lowest-id tie-breaks), `people/namesim.go` (Jaro-Winkler pinned to PO-PARAM-JW-1/2 +
PO-PARAM-1 suffix strip). The GIN trigram indexes the formula's candidate bound needs already
exist — 0052/0077's `idx_person_name_trgm` / `idx_org_name_trgm` over
`f_fold_apostrophes(lower(name))`; the resolver's candidate queries use that same expression so
the planner matches them. No new migration. All three of PO-F-1's worked examples reproduce exactly:
`name_sim(Jon Doe, John Doe) = 0.9667`, `confidence = 0.982` → 🟡, `0.532` → create.

**Update 2026-07-17 (later the same day): DH-GAP-1 is CLOSED upstream** — foundation commit
`6466432` pins `dedupe_candidate` as DH-DDL-1 (ADR-0062/A108: a dedicated table, not an
approval-inbox specialization), removes PO-F-1's unreachable `org_match = 0.5` tier
(PO-N-DEDUPE-1), and clarifies capture.md's "withheld" (the merge is withheld, not the
record). The resolver ships ahead of its wiring by founder decision; the wiring (§7b.4)
and the backlog `docs_commit` re-derive are the follow-ups.

### 7b.1 — RESOLVED upstream: DH-GAP-1 had no storage, so the fuzzy tier had no destination

`specs/subsystems/data-hygiene.md:244` names this itself:
> PO-F-1/PO-F-2's fuzzy output is defined as "route to a 🟡 review queue", but **no table in
> the corpus schema stores a queue item** … A candidate-pair table must be added
> **contract-first**, owned by this chapter when it lands, carrying at least: the pair
> (entity type + both record ids), the confidence, the per-field match evidence (AC-dedupe-2/3),
> disposition state (open / merged / not-a-duplicate), and the not-a-duplicate suppression
> that keeps a dismissed pair out of future sweeps (AC-dedupe-7), under the shared
> tenancy/provenance/audit conventions (DM-CONV-*).
> **Whether it is a new table or a specialization of the approval-inbox item store is the
> ticket's decision**; the queue semantics pinned in this chapter bind either shape.

That last sentence is load-bearing: reusing the approvals inbox (which capture's
`MergeStager` → `merge_records` already does for exact lead collisions) is a **sanctioned
candidate shape**, not a workaround. The decision is open.

Blocking because: the owning chapter is `status: planned` with no module in code, and the
table must be added contract-first (P3). Building it from the build repo would both violate
P3 and squat on another chapter's ownership.

**Founder decision (2026-07-17): stop and close DH-GAP-1 upstream first**, then build the
whole chokepoint — both tiers — in one pass against a settled spec.

### 7b.2 — resolved: FUZZY_REVIEW creates the record, it does not withhold it

The two chapters read opposite ways in isolation — PO-F-1's queue shows "both records side
by side" (both exist), capture.md:139 says a near-match is "**withheld** as a 🟡 merge
candidate" (sounds like no record). `data-hygiene.md:29` settles it: capture and the importer
"surface fuzzy candidates into the review queue — **they feed it, they never merge**", and
the item carries "the pair (entity type + **both record ids**)".

So: **create + queue a candidate pair.** "Withheld" scopes to the *merge*, not the record.
Worth a clarifying word in capture.md:139 — it is the sentence that misleads.

### 7b.3 — DEFECT: PO-F-1's `org_match = 0.5` tier is unreachable by construction

The formula reads "0.5 if free-text company strings normalize-equal". But `person` has **no
free-text company column** (`migrations/core/0004_people.up.sql:7-33`). The only
`company_name` free-text column in the schema is on `lead`
(`migrations/core/0009_leads.up.sql:12`) — and PO-F-1 explicitly excludes leads: "Leads never
appear (ADR-0008 segregation)".

So the 0.5 branch can never fire for a person. Implemented as `{1.0, 0.8, 0.0}` rather than
inventing a column. Upstream must either drop the 0.5 tier from PO-F-1, or pin where a
person's free-text employer lives — noting that a `person.company_text` column would sit
awkwardly beside the "employment is an edge, never a company field" pin
(`people-and-organizations.md:112-116`), which argues for dropping it.

⚠️ Weights: PO-F-1 states "weights sum to 1.0 so `confidence ∈ [0,1]`" — that still holds
(0.55 + 0.45), since the 0.5 was a value of `org_match`, not a weight. No renormalization
needed. Gated by `TestDedupeWeightsSumToOne`.

### 7b.4 — the true creation-path inventory (the survey undercounted)

Eight paths, not six. Two more found while reading:
- `people/person.go:453` `EnsurePersonByEmail` — a **fourth** exact-dedupe spelling (joins
  `person_email`, `ORDER BY created_at LIMIT 1`, with its own race-recovery on
  `DuplicateEmailError`).
- `activities/scheduling_public.go:177` — the public booking surface creates people through
  a port onto that same method.

So the live inventory is: `CreatePerson` (HTTP+MCP), `EnsurePersonByEmail` (public booking),
`promote.go:322`, `CreateOrganization` (HTTP+MCP), `company.go:246` (anchor),
`coldstartprofile.go:145` (cold-start), `capture/sink.go:393` (lead) — with four exact-match
spellings resolving three different ways (409 / auto-merge / stage-🟡).

### 7b.5 — upstream order for the spec session

1. **DH-GAP-1** — pin the candidate-pair table contract-first (new table vs approval-inbox
   specialization is the open call). This unblocks the fuzzy tier and the whole chokepoint.
2. **PO-F-1 `org_match` 0.5** — drop it, or pin where a person's free-text employer lives.
3. **capture.md:139 "withheld"** — clarify that the record is created and the merge withheld.
4. **D1** (§4) — amend `capture.md:95` to steady-state + pin the backfill window.
5. **§3.1** — home the free-mail blocklist in capture (it is asserted but ownerless).
6. **D4 / UC-E02-02 E4** — unconfident classification: "other" vs `NOT NULL DEFAULT 'prospect'`.
7. **`capture-classify`** — no §2.x prompt skeleton, no §3.2 threshold row. Routed, unspecified.

## 8. Open questions for upstream

- What are the window options (3/6/12 months?), and is there a cap?
- Does the bounded backfill reuse `gmail_sync` with a start cursor, or a distinct job kind
  with its own progress/resumability? `UC-E02-01` F2 implies the latter.
- Which contract op surfaces backfill progress + the live "database building itself" view?
  Not in `crm.yaml`.
- Does the window apply to IMAP at parity, or Gmail/Graph only?
- `capture-classify` has **no §2.x prompt skeleton** (skeletons run §2.1–§2.7) and **no row
  in the §3.2 threshold table** (`ai-operational-spec.md:289-304`). It is routed but not
  specified — that seam has to be written before build.
- Does the scope preview need a hard spend cap / abort, or is the estimate + metering enough?
