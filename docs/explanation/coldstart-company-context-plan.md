# Cold-start and company-context refresh plan

Status: implementation in progress. Phase 0 was ratified in
`margince-foundation` PR #1104; the implementation phases below land as
separate pull requests and merge only after their required quality gates pass.

## Outcome

Cold-start should leave Margince with a confirmed, durable understanding of the
company it serves, regardless of whether that understanding came from a website
or from manual entry. The same governed profile should then improve later AI
features through one bounded context service, rather than each feature querying
tables or assembling prompts independently.

The intended first-run experience is:

1. Choose **Read my website** or **Enter it myself**. Website ingestion is an
   optional accelerator, never a gate.
2. If a website is supplied, watch grounded findings arrive progressively while
   Margince reads the site. Every machine-read value retains its evidence and
   confidence; ungrounded values stay empty.
3. Review a concise company portrait, correct it, and answer only the few useful
   questions the site could not answer.
4. Confirm once. The confirmed profile becomes the installation's company
   context and the UI shows where it will help: drafting, search, qualification,
   briefs, and governed agents.

This plan does not make every extracted fact mandatory, inject the entire site
corpus into every model call, or let website content become model instructions.

## What exists today

There are two website-read paths with different capabilities:

| Path | Extraction | Persistence after confirmation | Current use |
|---|---|---|---|
| `POST /coldstart/preview` | A quick read constrained to 11 profile fields | `PUT /company` writes the confirmed values | Prefills the first onboarding form |
| `POST /organizations/{id}/deep-read` | Whole-site crawl; profile fields, company facts, offerings, signals, and published people | A `deepread` approval writes profile fields and facts; each person is a separate lead proposal | Enrichment from an existing organization 360 |

The onboarding preview itself persists nothing. When its prefilled form is saved,
`PUT /company` intentionally treats every submitted value as a human assertion:
`source=human`, `captured_by=human:*`, confidence `1`, and an empty evidence
snippet/source URL. The visible website evidence therefore does not survive the
current onboarding save. The separately staged `/coldstart` and deep-read accept
paths do preserve machine evidence, but the onboarding UI does not use them.

The stored data is split deliberately:

- `organization` and `organization_domain` hold canonical identity data such as
  display/legal name, industry, address, and primary domain.
- `organization_profile_field` holds one value per profile key, including ICP,
  value proposition, USP, buyer roles, buying intents, and history. It preserves
  evidence, confidence, source, capture actor, and capture time.
- `organization_fact` holds the richer multi-value deep-read taxonomy: founding
  year, employee range, phone, contact email, locations, services, products,
  certifications, partners, named customers, and technologies. It has the same
  provenance discipline and links back to `site_read`.
- `site_read` is the operational crawl dossier: progress, pages read/skipped,
  stop reason, and staged proposal identifiers.
- Published team members are not company-context rows. They are staged as thin
  `site_lead` proposals and become leads only after separate acceptance.

The plan began with a read-side reuse gap: profile fields were assembled only for
`GET /company`, facts had no production read consumer, and no bounded service
could supply governed company knowledge to later product calls. Phase 1 closed
that substrate gap with the typed, scoped `GET /company/context` read model.
Product-wide model-call injection remains Phase 3 work.

The contract mismatches recorded during planning now have explicit outcomes:

- **Resolved in Phase 0:** ADR-0065/A111 ratified the anchor organization,
  profile/fact persistence homes, and `/company` plus `/company/context` reads.
- **Scheduled for Phase 4:** the current React screen still combines Read and
  Confirm and exposes four top-level steps; the ratified target has five.
- **Resolved in Phase 0/1:** the universal manual minimum is display name,
  offer summary, and ICP. Legal identity fields are optional until a workflow
  with a real legal or invoicing need requires them.
- **Resolved in Phase 0:** progressive whole-site latency replaced the obsolete
  quick-read eight-second and five/ten-field assumptions.

## Recommended company model

Do not add a second denormalized “AI profile” that can drift from company data.
Treat the existing anchor organization, profile fields, and organization facts as
the governed source of truth, then expose one typed read model over them.

### 1. Canonical layers

Keep four distinct concepts:

1. **Identity** — canonical organization columns and primary domain.
2. **Business profile** — human-confirmable, mostly single-value statements in
   `organization_profile_field`.
3. **Evidence facts** — repeatable, source-grounded findings in
   `organization_fact`.
4. **Operational dossier** — crawl progress and coverage in `site_read`; never
   prompt context by itself.

