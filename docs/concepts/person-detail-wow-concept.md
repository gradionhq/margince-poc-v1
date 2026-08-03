# The Margince Person View — “The Relationship Room”

**Concept date:** 2026-08-03  
**Audience:** SDRs, account executives, founders, and relationship-led sellers  
**Status:** product concept, not an implementation commitment  
**Companion prototype:** [person-detail-wow.html](../prototypes/person-detail-wow.html)

## The promise

Opening a person must answer four questions before the rep has finished moving their eyes across the first screen:

1. **Why now?** What changed, and why is this moment worth attention?
2. **Who really knows them?** Which colleague has a credible, current route in?
3. **What matters?** What has this person or their company actually said, done, or prioritized?
4. **What should I do next?** One grounded move, ready to inspect, edit, and approve.

That makes the person view a **relationship room**, not a contact card. The CRM record is the substrate. The product is the next good conversation.

## Executive verdict on the current PoC

Margince already owns unusually strong raw material: capture-first activity, deterministic relationship strength, per-colleague warmth, typed employment and stakeholder edges, consent proof, provenance, context retrieval, history, and governed actions. Most CRMs would need several products to assemble this.

The current person screen does not yet compose those capabilities into a decision. It presents a vertical sequence of system modules:

- relationship strength;
- who knows them;
- consent;
- custom fields;
- related evidence;
- log activity;
- timeline;
- separate relationships and history tabs.

That is honest, but it makes the SDR perform the synthesis. The first screen is strongest when it behaves like a briefing: **moment → meaning → route → move → receipts**.

The Storybook fixture exposes the problem clearly. A legitimate but thin Anna Weber record opens with “Dormant / 0,” “Nobody here,” “no consent purposes,” “nothing related,” an empty log form, and an empty timeline. Every module is truthful; together they create an absence inventory. The page needs an authored empty/thin state that helps the rep improve the record without pretending to know more than it does.

## What Margince can already display

The following is implemented in the PoC or exposed by its current contract. “Display now” means a new composed read or layout can surface it without inventing a new domain fact.

| Capability | Available substrate | Current person view | Opportunity |
|---|---|---|---|
| Identity | name, title, emails, phones, address, social map, owner, source, reachability | name/title and row-level provenance only | Put contact methods, current employment, owner, reachable channels, and source directly in the identity rail. |
| Employment history | typed `relationship` edges with role, dates, current-primary marker | hidden in Relationships tab | Show current role in the header and a compact career ribbon; keep the full history in Connections. |
| Deal role | `deal_stakeholder` edges with champion/economic buyer/blocker/influencer/user roles | hidden in Relationships tab | Make buying role prominent and deal-specific, never infer it from job title. |
| Team-wide relationship | deterministic strength, factor breakdown, last interaction, in/out counts, contributing activity ids | prominent score card | Replace verdict-first scoring with a relationship pulse: cadence, reciprocity, last reply, decay, and evidence. Keep arithmetic one disclosure away. |
| Best internal route | `/people/{id}/network` ranks colleagues with per-user warmth, interaction count, and last touch | simple list | Turn the strongest colleague into an actionable route: why them, shared context, ask-for-intro draft. |
| Captured history | email, meeting, call, note, task, WhatsApp, Telegram; body, direction, links, provenance | timeline at page bottom | Make the latest meaningful exchange and unanswered thread visible above the fold; keep the full chronology below. |
| Evidence context | `/records/person/{id}/context` with named sections, record refs, summaries, evidence | generic Related evidence card | Curate into “what matters,” “open loops,” “shared context,” and “things to verify.” |
| Consent | per-purpose current state, lawful basis, DOI, append-only proof events | full consent module in Overview | Compress to an outbound guard near actions; expand the proof ledger on demand. |
| Provenance and changes | row provenance, record history, field diffs, actor/passport/evidence | header chip plus History tab | Put provenance on every consequential claim, not as a separate trust afterthought. |
| Custom fields | active person custom fields | separate card | Keep only decision-relevant values in the identity rail; “all fields” is secondary. |
| Governed action | compose/reply, tasks, booking, notes, archive, merge, share; confirmation tiers | actions are scattered | One “next move” action cluster with clear draft/approval state. |
| Assistant context | person `who_knows` and risk-aware context already reach agent retrieval | not visible as a person-page copilot | Add contextual questions and grounded outputs without building a second truth system. |
| Company-level signals | signals, warmth, intro-path APIs; company 360 already reads signals | absent on person view | Resolve the warm route to this known contact while keeping the signal company-level. |
| LinkedIn network substrate | imported connection ghosts, confirmed matches, reach and intro tools | settings/reach surfaces only | Surface a confirmed mutual route and shared professional context; never turn unmatched ghosts into contacts. |

