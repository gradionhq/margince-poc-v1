# Voice profile assessment and delivery plan

Status: implemented in this worktree on 2026-07-19. Onboarding now persists real
sources and durable builds; generated email consumes the active immutable
version through one shared drafter; sent-mail learning is own-text filtered,
batched, reversible, and user-controlled in Settings → My voice. The upstream
spec still needs reconciliation for the starter threshold, automatic activation
policy, and closed event catalog described below.

## Implementation delivered

- immutable profile versions, source fingerprints, durable River builds,
  deltas, rollback-as-forward-version, and safe failure recovery;
- deterministic stylometry plus a strict evidence-citing model schema;
- universal EN/DE anti-AI detection, one critic retry, and a hard final
  sanitizer on every shared email drafting path;
- real, resumable onboarding with paste/upload/speaker filtering, an honest
  800-word starter floor, server-backed progress, and the actual artifact;
- Graph Sent Items and IMAP `\Sent` capture with independent cursors, Gmail
  direction-aware capture, own-authored text extraction, and sensitive-mail
  exclusion before corpus retention;
- daily automatic eligibility scanning with a 2,000-word/10-message threshold
  or weekly refresh, always using the same durable build path;
- protected draft originals and significant edited-sent learning signals;
- Settings → My voice for preferences, opt-in/pause, source include/exclude and
  weighting, rebuild, version history, rollback, deltas, and permanent corpus
  clearing while keeping human instructions.

## Outcome

Every user should leave onboarding with an honest starter voice that is already
better than a generic AI style. It should improve from the user's own sent text,
without learning recipients' words, personal correspondence, quoted history, or
model-generated prose. The user remains in control: they can inspect evidence,
edit durable preferences, pause learning, rebuild, compare versions, roll back,
or clear the corpus.

Voice has two independent layers:

1. **A universal anti-AI baseline** applies to every generated draft. It catches
   structural tells such as parenthetical em dashes, abstract “not X, but Y”
   reframes, balanced tricolons, canned openers and calls to action, corporate
   filler, and suspiciously uniform rhythm. These rules do not depend on the
   corpus being large enough.
2. **An individual evidence-derived profile** describes how this user thinks and
   writes: directness, cadence, vocabulary, openings, closings, punctuation,
   formality, point of view, recurring moves, and differences between registers.
   Human-written preferences remain separate and always outrank inference.

Company context supplies the facts; voice supplies the expression. They must not
be merged into one prompt artifact or allowed to overwrite one another.

## Executive assessment

The second onboarding step is not built in the product sense. It is a polished
prototype whose progress, source counts, build result, voice rules, and sample
draft are fabricated in React state. No source text reaches the backend, the
“build” waits 1.1 seconds without calling an API, and the result disappears on a
reload or OAuth redirect.

The backend has strong primitives: per-user profiles, a separated human
personality field, source ingestion with speaker attribution, source exclusion
and weighting, real word counts, RLS/RBAC, audit records, and version integers.
Those primitives are not connected to a builder, model task, job runner,
onboarding, Settings, email capture, or drafting. `SetDerivedProfile` is called
only from tests. No active product path reads `voice_profile_md` when generating
text.

Automatic improvement is absent. Gmail capture may see sent messages as part of
its mailbox history, but nothing turns them into voice sources. Graph sync reads
the Inbox delta only, and IMAP selects `INBOX` only. The current mail mapper also
retains headers, quoted replies, signatures, and potentially the other person's
words, so feeding its activity body directly into voice learning would be unsafe
and inaccurate.

The product should therefore not expose the current animation as a completed
feature. The first releasable slice is a real, resumable starter build plus a
single drafting consumer. Automatic learning follows only after own-authored text
extraction, Sent-folder parity, version snapshots, rollback, and privacy controls
exist.

## What exists today

### Onboarding UI

`frontend/src/screens/onboarding.tsx` currently:

