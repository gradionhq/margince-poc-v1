# Margince conversational workspace — unified company onboarding concept

> **Status:** onboarding baseline implemented; reusable backend framework and
> compact company-maintenance caller remain follow-up work. This document is
> both the implemented product model and its remaining direction. It does not
> override the contract-first specification in `margince-foundation`; contract,
> ADR, and acceptance-criterion differences still require upstream
> reconciliation.

Margince should feel like one professional, governed AI collaborator rather
than a sequence of forms with an AI illustration beside them. The first
application of that model is company onboarding: Margince learns the legal
identity, offer, products, ideal customer, positioning, and sales motion with
or without a website; shows its work while it researches; proposes exactly
what should be stored; accepts questions and corrections; and writes nothing
until the administrator explicitly confirms the current proposal.

The same interaction model should then serve company-context refresh and later
AI-assisted product surfaces in a smaller form. This is therefore both an
onboarding concept and the first concrete use of a reusable Margince
interaction framework.

## Outcome

The visible onboarding sequence becomes:

1. **Company** — conversation, optional website research, proposal, and
   confirmation in one workspace.
2. **Voice** — optional Voice DNA setup.
3. **Results** — the confirmed understanding.
4. **Connect** — optional inbox connection.

The Company step has no separate:

- website-versus-manual selection screen;
- conventional manual questionnaire screen;
- company Review step;
- general-purpose chatbot.

The website is an optional accelerator, not a route the administrator must
choose. Conversation is the parent experience. A website read, when requested,
is one governed activity inside that conversation.

## Product principles

1. **One workspace, one relationship.** Margince remains visibly present from
   the first question through confirmation. Navigation does not interrupt the
   research-to-decision thread.
2. **Legal identity first.** Registered name, address, registration and
   VAT/UID information are sought before positioning and sales enrichment.
3. **Evidence while working.** Grounded discoveries appear during the read,
   not only after it ends.
4. **Proposal, not silent mutation.** Margince may research, explain, and
   suggest. A human owns every write.
5. **Per-field provenance.** Website evidence, administrator statements, and
   administrator-approved summaries may coexist in one draft without being
   misrepresented.
6. **Conversation is bounded work.** This surface completes company setup; it
   is not a general knowledge assistant.
7. **The controller owns safety.** Scope, progression, resource limits, and
   confirmation are enforced by application code, not entrusted to a prompt.
8. **Honest runtime transparency.** The configured route and exact
   provider-served models, calls, tokens, model latency, and price-on-read cost
   remain visible for the current task.
9. **Resume without loss.** Refresh, OAuth round-trips, deferred AI budget, and
   legacy wizard checkpoints reconstruct the same draft and current task.
10. **Human truth outranks machine refreshes.** Later site findings may propose
    changes but never silently replace a human-held value.

## The workspace

On desktop, the reusable workbench has a persistent runtime header and two
cooperating areas. On narrow screens they stack in the same order.

```text
┌──────────────────────────────────────────────────────────────────────────┐
│ Margince AI · configured route · models used · calls · tokens · cost    │
├───────────────────────────────┬──────────────────────────────────────────┤
│ CONVERSATION                  │ LIVE COMPANY ARTIFACT                    │
│                               │                                          │
│ Margince status and questions │ Research feed while reading              │
│ Administrator replies         │ Proposed company profile when ready      │
│ Suggested-change cards        │ Evidence and provenance per field        │
│ Confirmation action           │ Legal choice and fact inclusion          │
│                               │                                          │
│ [ Ask, answer, or correct… ]   │ [ Confirm and save ]                     │
└───────────────────────────────┴──────────────────────────────────────────┘
```

The animated Core remains Margince's presence, status, and attention signal.
It should breathe or speak subtly while Margince is responding and show
bounded progress while research runs. Dense evidence and editable business
information remain in the artifact area, where they are legible and
accessible; they are not squeezed inside the animation.