The profile/fact tables should remain provenance-bearing source records. Human
edits outrank machine refreshes. A refresh may propose a change, but must not
silently replace a human-held value.

### 2. Add a typed `CompanyContext` read model

Add a read-only port owned by the people module and composed at the application
boundary. Its output should be a typed structure, not an unstructured text blob:

- identity: display name, industry, domain, operating locations;
- positioning: offer summary, ICP, value proposition, differentiators;
- sales: buyer roles, customer pains/jobs, buying triggers, common objections,
  sales motion;
- offer catalog: products and services;
- markets: served industries, company sizes, geographies, and languages;
- proof: named customers, quantified outcomes/case-study claims,
  certifications, and partnerships;
- capabilities: relevant technologies and declared delivery capabilities;
- administrative identity: legal name, registered address, register/VAT data;
- provenance metadata and a deterministic `context_version`/fingerprint.

The assembler reads the anchor organization plus the two evidence tables under
the normal workspace transaction and RLS boundary. It emits deterministic field
ordering and task-specific bounded views. If caching is added, the cache key must
include the workspace, anchor organization, requested view, and context version.

Do not put individual team-member contact details, raw site text, secrets, or the
full crawl dossier in this context. Agents can retrieve individual records through
the governed data-source/tool surface when a task actually needs them.

### 3. Evolve the field vocabulary by purpose

The goal is not “more fields”; it is enough context to improve named product
behaviors. Retain existing useful fields, but reorganize and extend them as
follows.

| Group | Fields | Why the product needs them |
|---|---|---|
| Core | display name, offer summary, ICP | Minimum semantic context for useful drafting, search, and agent behavior |
| Positioning | value proposition, differentiators/USP, customer pains/jobs, desired outcomes | Drafting, qualification explanations, summaries |
| Buying | buyer roles, buying triggers, common objections, sales motion | Deal coaching, reply/offer drafting, next-best action |
| Market | industries served, company-size bands, geographies, languages | Search vocabulary, lead qualification, localization |
| Offer | products, services, capabilities, technologies | Offer drafting, query expansion, record classification |
| Proof | named customers, case-study outcomes, certifications, partners | Evidence-grounded drafts and credibility cues |
| Firmographic | own industry, employee range, founding year, locations | Company identity and sensible defaults |
| Administrative | legal name, registered address, VAT/register, contact channels | Invoicing, jurisdiction, compliance, company settings |

Add only fields with at least one declared consumer. Prefer normalized repeatable
facts for lists and profile fields for single curated statements. Quantified proof
points should preserve the exact claim and source; the system must never turn a
named customer or certification into a stronger claim than the website made.

Competitors should not be inferred. Store them only when the company explicitly
states them or a human supplies them. Keep voice/tone out of this model; the voice
profile owns how a user writes, while company context owns what the business is.

## Manual path and required fields

Website ingestion must be completely optional. Manual onboarding must work with
AI routing disabled, no egress, and no website.

The recommended universal minimum is three fields:

1. **Company name** (`display_name`) — who Margince is working for.
2. **What do you sell?** (`offer_summary`) — one plain-language sentence.
3. **Who do you sell it to?** (`icp`) — one plain-language target-customer
   description.

These are the smallest fields that make downstream semantic context useful. Ask
for them in both paths: the website attempts to prefill them, and the user fills
anything the site could not ground.

Do not universally block onboarding on legal name, registered address, VAT or
commercial-register number. Those fields are jurisdiction- and legal-form-
dependent; requiring all of them excludes valid companies and confuses CRM setup
with invoicing/compliance setup. Collect them when the site grounds them, show
them in an optional “Legal and company details” group, and make them conditionally
required only when a later feature with a real legal need is enabled.

Industry, website, value proposition, differentiators, buying center, buying
triggers, history, locations, and proof are recommended but optional. Base
currency and timezone already come from installation configuration and should not
be duplicated in this form.

ADR-0065 ratifies `offer_summary` and `value_proposition` as distinct canonical
profile fields: offer summary answers “what is sold,” while value proposition
answers “what outcome makes it valuable.”

## Website-ingestion target flow

Unify onboarding and in-app enrichment on one crawl/extraction engine. They may
have different transports and confirmation screens, but must not maintain two
field taxonomies or two prompt implementations.

### Onboarding read API

Introduce a contract-first onboarding read dossier that can exist before an
anchor organization exists:

- start a read with a URL;
- return an operational read identifier;
- poll or stream phase, pages read, grounded profile fields/facts, omissions,
  warnings, and coverage;
- support cancel/retry and an honest manual fallback;
- create no organization/profile/fact domain rows before confirmation;
- on confirmation, write the anchor organization, selected profile fields, and
  selected facts in one audited transaction.

Do not force the current organization-bound deep-read route to create a hidden
anchor organization merely so it has a foreign key. Either generalize the
operational dossier contract for an unbound onboarding target or add a dedicated
onboarding draft owned by the same engine. The choice belongs in the upstream
schema/ownership decision.

Keep the following existing guarantees:

- SSRF protection, robots handling, same-site deterministic discovery, and hard
  page/byte/time bounds;
- evidence-or-omit and legal-entity abstention;
- secret stripping and sovereign zero-egress behavior;
- machine proposals never overwrite human-held values;
- published people remain separately accepted leads;
- a failed or partial crawl never blocks manual completion.

### Confirmation and refresh semantics

The confirmation request should carry explicit selected field/fact identifiers or
their bound hashes, so the server can detect a stale read and so “accept subset”
is real. Human edits become manual assertions; preserve the original machine
evidence as proposal history/reference rather than falsely claiming the edited
text was quoted from the site.

After onboarding, “Refresh from website” should use the same pipeline and present
three buckets: new facts, changed machine-held facts, and conflicts with
human-held facts. Only the first two may be bulk-accepted; human conflicts require
an explicit field decision.

## Context injection policy

“Use this everywhere” should mean every context-sensitive AI task declares its
policy, not that every HTTP or model call receives a large generic prompt.

### One injection boundary

Add a compose-layer `CompanyContextProvider` that converts the typed read model
into bounded, task-specific model context. Product features request named views;
they do not query profile/fact tables directly and do not concatenate their own
company prompts.

Every AI task must declare one of:

- `none` — company context would bias or contaminate the task;
- one or more named context scopes;
- a maximum token/character budget;
- whether absence degrades to no context or makes the feature unavailable.

A fitness test should derive the task catalog and fail when a new task has no
explicit context policy.

### Recommended task matrix

| Task/surface | Context scopes | Initial policy |
|---|---|---|
| Governed agent loop | compact identity, positioning, sales, offer | Always include the compact confirmed profile; retrieve detailed records through tools |
| Reply/email drafting | positioning, buyer roles, proof, languages; voice separately | Include when drafting on behalf of the company |
| Offer drafting | offer, capabilities, value proposition; keep deal evidence and rate card authoritative | Include; never let profile text invent a line or price |
| Natural-language search | offer and market vocabulary | Use for query expansion, never as a filter that hides valid records |
| Lead/deal qualification explanations | ICP, market, buying triggers | Include only after the scoring/qualification contract is ratified |
| Morning brief ranking | sales priorities/ICP only if the ranking policy explicitly names them | Defer; deterministic feature weights remain authoritative today |
| Voice-profile generation | business profile as knowledge; corpus as style evidence | Include, preserving the separation between what the company says and how the user writes |
| Capture classification | none initially | Classify the message in front of it; avoid company-profile confirmation bias |
| Signature enrichment | none | Evidence must come only from the signature block |
| Website extraction | none | Avoid circular extraction and prompt anchoring |
| Raw record embeddings | none | Preserve record meaning; context belongs at query/retrieval time |
| Auth, CRUD, approvals, calculations, health | none | These are deterministic/non-AI operations |

### Safety, freshness, and reproducibility

- Put confirmed human assertions and accepted web facts in a clearly delimited
  data block, never in the system-instruction frame. Website-derived text remains
  untrusted data even after acceptance.
- Include only allowlisted fields for the selected task view. Apply the existing
  secret stripper before egress.
- Bound context size, sort deterministically, deduplicate facts, and prefer
  human-confirmed values over machine values.
- Record the context fingerprint/version and selected scopes in the AI-call
  trace. Add the fingerprint to response-cache keys so an edited company profile
  cannot receive an answer cached against stale context.
- Missing context must be explicit in diagnostics and must never be replaced with
  guessed defaults.
- Measure per-task quality and token cost with context on/off before expanding a
  view. Context injection is useful only when it improves the named task.

## UI/UX refactor

The current company step is a long field wall that merges the design's Read and
Confirm moments. Refactor it back to the five-step product sequence:

**Read · Confirm · Voice · Results · Connect**

### Step 1 — Read