- presents four top-level steps: Company, Voice, Results, and Connect;
- seeds source tiles with hard-coded word counts from 1,200 to 18,000 words;
- increments those counts when a tile is selected;
- counts words in uploaded files locally, then discards their contents;
- enables a build at 300 simulated words, while the current spec says 4,000;
- finishes the build through a timer and shows hard-coded profile content;
- holds `voiceBuilt` only in component state;
- lets the user continue without a server profile or durable completion state.

The existing frontend test verifies that the skipped-voice results copy is
honest. It does not prove ingestion, build persistence, resume, errors, or the
quality of the result. There is no Voice section in Settings.

### Backend voice foundation

The implementation in `backend/internal/modules/ai` already provides:

- one current profile row per owner, with user/team/workspace scope support;
- separate machine-derived `voice_profile_md` and human-edited
  `personality_md`;
- source kinds for posts, transcripts, emails, chat, long-form text, and voice
  memos;
- explicit speaker attribution for conversational sources, with only the
  selected speaker's turns included;
- idempotent source references, per-source weighting and exclusion;
- a text-only one-MiB source limit and server-computed word counts;
- corpus quality bands at 8,000, 20,000, and 30,000 words;
- manifest responses that do not return raw corpus content;
- RLS, RBAC, audit/outbox write discipline, and integration coverage.

This is worth keeping. The speaker-attribution contract is stronger than the
LinkedIn Automation implementation and should remain mandatory.

The missing production capabilities are:

- no build or rebuild API;
- no builder service, prompt, structured result schema, model task, or River job;
- no durable job progress or actionable failure state;
- no immutable version snapshots, version diff, or rollback;
- no source-set fingerprint binding a result to its evidence;
- no exclusion reason or extraction-version metadata;
- no stale/pending state when the corpus changes;
- no email capture integration or Sent-folder parity;
- no edited-draft, rejected-draft, or gold-pair learning signal;
- no weekly “what changed” record;
- no user management surface;
- no shared drafting service that consumes the active voice;
- no deterministic anti-AI output gate.

The two current draft-email paths return generic deterministic copy. The voice
artifact is not a consumer input, so even a test-created profile cannot change a
draft.

### Contract gaps to reconcile upstream

The sibling specification correctly asks for continuous corpus growth, weekly
deltas, append-only versions, rollback, a management screen, and explicit
rebuilds. It also says both that the voice improves without a retraining step and
that adding sources must never trigger a model call because rebuild is explicit
and paid. Those statements cannot both describe a derived artifact used at draft
time.

The spec also marks this area as built although the load-bearing paths above are
absent. It requires a 4,000-word first build, which is too high for a practical
onboarding upload, and it describes voice-profile events without a ratified event
catalog entry. These are upstream contract defects, not implementation details to
work around here.

## Lessons from LinkedIn Automation

The useful reference is not its web task plumbing; it is the way it separates
measurement, inference, examples, and enforcement.

### Adopt

1. **Measure before asking a model.** Compute deterministic stylometry overall
   and by register: sample and word counts, sentence length distribution,
   punctuation/emoji/line-break rates, common words and bigrams, and real opening
   and closing examples. Treat these measurements as ground truth in the model
   prompt, not as generation quotas.
2. **Model cognition, not cosplay.** Describe stance, reasoning pattern,
   directness, recurring interests, structural habits, vocabulary, texture, and
   register changes. Do not reduce a person to adjectives such as “professional
   and friendly.”
3. **Ground every claim.** Profile rules and signature moves should cite source
   sample identifiers. Representative examples must be the user's verbatim text;
   the model must not invent few-shot examples.
4. **Select examples deliberately.** Use a small, structurally diverse set of
   representative samples. Do not choose viral or engagement-heavy outliers.
5. **Keep spoken and written registers distinct.** Spoken material is valuable
   for cadence and thinking patterns, but it should be translated conservatively
   into written guidance.
6. **Learn strongly from real rewrites.** Preserve the AI original and final
   user-sent version as a gold pair. Generalize repeated transformations, not the
   topic-specific content. Require repeated evidence before a correction becomes
   a durable rule.