The same component later collapses to a compact header, short thread, and
single artifact card for ordinary product screens.

## Entry: no method-selection screen

After login, Margince opens directly in the Company workspace:

> Hi, I'm Margince. I need to understand your organization before I can work
> effectively for you. The fastest way is to send me your website. If you
> prefer not to, tell me your company name and I'll ask one question at a time.

The composer accepts any of these valid beginnings:

- a website URL;
- a short company description;
- “I don't want to provide a website”;
- the quick action **Answer questions instead**.

A valid URL starts the governed site-read pipeline. Any other in-scope answer
starts conversational collection. An administrator may add a website later,
or answer missing questions after a site read; the two are not exclusive
modes.

The existing `source_mode: website | manual` checkpoint can remain as a
compatibility detail during migration, but it is no longer the product's
conceptual model. Provenance belongs to individual fields and facts.

## Conversational collection without a website

Margince asks one focused question at a time. A deterministic missing-field
planner owns the sequence so that the model cannot forget required information
or drift into a different interview.

The recommended order is:

1. registered legal name;
2. registered address;
3. commercial-register number and VAT/UID number;
4. customer-facing company name;
5. products and core offer;
6. ideal customer profile and buying center;
7. customer problems and desired outcomes;
8. value proposition and differentiation;
9. buying intent, common objections, and sales motion;
10. history, industry, and other useful context.

The first six dimensions are completion-critical for a useful company
understanding. Legal details are **must-ask**, but resolution does not always
mean a value: the administrator may explicitly say that a number is not
applicable or not yet known. That state must not be stored as a fake company
value such as the literal string “unknown.” The upstream contract must decide
which legal fields are universally required values and which require an
explicit resolution state.

Every accepted answer updates the artifact immediately:

```text
Registered legal name
Gradion GmbH
Source: provided by you
```

If Margince condenses a long answer into a proposed ICP, offer, or positioning
statement, the result is an administrator-approved AI summary, not a verbatim
administrator statement:

> Based on your answer, I suggest storing: “Mid-sized European manufacturers
> modernizing revenue operations.” Use this wording?

The summary reaches the draft only after approval.

## Website-assisted research

### Live progress

The existing dossier polling remains the delivery mechanism. Persisted,
cumulative snapshots are preferable to an ephemeral SSE-only stream because
they survive reload and deferred work. Polling roughly once per second is
sufficient when each snapshot contains meaningful incremental state.

While reading, Margince reports the current activity in the conversation:

- “I'm reading `example.com/impressum`.”
- “I'm extracting the legal identity.”
- “I'm comparing the offer and customer evidence.”

The artifact shows a bounded live research feed. New gate-surviving findings
are added as they appear:

- possible legal entity, registered address, or register/VAT number;
- product, service, or offer;
- market, customer, or customer proof;
- ideal-customer and buyer evidence;
- positioning, pains, outcomes, and sales-motion evidence.

Each item carries its page source and a truthful state. Page-local legal
candidates may be marked **still verifying** until the global legal census has
checked ambiguity. Ungrounded model output never enters the feed. A candidate
rejected by a later global gate must not silently become a proposed field.

### Implemented live substrate

The worker now persists cumulative pages, facts, people, and legal candidates,
and the artifact renders them while the crawl continues. The dossier carries:

- current page URL and classified page kind;
- grounded facts;
- page-gated legal candidates;
- profile fields when the terminal profile lane has produced them;
- phase, pages read, warnings, and budget deferral as today.

The UI derives feed additions from successive versioned snapshots. It does not
need an unbounded activity log.

### Transition to a proposal

While research runs, the right side answers **what I have found**. When the
read reaches `ready` or `partial`, the same area transitions to **what I
suggest we store**, retaining the sources that justify each proposal.

The proposal is grouped into:

- Legal company information
- Offer and products
- ICP and customer needs
- Positioning and sales motion

Each field shows:

- the proposed value;
- website, administrator, or approved-summary provenance;
- the supporting source when applicable;
- confidence or uncertainty where useful;
- Change and Remove controls.

Additional repeatable facts remain compact by default:

> 32 supported facts included

Expanding the section allows individual inclusion or exclusion. New facts
arriving in a later snapshot default to included, but an administrator's
explicit exclusion must survive subsequent polling; the selection algorithm
must not reselect everything whenever the dossier version advances.

If the site names several legal entities, Margince asks which one belongs to
the installation and presents intact legal blocks. Selecting one applies its
name, registered address, and register/VAT data together. Margince never
recombines details from sibling entities.

## Conversation behavior

The administrator can:

- ask why a field was suggested;
- ask what a page stated;
- request a recommendation for an empty field;
- correct a value;
- provide missing information;
- exclude a fact;
- select a legal entity;
- ask for current research status;
- confirm the complete proposal.

Questions do not mutate the draft. Corrections and recommendations produce a
small suggested-change card with **Approve** and **Discard**. Approval changes
only the onboarding draft; it is still not a domain write.

Missing completion-critical information becomes the next conversational
question rather than a validation error after a distant Continue button.

### Explicit response kinds

The current `message + proposed_changes` response is insufficient: a model can
return changes while answering an ordinary question. Every response must carry
one of a closed set of dialogue kinds:

- `status`
- `answer`
- `recommendation`
- `correction`
- `confirmation`
- `clarification`
- `off_topic`

Server validation enforces the relationship:

- `status`, `answer`, `clarification`, and `off_topic` contain no proposed
  changes;
- `recommendation` and `correction` may contain grounded, typed changes;
- `confirmation` is meaningful only while a current version-bound proposal is
  ready to save;
- ambiguous input defaults to an answer or clarification, never a change;
- Margince does not apologize unless acknowledging a concrete error,
  correction, or failed operation.

The regression example is explicit:

```text
Administrator: Does this work?
Margince: Yes. I've finished reading 18 pages and prepared 11 supported
          company details. Nothing is saved yet.
Response kind: status
Proposed changes: none
```

## Confirmation

When all completion-critical fields are resolved—and, if research was
started, the website read is ready or partial—Margince writes:

> I'm ready to store 14 company details and 27 supported facts. Nothing has
> been saved yet.

The message contains:

- **Confirm and save**
- **I want to change something**

The same primary action is available at the bottom of the artifact. A research
flow calls the existing version-bound confirmation path: the server checks the
dossier version and proposal hash, writes the company, selected fields and
facts in one audited transaction, and marks that dossier confirmed. A manual
flow has no dossier to mark; it confirms the current onboarding-draft version
through the audited company write after the required fields are resolved.
Failed or abandoned research offers an explicit **Continue manually** action;
that clears the dossier binding before the manual confirmation path is enabled.

Typing the exact active command “Confirm and save” may invoke the same action
when the UI has a current ready proposal. A vague “yes” asks for clarification.
The model never directly executes the write, and a confirmation phrase emitted
by the model has no authority.

If the proposal changes or a stale version is detected, confirmation is
disarmed, the latest dossier is loaded, and Margince explains what changed.

On success, Margince shows a confirmation receipt and advances directly to
Voice. There is no intervening Review page.

## Scope and abuse resistance

This surface is a task conversation, not open chat. A prompt alone cannot
enforce that boundary. Each request passes through application-owned guards:

1. authenticated-human and administrator authorization;
2. current onboarding/company-workspace resolution;
3. message-size, history, rate, concurrency, token, and cost bounds;
4. closed intent classification;
5. default-deny scope routing;
6. bounded context assembly;
7. structured model completion;
8. evidence, proposal, and action validation;
9. explicit confirmation for every write.

### Allowed intents

- provide or correct company information;
- ask why information is required;
- ask about a current finding or source;
- request a recommendation for a company field;
- say that a field is unknown or not applicable;
- provide a website;
- ask about research status;
- confirm the current proposal.