- Lead with “Help Margince understand your company,” then show two equal, clear
  choices: **Read my website** (recommended accelerator) and **Enter it myself**.
- The manual choice goes directly to an empty Confirm step; do not make the user
  manufacture a URL or dismiss an error state.
- For a website read, show live phases and real evidence as it arrives: page
  discovery, legal identity, offers, customers/markets, proof, and team. Replace a
  single blocking spinner with a calm progress narrative and skeleton cards.
- Surface page coverage and honest omissions without making internal crawl detail
  the hero. Keep a “What we read” disclosure for trust.
- Allow “Continue manually while this finishes” and an explicit cancel/retry.

### Step 2 — Confirm

- Start with a compact “company portrait,” not all database fields. Put the three
  required answers at the top and group the rest into Positioning, Market &
  buying, Offer & capabilities, Proof, and Legal details.
- Render repeatable facts as editable chips/cards, not comma-filled textareas.
- Show read-from-site versus typed-by-you provenance and expandable evidence at
  field level. Confidence is supporting information, not a score the user must
  interpret to continue.
- Highlight only missing high-value questions. The ideal state asks one to three
  targeted questions instead of exposing every optional empty field.
- Support group selection and field-level exclusion so confirmation really means
  accept-subset.

### Completion moment

After confirmation, show a short “Margince now understands…” reveal built from
real confirmed data: company/market summary, products/services count, buyer-role
count, proof-point count, and the product surfaces this context will improve. This
is the WOW moment: recognition plus visible utility, not decorative animation or
fabricated output.

Use the existing Ledger-Green tokens, design-system atoms, trust components,
wordmark, and motion language. Eliminate inline styles in the onboarding screen
as the refactor lands. Add responsive behavior, keyboard/focus flows,
`prefers-reduced-motion`, i18n coverage, and accessible announcements for
progressively arriving findings.

## Delivery plan

### Phase 0 — Contract and product decisions (completed)

1. Reconcile the richer implementation into `margince-foundation`: persistence
   ownership, company-context read model, field vocabulary/cardinality, manual
   minimum, onboarding read dossier, accept-subset, refresh conflicts, and the
   five-step flow.
2. Ratify the latency metrics for a progressive whole-site read. Recommended
   starting targets: immediate phase feedback, first grounded result measured
   separately from full completion, and a bounded full-read p95 based on the
   current 12–25 second observed range rather than the obsolete eight-second
   whole-flow assumption.
3. Decide whether the operational onboarding read generalizes `site_read` or has
   a dedicated draft table. Do not implement a hidden anchor record workaround.
4. Ratify the three required fields and conditional legal requirements.

Exit gate met by `margince-foundation` PR #1104: updated spec, data ownership,
contract shapes, event semantics, acceptance criteria, and migration approach
were approved upstream.

### Phase 1 — Unified profile read/write substrate (implementation complete)

PR #127 delivers the additive profile/fact vocabularies, the provenance-bearing
and RLS-safe company-context assembler, expanded `/company` read/write mapping,
deterministic scopes/fingerprints, human precedence, and the existing atomic
mutation + audit + outbox write shape.

Its local and GitHub gates cover provider-free manual creation/editing,
cross-workspace isolation, provenance, audit/outbox behavior, migrations, and
zero-skip integration. The remaining stale-draft conflict belongs to Phase 2's
version-bound onboarding confirmation endpoint; it is not a second Phase 1 write
path.

### Phase 2 — Onboarding deep read

1. Reuse the current crawler, extraction lanes, evidence gate, legal census, and
   merging logic behind the onboarding dossier contract.
2. Expose progressive findings/coverage and the manual fallback.
3. Implement bound accept-subset and a single transactional confirmation into the
   anchor organization, profile fields, and facts.
4. Keep team members in separate lead proposals and make that separation visible.
5. Address the already-recorded deep-read durability issues—transactional
   enqueue, stale-running recovery, and redeem-plus-apply atomicity—before making
   the pipeline the installation's front door.

Exit gate: website and manual paths produce the same confirmed context shape;
zero domain persistence occurs before confirmation; failed/partial reads never
block setup.

### Phase 3 — Context-aware product calls

1. Add the central provider and exhaustive per-task policy registry.
2. Integrate in this order: agent loop, reply/draft surfaces, offer drafting,
   voice knowledge grounding, then search query expansion.
3. Add context fingerprints to cache and AI-call trace metadata.
4. Run task-specific evals with context on/off; do not roll context into brief
   ranking or qualification until their deterministic contracts name it.