7. **Enforce anti-AI rules outside the prompt.** Run deterministic detection,
   feed violations to a critic/rewrite pass, then run a final sanitizer for hard
   bans such as parenthetical em dashes.
8. **Evaluate against held-out real writing.** Compare voice-guided drafts with
   generic drafts using deterministic tell rates and blinded preference tests.

### Do not copy

- an in-process background task without durable status, retry, or recovery;
- destructive source deletion as the only correction mechanism;
- weak parsing of a free-form model artifact;
- a corpus sampler that lets one large source starve register diversity;
- auto-learning from drafts before enough independent human rewrites exist;
- a single fixed-order model judge, which invites position and model-family bias;
- manual-only rebuild as the answer to continuous improvement.

Margince's transaction, RLS, audit, approval, consent, and job infrastructure is
stronger. The reference algorithms should be ported into those boundaries, not
the other way around.

## Recommended product behavior

### Starter voice during onboarding

After company confirmation, the server creates or resumes the current user's
profile. The UI never infers completion from local component state.

The step should offer three honest inputs:

1. paste representative messages, posts, or documents the user actually wrote;
2. upload written or spoken material, with explicit speaker selection where
   required;
3. state durable preferences and prohibitions in plain language, for example
   “never use em dashes,” “do not use fake contrasts,” or “sign off with Lars.”

Source tiles describe categories and their value; they do not pretend that data
has already been found. Uploads are ingested immediately and the meter reflects
backend counts. The build button creates a durable build job, and the UI polls
real stages and errors.

The 4,000-word hard gate should be changed upstream to two honest levels:

- **Starter:** roughly 800–1,500 own-authored words or several substantial
  samples. It combines high-confidence personal signals with the universal
  anti-AI baseline and is clearly labelled provisional.
- **Established:** 4,000 words unlocks more specific inferred rules. Existing
  8,000/20,000/30,000 bands continue to express increasing evidence quality.

This lets every willing user leave onboarding with a first profile without
claiming precision the evidence cannot support. Skipping remains allowed and
activates only the universal anti-AI baseline. The Results step renders actual
measurements, a few evidence-backed rules, and a real comparison draft.

Mailbox connection remains after Results. It offers an explicit “learn from my
sent messages” choice and explains what is excluded. OAuth redirects and browser
reloads resume from server state.

### Universal anti-AI baseline

Build one reusable EN/DE linter used by every outbound AI drafting path. Start
with these calibrated detectors:

- parenthetical em/en dashes, excluding legitimate numeric ranges and URLs;
- abstract negation or false-reframe patterns such as “It is not X. It is Y” and
  “not about X, but about Y,” including German equivalents;
- balanced three-part slogans and suspiciously symmetrical cadence;
- canned openers such as “Here is the thing” and generic engagement questions;
- consultant-style calls to action such as “Is your organization ready?”;
- corporate AI-ese vocabulary in English and German;
- sentence-length uniformity that conflicts with the user's measured rhythm;
- generic influencer line stacking, excessive headings, and empty intensifiers.

Do not ban every use of contrast. Concrete correction, causal qualification, and
genuine additive statements are valid. The detector should target the abstract,
balanced AI construction and be tested with true and false positives.

The profile prompt can request compliance, but enforcement must be mechanical:
detect, optionally rewrite with a critic, re-check, and fail closed or remove a
hard violation before presenting a draft.

Instruction precedence is explicit:

1. compliance, channel, and factual-grounding rules;
2. universal anti-AI hard rules;
3. human-written personality and prohibitions;
4. evidence-derived individual voice;
5. task and company context.

### Safe automatic learning

Automatic learning is a batch pipeline, never a model call per message.

For each supported mailbox, capture a Sent stream under the authenticated
connection's user identity. Gmail can filter its existing history stream by
direction/label. Graph needs an independent `sentitems` delta cursor. IMAP needs
special-use `\Sent` discovery or an explicit folder selection, with its own
UIDVALIDITY and UID cursor.

Before creating a voice source, extract only the user's authored contribution:

- remove transport headers and metadata;
- strip quoted history, forwarded blocks, and the recipient's turns;
- detect and remove repeated signatures, legal disclaimers, and automated
  footers;
- ignore attachments and generated templates unless the user explicitly opts in;
- run personal/sensitive-mail exclusion before retaining the body;
- store provider/message reference, content hash, extractor version, register,
  and occurrence time for idempotency and later reprocessing.

For an excluded personal message, retain only enough metadata to explain the
exclusion and deduplicate it; do not keep the body or send it to a model. A user
can exclude domains, labels, recipients, and individual samples. Disconnecting
stops future learning. A separate clear action removes retained corpus under the
normal privacy lifecycle.

Corpus statistics update immediately. A profile becomes `stale` when its active
version does not cover the current source-set fingerprint. When automatic
learning is enabled, queue one bounded rebuild weekly or after a meaningful
threshold such as 2,000 high-quality new words and ten messages. These exact
thresholds belong in the upstream contract and configuration, not scattered in
code.

Every automatic build creates a candidate immutable version. Ordinary
high-confidence refinements may activate automatically with the previous version
available for one-click rollback. Material drift, a new inferred prohibition, or
a conflict with human instructions stays a candidate and asks the user to apply
it. The last known-good version remains active on timeout, budget stop, invalid
output, or quality-regression failure.

A weekly delta answers: what sources were added or excluded, how many words and
registers changed, which measured traits moved, which profile rules changed, why
the new version was or was not activated, and what it cost.

### Learning from edited drafts

All generated drafts record the active profile version and a protected copy of
the AI original. If the user edits and sends it, store the final own-authored
version as a gold-pair signal. Rejection and explicit feedback are separate
signals; a rejected draft is never treated as positive style evidence.

Only significant real rewrites qualify. Dedupe per draft, detect copied quoted
content, and require at least five consistent examples before promoting a
generalized transformation. A change such as “replace abstract claims with a
specific observation” is useful; copying the customer's name or deal topic into
the profile is not. Human preferences can promote, edit, or retire a learned
rule.

This signal layer can affect new drafts immediately when confidence is high, but
durable profile changes still pass through the versioned batch builder and its
quality gate. That avoids poisoning the profile with one accidental edit.

### My Voice management

Add a user-owned **Settings → My voice** page. It shows:

- active quality/status, word count, registers, last build, next automatic
  refresh, and current version;
- human identity, personality, and hard preferences in an editable section;
- evidence-derived traits with supporting source references;
- sources by kind, date, weight, inclusion state, and exclusion reason;
- include/exclude/weight controls and a privacy-safe corpus-clear action;
- automatic-learning and weekly-summary controls;
- version history, diff, rebuild, candidate apply/reject, and rollback;
- a side-by-side sample draft and simple feedback controls;
- recent learning deltas and build failures with corrective actions.

“Redo” means build a new candidate from the current corpus. It does not erase
human preferences or history. “Start over” is a separate destructive action that
clears derived data and corpus after explicit confirmation. Rollback creates a
new forward version that copies the chosen artifact, preserving append-only
history.

The voice is private to the user. Workspace administrators may see operational
health and opt-in status but should not read another user's corpus or derived
profile without an explicit delegated capability. The current team/workspace
scope/read posture should be tightened upstream before exposing this UI.

## Technical design

### Durable records

Retain `voice_profile` as the current pointer and human-control record. Add:

- `voice_profile_version`: immutable artifact, structured profile, stats, model
  and builder versions, source-set fingerprint, source word count, build reason,
  predecessor, activation decision, and timestamps;
- `voice_build`: queued/running/succeeded/failed status, stage/progress,
  requested reason and actor, input fingerprint, budget/usage, result version,
  and safe failure detail;
- `voice_learning_signal`: draft outcome, original/final references, normalized
  transformations, confidence, and active/retired state;
- `voice_profile_delta`: the deterministic per-version/weekly change summary.

Extend `voice_corpus_source` with origin, occurrence time, content hash,
extractor version, register, optional activity/message reference, and exclusion
reason. Raw own-authored content remains protected and is never returned through
the normal manifest API.