Everything else is `off_topic`. The response is deterministic where possible
and repeats the unresolved company question:

```text
Administrator: Write me a Python script.
Margince: I can only help configure your company in this workspace. We were
          confirming your registered address. What should I store, or is it
          not applicable?
```

Repeated off-topic or adversarial messages trigger a cooldown before more
model calls are allowed. The administrator is never trapped: the proposal
remains directly editable and the session can resume later.

### Deterministic routes

The following should not need a model call:

- current read status and progress;
- exact Skip, Unknown, and Not applicable actions;
- off-topic redirection once classified;
- exact active confirmation;
- selecting a presented legal entity;
- approving or discarding a typed proposal.

Deterministic routes reduce latency, spend, and prompt-attack surface.

### Website prompt injection

Website text is untrusted data even after it becomes accepted company context.
Instructions found in pages never become system instructions. The existing
evidence rules remain binding:

- only source IDs supplied with the current dossier may be cited;
- a website-derived proposed value must occur in its cited evidence;
- an administrator-derived value must occur in an administrator statement, or
  a separately approved summary must cite that statement;
- no cross-organization data is available;
- no general browsing or arbitrary tool access is attached to the
  conversation;
- secrets, unsafe URLs, provider payloads, and internal errors are not exposed.

### Resource limits

The framework enforces configurable bounds for:

- characters per message;
- preceding conversation turns and characters per turn;
- messages and model calls per minute;
- concurrent calls per human and installation;
- cumulative tokens and price-on-read cost per onboarding session;
- repeated off-topic or failed-validation attempts.

Budget deferral is explicit and resumable. Missing price information remains
unpriced rather than appearing as a false zero.

## Reusable Margince interaction framework

The reusable abstraction is a **scoped conversational task framework**, not a
single omnipotent assistant. It has two concrete callers immediately:

1. first-run company onboarding;
2. the existing company-context refresh and maintenance surface.

That second caller is required to prove the abstraction before it spreads to
deal preparation, automation building, communication drafting, and data
cleanup.

### Shared responsibilities

The framework owns:

- bounded turn handling;
- the closed response envelope;
- default-deny intent and off-topic behavior;
- structured completion and validation hooks;
- proposal, citation, and pending-action primitives;
- explicit-confirmation choreography;
- rate, token, and cost limits;
- exact runtime transparency;
- standard loading, error, retry, and reduced-motion behavior;
- reusable conversation, artifact, proposal, and confirmation UI.

### Workflow-adapter responsibilities

Each adapter is registered in the closed workflow-scope registry and declares:

- allowed intents;
- readable context and its token budget, sourced only through the governed
  CompanyContext path when company data is in scope;
- required-field or task progression;
- typed proposal vocabulary;
- evidence and provenance rules;
- directly available actions;
- confirmation handler;
- product-specific copy and artifact rendering.

An adapter receives no tools or actions by default. Missing policy is a startup
or fitness-test failure, not permissive behavior. The registry also binds the
applicable CompanyContext rollout gate and propagates the context fingerprint
into runtime transparency; an adapter cannot substitute an ad-hoc company read.

### Backend placement

Model execution, routing, usage, and pricing remain in `internal/modules/ai`.
Business conversation orchestration crosses identity, people, site reads, and
the model runtime, so it belongs in the composition layer.

The intended shape is:

```text
internal/compose/assistantflow/       reusable bounded interaction mechanics
internal/compose/onboardingassistant/ company collection + confirmation adapter
internal/compose/companyassistant/    company refresh + conflict adapter
```

The exact package split should follow the repository's named-growth rule and
land only with both concrete callers. The framework must not introduce a new
durable business entity in compose.

The general company conversation surface must work without a site read. A
conceptual endpoint is:

```text
POST /onboarding/company/messages
```

The site-read start, poll, and confirm endpoints remain the governed research
and persistence paths beneath it.