## The gap between the spec and the PoC

The foundation spec already demands more than the current page renders:

- Contact methods and the current company belong in the header (`AC-person-1`).
- Strength must say team-wide, show its inputs and expose its literal arithmetic and contributing activities (`AC-person-2..4`).
- The contact 360 owns a warm-signal card with evidence and confirm-first actions (`AC-person-5/6`).
- Activity, deals, and notes are meant to be distinct panes (`AC-person-7..9`).
- Consent is per purpose with the proof trail (`AC-person-10`).
- Signature enrichment should show the exact captured quote and confidence (`AC-person-11`).
- Every field should reveal whether it was captured or typed, with source and date (`PO-AC-9`).
- Deep public research is already a V1-WOW concept: public sources only, every claim cited, correctable/dismissible, and staged before save (`S-E04.6`, `AC-profiler-*`).

The PoC has also moved ahead of older screen language in useful ways:

- Relationship strength now includes direction as a fourth factor.
- The network graph substrate records activity participants and per-colleague edges.
- Person context already includes `who_knows` for the assistant.
- Person, deal, and company surfaces increasingly use composed reads and three-zone record layouts.

The concept should reconcile these instead of reproducing old prototype layouts literally.

## Research synthesis: what the strongest products get right

This concept borrows patterns, not pixels.

### Attio: the record is a configurable work surface

Attio’s record pages combine a left detail rail, configurable tabs, activity, emails, files, notes, tasks, calls, relationship tabs, and immediate actions. Its communication intelligence exposes first/last/next interactions, strongest connection, and team-wide connection strength. The lesson is not “add more tabs”; it is **keep the record composable while pulling the most useful communication facts into the working surface**.