Exit gate: every AI task declares `none` or bounded scopes; prompt snapshots prove
the correct data/instruction separation; no stale cache survives a profile edit.

### Phase 4 — Five-step onboarding UI

1. Split Read and Confirm into separate route/state-machine steps.
2. Build the website/manual choice, live read state, grouped review, targeted
   missing questions, accept-subset controls, and completion reveal.
3. Persist resumable wizard state server-side as required by the foundation spec;
   reload and OAuth returns must reconstruct the same confirmed/draft state.
4. Add component stories/tests for empty, manual, reading, partial, robots-
   blocked, no-model, multi-entity legal warning, review, save conflict, and
   complete states.
5. Add responsive visual regression, accessibility, reduced-motion, i18n, and
   real-stack UAT.

Exit gate: a new installation can finish manually with zero egress; a normal site
produces progressive recognition and a confirmed context; no path presents
guessed facts or traps the user.

### Phase 5 — Refresh, observability, and rollout

1. Add “Company context” settings and “Refresh from website,” including explicit
   conflict resolution against human-held values.
2. Instrument website-path selection, manual-path selection, time to first
   grounded result, time to confirmation, missing-required rate, manual correction
   rate, extraction coverage, context size/token cost, and per-task quality lift.
3. Roll out behind a server capability flag: storage/read model first, then task
   integrations, then the new first-run UI.
4. Backfill existing installations by assembling context from current rows; do not
   rerun websites or overwrite human values automatically.

Exit gate: existing customers retain their data and provenance, context-enabled
tasks are observable and reversible, and the old quick-read UI can be removed.

## Likely implementation seams

The exact file set follows the upstream contract decision, but the change should
stay concentrated in these existing homes:

- `backend/api/crm.yaml` for company profile/context and onboarding-dossier wire
  shapes, followed by normal code generation;
- additive migrations under `backend/migrations/core/` for vocabulary, context
  versioning, or the ratified operational draft shape;
- `backend/internal/modules/people/` for canonical profile/fact persistence,
  company-context assembly, human precedence, and site-read ownership;
- `backend/internal/compose/` for shared crawl/extraction orchestration,
  confirmation, context-provider composition, and task integrations;
- `backend/internal/modules/ai/tasks.go` plus a derived policy fitness test for
  exhaustive per-task context declarations;
- `backend/internal/modules/agents/runner/` and context-sensitive compose
  features such as offer drafting for the first bounded consumers;
- `frontend/src/screens/onboarding.tsx`, its CSS/i18n/tests, and reusable
  design-system trust components for the five-step experience;
- `STATUS.md` and the upstream foundation chapters/use cases as each contract
  decision and implementation slice lands.

## Verification matrix

- Contract/codegen drift and additive migration down/up tests.
- Derived vocabulary parity across contract enum, extraction schema, Go maps, and
  database constraints.
- RLS and row-scope tests for context reads and onboarding drafts.
- Human-over-machine precedence, refresh conflict, accept-subset, and stale-read
  tests.
- Mutation + audit + outbox atomicity for confirmation and edits.
- No-guess/evidence tests for every new extracted field and fact.
- Manual no-AI/no-egress end-to-end path.
- SSRF, robots, byte/page/deadline, legal multi-entity, partial, and recovery tests.
- Context scope/budget, data-not-instructions, secret stripping, fingerprint,
  cache invalidation, and task-policy exhaustiveness tests.
- Context on/off evals for each adopted model task.
- Frontend unit/component tests, real-stack UAT, accessibility checks, responsive
  visual regression, and progressive latency telemetry.

## Decisions to answer before coding

1. Confirm the required minimum: name + offer summary + ICP.
2. Confirm that legal/VAT/address fields are conditional rather than universal.
3. Ratify `offer_summary` as distinct from `value_proposition`.
4. Choose the onboarding operational-draft persistence shape.
5. Ratify the expanded field vocabulary and the consumer for every field.
6. Decide which progressive findings are returned before the crawl finishes and
   how their hashes bind to confirmation.
7. Set first-result and full-read latency targets from production-like evidence.
8. Decide retention for abandoned onboarding read dossiers and their fetched
   snippets.
9. Decide whether accepted website facts are always presented as untrusted T2
   model data or promoted to a separate confirmed-but-external trust label. They
   must never become instructions either way.
10. Decide the first context-enabled AI surfaces and the eval threshold each must
    clear before rollout.