### Frontend placement

`MarginceWorkbench` remains the design-system shell. Reusable children should
cover:

- runtime header;
- assistant and administrator turns;
- composer;
- citation group;
- suggested-change card;
- pending-confirmation card;
- responsive artifact rail;
- compact variant for later screens.

Company field groups, legal-entity selection, fact inclusion, and onboarding
copy remain feature components rather than leaking business concepts into the
design system.

## Conceptual contract

The general request needs bounded history and version context, but not a
general tool payload:

```text
message
history (bounded, oldest first)
onboarding_state_version
site_read_id (optional)
```

The response envelope is shared while proposals remain workflow-typed:

```text
kind
message
citations
proposed_changes
next_question (optional)
remaining_required_fields
available_action (closed enum, optional)
ai_runtime
```

There is no arbitrary `data` map or client-supplied action. Each workflow's
OpenAPI schema names its proposal fields and action enum. Generated contract
types remain authoritative.

Conversation history may remain client-supplied and bounded initially. Only
accepted draft values, provenance, wizard checkpoints, and the final
confirmation need durable domain state. Any future decision to retain full
chat transcripts must first define purpose, access, retention, SAR, and erasure
behavior. Existing optional AI payload capture retains its current operational
posture and is not silently enabled by this feature.

## State models

### Visible Company workspace

```text
collecting → researching → resolving → ready → saving → confirmed
                 │              ▲        │
                 ├─ deferred ────┘        └─ stale → resolving
                 └─ partial ───────────────→ resolving
```

- **collecting:** awaiting a URL or the next administrator answer.
- **researching:** site read active; discoveries accumulate on the right.
- **resolving:** research ended or conversational collection continues; gaps
  and conflicts are addressed.
- **ready:** completion-critical information is resolved and the current
  proposal is confirmable.
- **saving:** the version-bound write is in flight.
- **confirmed:** receipt shown; advance to Voice.
- **deferred:** budget-bound work will resume automatically; conversation and
  direct editing remain available.

### Proposal value

Every value needs:

- typed field or fact identity;
- current value;
- source kind: website, administrator, or approved AI summary;
- evidence/source reference when applicable;
- approval and inclusion state;
- the dossier/draft version that produced it.

Typing over a website value makes the replacement an administrator assertion
and removes website provenance, matching the existing invariant.

## Compatibility and migration

1. Keep accepting the legacy `confirm` onboarding checkpoint initially.
2. Map a restored creator at `confirm` back into the Company workspace with
   its draft, fact selection, and site-read ID intact.
3. Stop emitting `confirm` from the new client.
4. Keep members starting at Voice as today.
5. Reconstruct a ready confirmation action from the current terminal dossier
   and draft after reload; do not persist authority in chat text.
6. Preserve legacy website/manual source values while moving new logic to
   per-field provenance.
7. Reconcile and then regenerate the OpenAPI contract; never hand-edit
   generated files.
8. Reconcile the corresponding subsystem and tutorial changes upstream before
   treating this implementation document as normative specification.

## Implementation sequence

1. **Upstream reconciliation**
   - Ratify the four-step onboarding sequence.
   - Define legal must-value versus must-resolve semantics.
   - Define the shared response envelope and closed intent/action sets.
   - Define compatibility for `confirm` and exclusive `source_mode`.
2. **Framework with two adapters**
   - Add reusable bounded interaction mechanics.
   - Adapt company onboarding and company-context maintenance together.
3. **Progressive research substrate**
   - Persist current page/kind and cumulative gated findings.
   - Publish legal candidates and profile fields at safe commit points.
   - Preserve version/hash coherence.
4. **General company conversation**
   - Support messages without a site read.
   - Add deterministic required-field planning.
   - Add response kinds, off-topic routing, and validation.
5. **Unified Company workspace**
   - Remove method selection.
   - Render the live feed and proposal in the artifact rail.
   - Move legal choice, fact inclusion, corrections, and confirmation into the
     workbench.