Sources: [Attio record pages](https://attio.com/help/reference/managing-your-data/records/create-and-view-records), [Attio enriched data and communication intelligence](https://attio.com/help/reference/managing-your-data/enriched-data).

### Affinity: the route is more valuable than the score

Affinity emphasizes collective-network relationship intelligence, relationship decay, and the warmest introduction path. Its best-path logic combines strength, recency, two-way engagement, and shared context. The decisive lesson for Margince is: **do not stop at “Fatima knows Anna.” Explain why Fatima can credibly introduce us and make the intro request the next action.**

Sources: [Affinity relationship intelligence](https://www.affinity.co/product/relationship-intelligence), [Affinity on warm-introduction quality](https://www.affinity.co/blog/which-relationships-drive-warm-introductions).

### Common Room: a person is the resolved journey behind scattered signals

Common Room’s Person360 combines identity and activity from product, community, social, and other channels. The useful idea is a unified person journey; the dangerous idea for Margince is covert named-individual behavior. Margince should keep its stronger boundary: company-level intent and consented first-party engagement may resolve to a known route, but no invisible per-person dossier is manufactured.

Source: [Common Room Person360](https://www.commonroom.io/blog/introducing-person360-connect-with-the-person-behind-the-signal/).

### Clay: signals need context and an operating play

Clay monitors job changes, promotions, hires, company news, fundraising, social mentions, website changes, product usage, and custom signals, then layers enrichment and action. The useful lesson is that a raw alert is not a feature. A signal becomes valuable only when it is **deduplicated, explained, routed, and paired with a recommended action and feedback loop**.

Sources: [Clay Signals](https://university.clay.com/docs/signals), [Clay custom signals](https://www.clay.com/signals), [Clay Account Research Agents](https://university.clay.com/docs/account-research-agents).

### LinkedIn Sales Navigator: surface “why this person” and “why now” together

Relationship Explorer combines buyer-role relevance, warm paths, recent job changes, recent activity, past-customer context, and a recommended next action. TeamLink makes colleagues’ paths explicit. The lesson is to pair **fit + timing + route**, not to make the rep inspect three separate intelligence panels.

Sources: [Relationship Explorer](https://www.linkedin.com/help/sales-navigator/answer/a1421128), [TeamLink](https://www.linkedin.com/help/sales-navigator/answer/a101027/teamlink-overview), [Sales Navigator account pages](https://www.linkedin.com/help/sales-navigator/answer/a110685?lang=en-US).

## The proposed information architecture

### 1. Persistent identity rail — “Who is this?”

The left rail is calm, factual, and mostly non-AI:

- avatar or deterministic monogram;
- name, current employment, location and timezone;
- verified/reachable contact methods with per-field provenance;
- owner;
- current buying role on active deals;
- relationship state in words;
- consent/outbound guard;
- lists, tags, and selected custom fields;
- career ribbon with former/current employments.

It should answer identity without scrolling. “View all fields” opens the complete editable record.

### 2. The Moment — “Why open this today?”

The first center card is not a generic AI summary. It is a deterministic/rules-first selection of the strongest current moment:

> **ScaleCommerce opened three procurement roles after announcing its DACH rollout. Anna replied to Fatima two days ago about vendor consolidation.**

It carries:

- a category: company change, relationship change, deal change, scheduled event, open loop;
- a freshness timestamp;
- confidence or explicit “observed fact” status;
- source chips and a “show evidence” disclosure;
- why this person is the route;
- one recommended next move;
- at most three subordinate actions: draft, schedule, follow-up;
- dismiss / not relevant feedback.

If there is no strong moment, the page says so and pivots to the next useful action: connect a mailbox, add the employer, ask the owner, or start public research.

### 3. The 30-second brief — “What should I know before I speak?”

Four compact, evidence-bearing lines:

- **Cares about:** a cited theme from their own messages or meetings.
- **Open loop:** an unanswered question, promised asset, or next step.
- **Business relevance:** their role in an active deal or account motion.
- **Talk about:** a grounded conversation angle, phrased as a question when confidence is not high.

No line is rendered without a receipt. A correction becomes human truth and the source is retained.

### 4. Relationship constellation — “How do we get there?”

An Obsidian-like **local graph**, never a decorative global hairball.

Default scope:

- the person at the center;
- current and former companies;
- the two strongest internal colleagues;
- active deals where the person has a stakeholder role;
- one warm-intro path;
- one or two important mutuals or related people where provenance permits it.

Graph rules:

- one hop by default; fixed two-hop only for an explicit route;
- node size represents relevance to this moment, not popularity;
- edge weight represents the named relation (employment, stakeholder role, captured interaction, confirmed connection);
- every edge is selectable and exposes the activities or records behind it;
- archived, inferred, and proposed edges look different;
- access-controlled omissions are named honestly;
- the keyboard-readable list is canonical; the diagram is an alternate spatial reading;
- “Direct / Route / Account” changes the question, not a generic zoom depth.

The graph does not require a graph database. The current relational graph substrate and fixed-hop queries are sufficient.

### 5. Signal stack — “What is changing around them?”

Three lanes keep the data boundary legible:

1. **Account signals:** company news, hiring, funding, website/offer changes, product/customer changes. These remain company-level and can nominate Anna as the best known route.
2. **Relationship signals:** reply after silence, reciprocity change, meeting booked, champion going cold, colleague relationship decay. These derive from captured history.
3. **Public professional changes:** a cited role change, promotion, public post, conference appearance, or authored article. These are public-source research facts, not hidden behavioral tracking.

Every signal has source, observed date, relevance reason, retention policy, and feedback. Margince must never show “Anna is researching procurement software” unless Anna explicitly and lawfully created that first-party signal. Anonymous or third-party intent stays at company level.

### 6. Unified chronology — “What happened, and what changed?”

The Timeline pane merges communication and meaningful record changes while preserving their difference:

- inbound/outbound activity;
- email body preview;
- calls, meetings and transcripts;
- tasks and commitments;
- signal arrival/dismissal;
- field corrections;
- employment and buying-role changes;
- approvals and agent actions.

Filters should answer sales questions: All, Messages, Meetings, Commitments, Changes. History remains available for audit-grade detail.

### 7. Action dock — “Do the next good thing”

The right rail contains one primary recommendation and a small queue:

- ask Fatima for a warm intro;
- draft a reply in the rep’s voice;
- send a booking link;
- prepare for the next meeting;
- create a follow-up;
- research publicly.

Every action states its state before the click:

- **Draft only** — no external effect;
- **Will ask for confirmation** — send or persist after approval;
- **Blocked** — consent or permission prevents it, with a reason;
- **Available** — safe internal note/task.

The action dock is where Margince’s governance becomes a product advantage instead of a compliance appendix.

## The “WOW” moments

### WOW 1 — The page opens on a reason, not a record

Within one second, the rep reads a grounded sentence connecting current account change, recent interaction, and the person’s role.

### WOW 2 — The graph answers a real question

Clicking “Best route” highlights **Lars → Fatima → Anna → ScaleCommerce**, explains that Fatima exchanged six two-way messages with Anna in the last 90 days, and offers a pre-drafted intro request grounded in their shared context.

### WOW 3 — “What do I say?” is already answered—with receipts

The conversation angle references Anna’s own stated vendor-consolidation concern and a public DACH expansion announcement. The source drawer shows the exact email excerpt and public page. Medium-confidence material is phrased as a question.

### WOW 4 — The page remembers what the human corrected

The rep changes “blocker” to “evaluator.” The label updates immediately, keeps the original evidence, displays “corrected by you,” and does not silently revert.

### WOW 5 — Thin data still feels intelligent

For a new contact, Margince says:

> **We know Anna’s work email and employer, but no teammate has a recorded two-way exchange yet.**

Then it offers three honest ways forward: inspect the employer, run public-source research, or ask the owner to confirm the relationship. Zeroes disappear unless they help make a decision.

## What should be added

### Contract and composed-read additions

1. **`GET /people/{id}/360`** — one authorization-aware read, analogous to company 360, returning identity, current employment, selected relationships, latest activities, relevant signals, consent summary, best route, open next steps, and omitted sections.
2. **Person moment projection** — a bounded, deterministic candidate set with `kind`, `headline`, `why_now`, `evidence[]`, `recommended_action`, and dismissal state. AI may word a grounded candidate; it does not invent the candidate.
3. **Public research profile resource** — the existing profiler concept needs a wire home for run, progress, staged claims, corrections, dismissals, and save.
4. **Person profile-field read** — expose the existing evidence sidecar for signature/site-derived title, phone, role, LinkedIn, and employer name.
5. **Next-step/commitment projection** — tasks, promised follow-ups, unanswered questions, and scheduled meetings in one bounded read.
6. **Per-edge receipts** — a graph edge response should name the activity/relationship ids that justify it and whether it is direct, inferred, proposed, or confirmed.
7. **Public professional event** — if added, define a narrow schema separate from warm-room behavioral signals: public source, observed-at, event type, confidence, retention, correction/dismissal, and no special-category inference.

### Data and capture additions

1. Complete multi-party email and calendar participant capture; today the participant graph is incomplete for group mail and attendees.
2. Add a person avatar/profile image with provenance and a monogram floor.
3. Persist commitments/action items extracted from mail and transcripts as proposals until accepted.
4. Preserve current and prior employments from confirmed public changes or capture evidence; never overwrite history.
5. Add explicit source recency/expiry so stale public claims can visibly age out.
6. Model a preferred reply channel as an explainable deal/cadence suggestion, not a permanent personality attribute.

### Experience additions

1. Keyboard-first command bar scoped to the person: ask, log, draft, research, schedule.
2. A focused evidence drawer shared by brief lines, signals, graph edges, and AI output.
3. “Since your last visit” on the person view, using the same monotonic visit-baseline pattern as company 360.
4. A meeting mode that collapses the page to the brief, attendees, open loops, timeline, and notes.
5. A handover mode that explains the relationship, active commitments, known sensitivities, and the best internal routes to a new owner.

## Feasibility map

| Horizon | Experience | Why it is feasible |
|---|---|---|
| **Now — composition** | identity rail, current employment, contact methods, relationship pulse, best colleague, latest exchange, consent guard, tabs, three-column layout | Existing person, relationship, activity, strength, network, consent, context, history and custom-field APIs. Mostly a composed read and frontend redesign. |
| **Next — synthesis** | 30-second brief, open loops, person moment, action dock, “since last visit,” evidence drawer | Existing retrieval, AI runtime, evidence model, approvals, tasks and view-ack pattern. Needs bounded projections and a person-360 endpoint. |
| **Next — local graph** | direct/route/account constellation with receipts | Existing `activity_participant`, `graph_interaction_edge`, typed relationships, person network, signal intro path and LinkedIn connection substrate. Fixed-hop SQL is enough. |
| **Later — governed research** | streamed public-source profiler, cited claims, conversation angles, correction/dismiss/save | Fully described by the foundation spec, but the web-search seam and wire resource remain pre-build gaps. |
| **Later — signal crawling** | job/promotions/public activity, company website/news/hiring changes, custom watches | The signals module and site-read pipeline exist, but provider/ToS, budgets, retention, dedupe and the person-vs-company boundary need explicit contracts. |
| **Later — durable commitments** | extracted promises, unanswered questions and next-step proposals | Activity bodies/transcripts exist; requires a commitment proposal/read model and correction lifecycle. |

## Non-negotiable product boundaries

- **No mystery intelligence.** Every consequential claim, signal, label, score, route, and suggestion has a receipt.
- **No global graph hairball.** The graph is local, question-scoped and bounded.
- **No covert named-person intent.** Company-level intent may choose a known contact as the route; it does not become a behavioral dossier about that contact.
- **No AI overwrite.** Machine output proposes; confirmed human truth wins unless explicitly changed.
- **No action ambiguity.** A rep knows whether a click drafts, persists, asks for confirmation, or sends.
- **No absence inventory.** Missing data produces one useful explanation and one good next step, not six empty cards.
- **No second truth system.** The brief, graph, assistant and timeline resolve back to the same records and activities.
- **No graph-store requirement.** Reconsider storage only if unbounded path-finding becomes a genuine product need.

## Suggested success measures

- A rep can state **why now, who knows them, what matters, and the next move** after five seconds on a seeded record.
- Opening the page to initiating a grounded action takes under 30 seconds.
- Every rendered brief line and graph edge can reveal its receipt in one action.
- A thin record offers no more than one primary remediation path and never fabricates a fact.
- A dismissed signal stays dismissed until materially new evidence arrives.
- A corrected claim/label is visibly human-set and does not silently revert.
- Before confirmation, outbound side effects and persisted AI proposals remain zero.
- At 200% zoom and on a 390 px viewport, identity, moment, evidence and the primary action remain reachable without horizontal page scrolling.

## Prototype notes

The companion prototype demonstrates the default “Brief” state with:

- a persistent identity rail;
- a grounded Why now moment;
- an interactive direct/route/account relationship constellation;
- a 30-second brief with evidence markers;
- a signal stack;
- a best-route action rail;
- Brief, Timeline, and Connections modes;
- evidence disclosures and action-state feedback;
- a responsive single-column mobile layout.

It is intentionally not wired to Margince APIs. The content is a coherent fictional scenario designed to make the information hierarchy and interactions judgeable.

## Foundation references reviewed

- `specs/subsystems/people-and-organizations.md`
- `specs/subsystems/context-graph.md`
- `specs/subsystems/signals-and-warm-room.md`
- `specs/subsystems/capture.md`
- `specs/subsystems/search-and-retrieval.md`
- `specs/subsystems/meetings-and-transcripts.md`
- `specs/subsystems/morning-brief.md`
- `specs/use-cases/UC-E04-02-deep-research-person-profile.md`
- `specs/use-cases/UC-E05-04-people-picture-warm-blocking.md`
- `specs/product/journeys.md`
- `specs/adr/ADR-0007-context-graph-is-v1-substrate.md`
- `specs/adr/ADR-0078-user-contact-interaction-edges.md`