The build takes a bounded snapshot of included source IDs and their fingerprint,
then releases the transaction before calling a model. Completion atomically
writes the immutable version, delta, audit/event records, and current pointer.
Its idempotency key includes profile, source fingerprint, and builder version.
New sources arriving during a build remain visible as later staleness; they do
not mutate the finished result or keep a database transaction open across model
I/O.

### Builder pipeline

1. Normalize text, remove duplicate boilerplate, and calculate deterministic
   stylometry overall and by register.
2. Select a bounded, stratified sample across kind, register, and time. Weighting
   may influence selection but cannot eliminate diversity.
3. Ask the configured `voice_build` task for a strict JSON-schema result. Every
   inferred claim cites sample IDs; real example text is selected by code rather
   than generated by the model.
4. Validate the schema, evidence citations, quoted snippets, forbidden fields,
   and instruction precedence. Reject unsupported claims.
5. Compile both a structured artifact for product consumption and a readable
   Markdown projection for inspection. Preserve `personality_md` independently.
6. Run held-out voice and anti-AI evals. Store but do not activate a candidate
   that regresses below the acceptance floor.
7. Commit the immutable version and publish the ratified events through the
   outbox.

The job runs through River with retry classification, budget checks, stale-job
recovery, and the last good version always available. Source ingestion itself is
cheap and never waits for the model.

### One drafting consumer

Replace the duplicated generic email-draft paths with one compose-owned drafting
orchestrator injected across module boundaries. It loads:

- task facts and bounded company context;
- the authenticated user's active voice version;
- the latest high-confidence correction signals;
- the global anti-AI policy.

It generates, fact-checks, runs the voice critic, executes deterministic
anti-tell checks, and records the profile version on the draft. HTTP, MCP,
automation, and later channels must call this same service; a second prompt path
would immediately cause behavioral drift.

## Contract and API work first

Reconcile these decisions in `margince-foundation` before source implementation:

- provisional starter threshold versus the current 4,000-word gate;
- opt-in batched automatic rebuild semantics and activation/drift policy;
- user-private read scope and administrator metadata-only visibility;
- immutable version, rollback-as-forward-version, build job, and delta records;
- exclusion reason, own-text extraction metadata, and retention/deletion rules;
- voice and build events in the closed event catalog;
- draft-version binding and edited/rejected feedback semantics;
- exact build/versions/deltas endpoints and 202 job polling contract;
- cross-provider Sent-folder capture obligations;
- acceptance thresholds and cost/budget posture.

Recommended resource shape:

- current own profile read/create/update;
- source ingest/list/update, including exclusion reason;
- create build and read build status;
- list/get versions, apply candidate, and rollback;
- list deltas and learning signals at a privacy-safe level;
- clear corpus/profile and pause/resume automatic learning.

The generated contract files remain generated from `backend/api/crm.yaml`; no
hand edits belong in generated Go.

## Delivery sequence

Each phase is a separate reviewable PR and keeps the existing merge gates green.

### Phase 0 — Ratify the product contract

Resolve the contradictions and pin the records, endpoints, privacy posture,
events, thresholds, automatic activation policy, and acceptance catalog in the
spec repo. Mark the current feature status honestly.

Exit: the builder and automatic learner can be implemented without inventing
behavior in source.

### Phase 1 — Versioned builder substrate

Add immutable versions, durable build jobs, fingerprints, deltas, exclusion
reasons, the structured builder task, deterministic analyzer, validation, River
execution, and build/status/version APIs. Preserve the current source-ingest
strengths and human personality separation.

Exit: a test corpus can produce a validated, inspectable version; failure leaves
the old version active; rebuild and rollback are durable and idempotent.

### Phase 2 — Real onboarding

Replace simulated counts and timers with real profile/source/build APIs. Add
honest paste/upload/speaker flows, server-resumable state, starter/established
quality labels, actual results, and reload/OAuth recovery.