6. **Wizard simplification**
   - Remove the visible Review step.
   - Restore legacy checkpoints safely.
   - Advance confirmation directly to Voice.
7. **Hardening and certification**
   - Run repository gates, real-Postgres integration, accessibility and
     browser suites.
   - Run the adversarial conversation corpus.
   - Repeat the ten-company ingestion benchmark and verify discoveries appear
     during, not only after, each read.

## Acceptance criteria

### Unified experience

- Login leads directly to the Company conversation; no source-choice screen is
  required.
- A URL begins research; refusing a URL begins one-question-at-a-time
  collection in the same composer.
- Website and administrator input can be mixed without restarting.
- No separate company Review step exists.
- Confirmation advances directly to Voice.

### Live research

- At least one grounded discovery can appear before a multi-page read reaches
  its terminal status when the site supplies one early.
- The current page and phase are truthful and survive polling/reload.
- Legal candidates remain intact and visibly provisional until globally
  resolved.
- Progressive facts are visible, cited, and bounded.
- Explicit fact exclusions survive later dossier versions.

### Conversation correctness

- “Does this work?” returns status, no apology, no proposal, and no draft
  mutation.
- Questions about findings answer with citations and no implicit change.
- Corrections and recommendations create typed suggestion cards.
- A suggestion affects the draft only after approval.
- The deterministic planner asks every unresolved completion-critical
  question in the ratified order.
- Unknown and not-applicable legal answers remain honest states, never fake
  stored strings.

### Safety and governance

- Off-topic requests are refused and the current onboarding question is
  resumed.
- Website prompt injection cannot change system behavior or create an
  unsupported proposal.
- Unknown citations, unsupported field names, uncited website values, and
  unbound administrator summaries are rejected server-side.
- No model response can invoke a write.
- Vague confirmation never writes.
- Exact confirmation is accepted only for a current ready proposal and still
  passes the existing version/hash transaction gate.
- Cross-organization reads and actions remain impossible.
- Rate, history, concurrency, token, and cost bounds are tested.

### Transparency and design

- The header distinguishes configured models from provider-reported models
  actually used for the task.
- Calls, tokens, model latency, estimated cost, and unpriced usage remain
  accurate as conversation calls accumulate.
- First-person English and German copy covers every state.
- Desktop, 390 px mobile, keyboard, screen-reader, reduced-motion, long-text,
  failure, deferred-budget, partial-read, and resume states pass visual and
  automated accessibility checks.
- The company-context maintenance screen proves the compact reusable variant.

### Verification corpus

The AI certification and application tests include:

- ordinary status and explanatory questions;
- direct corrections and recommendation requests;
- ambiguous “yes” and exact confirmation;
- general knowledge, coding, and creative-writing requests;
- requests for another organization's data;
- attempts to bypass confirmation;
- prompt injection embedded in website pages;
- long, repeated, multilingual, and encoded adversarial messages;
- several legal entities on one site;
- missing, unknown, and not-applicable legal information;
- corrections while a read continues;
- refresh, deferred budget, partial extraction, version skew, and retry.

## Measures of success

The feature should make onboarding faster without weakening governance. Useful
product measures are:

- completion rate and median time to confirmed company;
- percentage using website, conversation, or both;
- unresolved legal-field rate at confirmation;
- number of suggested values changed or excluded before confirmation;
- percentage of reads showing a live discovery before completion;
- off-topic and validation-failure rates;
- model calls, latency, and provider cost per completed onboarding;
- resume success after reload or deferred budget;
- confirmation version-skew rate.

Metrics describe the interaction, never broaden model access or justify silent
collection of conversation content.

## Final design rule

Margince should feel open and conversational while remaining structurally
narrow:

> One reusable interaction protocol, many strictly scoped assistants. The AI
> may explain and propose; application policy controls progression, evidence,
> resources, and every write.