Exit: a new user can build, skip, resume, or recover from failure, and the result
survives another browser and session.

### Phase 3 — Voice-aware drafting and anti-AI gate

Introduce the shared drafting orchestrator, active-version binding, EN/DE
anti-tell linter, critic/rewrite loop, deterministic final check, and draft
provenance. Migrate existing generic email-draft consumers onto it.

Exit: the same grounded request produces a measurably closer draft than generic
AI copy, and prohibited AI patterns cannot leak from a prompt-only failure.

### Phase 4 — Sent-mail corpus growth

Add Graph Sent Items and IMAP Sent-folder sync, harden Gmail direction filtering,
implement own-authored-body extraction and personal-mail guards, and ingest
idempotent per-user sources at capture time.

Exit: provider conformance tests prove only the connected user's authored sent
text enters the corpus; quotes, counterparties, personal exclusions, and
cross-user data do not.

### Phase 5 — Automatic refresh and My Voice

Add threshold/weekly scheduling, candidate activation policy, learning deltas,
pause/clear controls, full source management, rebuild, version diff, rollback,
and Settings UI.

Exit: a user can explain every current rule, see what changed automatically,
undo it, or stop future learning without support intervention.

### Phase 6 — Gold-pair learning

Capture sent edits and rejections, normalize repeated transformations, add
confidence/poisoning controls, and surface proposed corrections for user review.

Exit: repeated user corrections improve later drafts while one-off edits,
recipient text, and rejected drafts cannot silently rewrite identity.

## Quality gates

### Deterministic and unit coverage

- stylometry and register segmentation on fixed fixtures;
- own-authored-body extraction across replies, forwards, signatures, disclaimers,
  HTML/plain alternatives, and EN/DE mail formats;
- anti-AI detectors with calibrated positive and negative cases;
- structured profile validation and evidence citation checking;
- stratified sample selection, fingerprinting, and idempotency;
- gold-pair qualification and poisoning controls.

### Database and integration coverage

- profile/source/build/version RLS and owner-only content access;
- atomic version activation, rollback-as-forward-version, and failure recovery;
- concurrent source arrival during a build;
- source-reference deduplication and extractor-version reprocessing;
- audit/outbox coverage for every mutation;
- capture-to-corpus behavior for Gmail, Graph, and IMAP Sent streams;
- personal-mail and cross-user isolation with real PostgreSQL.

### Frontend and end-to-end coverage

- real counts, build progress, failures, reload resume, and OAuth return;
- thin/starter/established language that never overstates quality;
- source exclusion, rebuild, candidate apply/reject, rollback, pause, and clear;
- onboarding-to-draft and Settings-to-redraft flows.

### Voice acceptance evaluation

Use consented, de-identified multi-user fixtures with a held-out set. For each
user and register:

- compare generic and voice-guided drafts in randomized blinded A/B evaluation;
- require improvement in author/independent preference, not only a model score;
- measure opening, closing, directness, sentence rhythm, punctuation, and
  vocabulary distance from held-out writing;
- require anti-AI tell rate at or below the user's real-writing baseline;
- require zero introduced factual claims and unchanged grounded facts;
- test sparse, multilingual, highly formal, terse, and deliberately
  unconventional voices;
- reject activation when the new candidate regresses beyond the pinned floor.

## Definition of done

The voice step is done only when all of the following are true:

- onboarding creates a real per-user starter profile from real inputs;
- progress, counts, results, and completion are server-backed and resumable;
- generated email uses the active profile and records its version;
- universal anti-AI rules are mechanically enforced, not just prompted;
- sent-mail learning works for every supported provider and retains only the
  user's authored contribution;
- automatic improvement is batched, versioned, explainable, reversible, and
  opt-out;
- users can edit preferences, manage sources, rebuild, compare, roll back, and
  clear their data in My Voice;
- quality evaluation proves improvement over generic copy without factual,
  privacy, or cross-user regressions.

Until those conditions hold, the product should describe the existing backend as
voice-corpus/profile infrastructure, not a completed voice-learning feature.
