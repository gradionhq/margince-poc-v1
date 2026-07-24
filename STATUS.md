# Status — where this stands and where to pick up

> The pickup record for this implementation. Whoever works here next
> (human or agent): read this first, then [AGENTS.md](AGENTS.md) for the
> binding engineering rules. Update this file at the end of every working
> session. The durable, detailed record is git history — this file stays
> a concise snapshot of current state and open work, not a session log.

## Where this is

Margince's **WP0 foundation + WP1 core spine** are built and green:
schema, contract pipeline, auth, core CRUD, the event bus, RBAC, the
governed MCP/agent surface, the transport-agnostic autonomy gate, the
approval engine, two-record merge, and the Vite/React web UI. The full,
current inventory of built surface is
[README.md → *What works today*](README.md#what-works-today); what is
deliberately still stubbed (answering explicit 501) is
[*Deliberately not here yet*](README.md#deliberately-not-here-yet).

The merge gate (`make check`), the real-Postgres integration lane
(`make test-integration`), and the live-boot job are all green.

## Recently landed

**Rate-proposal precondition + supersede (#225, `fix/rate-proposal-precondition`).**
The three deferred P1s from the rates-refresh review, fixed on BOTH refresh
paths (fx and model-cost): a proposal now carries the prior value it was
diffed against and the apply effect re-reads inside the redeem-and-apply
transaction, refusing with `ErrVersionSkew` when the sheet moved (the
approval stays approved-unconsumed; the remedy is a fresh refresh); staging
gained a logical-identity mode (`approvals.StageInput.Identity`) so a fresher
diff force-expires the stale pending proposal for the same currency/model
instead of competing with it in the inbox; and producers diff against the
effective-as-of-today rate (`ListEffectiveFxRates` / `ListEffectiveModelRates`),
not the sheet head, so a future-scheduled row neither masks nor manufactures
proposals. No contract, migration, or status-enum change — supersession is
forced expiry with the audit row carrying the survivor.

**The Rates & costs editor (Phase 1, `feat/rates-costs`).** Admin/ops can now
view and update the two effective-dated price sheets — `fx_rate` (deals) and
`ai_model_rate` (ai) — from a new Settings "Rates & costs" tab. Strict
append-forward: `effective_date` defaults to today, a past date is refused, a
same-day write corrects in place, and there is **no delete** (a past-dated row
is immutable — it prices historical rollups and AI calls). Two new admin/ops-only
RBAC objects (+ a `0116` JSONB backfill for existing workspaces); four
human-admin-only endpoints (`GET/POST /fx-rates`, `/ai-model-rates`,
`x-agent-access: human-only`); prices speak USD/MTok on the wire, µUSD in the
store; both writes are audit-only by ratification (EVT-NOEVT-3 — the closed
event catalog has no fx/ai-pricing stream, the product rate-card precedent).
Craft + security reviewed (1 craft blocker fixed: no build-invented event type;
security clean). **Contract-first flag (P3):** these endpoints, and the Phase-2
`rate_extract` task + proposal kinds still to come, do **not** exist in the
upstream `margince-foundation` spec (whose posture is "operators edit rows
directly") — raised for upstream reconciliation, not a silent divergence.
**Phase 2 (async AI refresh) is built too.** Two admin-only "Refresh from
sources" endpoints enqueue async River jobs: BOTH producers now fetch a
configured page via `webread` and AI-extract with the `rate_extract` task
(**certified for Gemini** — reliability 1.00). The FX producer reads *any* rates
page (no longer a fixed JSON-API shape — the `fxsource` client is deleted),
extracts the pairs it states verbatim, and anchors each to the workspace base in
Go with `big.Rat` (base-as-to direct, base-as-from inverted, cross-pairs
dropped); the model-cost producer extracts per-model prices. Both diff against
the rate in force **today** (not the sheet head), carry the prior rate as an
`expected_prior_rate` precondition the apply effect re-checks, supersede a stale
pending proposal on a fresher diff, and stage 🟡 `fx_rate_proposal` /
`ai_model_rate_proposal` approvals (registered in `approvals/authority.go`);
approving applies through the Phase-1 write path (edit-before-approve works).
Sources live in the deployment config's `rates:` block (`fx_source` is now a
page URL + a provider→url `model_pricing` map); absent config = honest no-op.
Caveat: the `fx_source` default (api.frankfurter.dev) returns EUR-based rates
with no query params, so a non-EUR-base workspace should configure a
base-appropriate rates page.

**The conversational onboarding is now THE onboarding; the classic
wizard is deleted.** Onboarding is ONE Margince conversation. Landed:
the corpus honesty layer (server speaker preview, kept-vs-discarded
ingest stats, diarizer/timestamp transcript parsing — only the owner's
words ever count), the conversation primitives (pure act/phase machine
with run-correlated events, poll-delta narration with a paced queue,
thread/entry components), the conversational COMPANY act (narrated site
read, deterministic clarify questions whose answered option is
server-verified before it authorizes exactly that change, the proposal
read, the in-thread confirm card), the voice act (upload-in-chat,
speaker question, build narration), the results/connect acts, and
restore (wizard-state `path` is THE member signal). Phase 6 flipped the
default: `OnboardingScreen` renders the conversational shell
unconditionally (the `conv` flag and its plumbing are gone), and the
superseded stepper coordinator, Footer, VoiceStep, and ConnectStep
wizard wrapper were deleted with their tests and i18n keys.
`screens/onboarding.tsx` now holds only the shared vocabulary (draft,
URL, wizard-state, corpus constants) with the surviving shared
components split into `onboarding-company-form.tsx`,
`onboarding-manual-interview.tsx`, `onboarding-results.tsx`, and
`onboarding-connect-panels.tsx`; the pinned invariants that survived are
re-tested through the conversational surface. Outstanding: Phase 7
polish (RevealText, orb choreography, reduced-motion audit) per
`~/.claude/plans/while-you-are-waiting-snug-horizon.md`, and the
upstream spec raises (4,000-word onboarding gate decision;
conversational re-pinning of AC-onboarding-*).

**The CI integration lane is sharded per test across twelve runners.** The
single-runner lane took ~6.5 minutes and floored at `compose/integration`
(minutes of serial tests), so package-level parallelism could not shorten
it further. `INTEGRATION_SHARD=k/N` in
`scripts/test-integration-parallel.sh` now runs a deterministic round-robin
slice of every package's top-level Test functions via `-run`; discovery is
static, allowlists lone build tags (`integration` in; the opt-in lanes
`e2e_llm`/`livesmoke` skipped exactly as the compiler skips them), and
fails loudly on any other constraint. Each shard proves it ran exactly its
assigned slice, and the `integration` fan-in job — same required-check
name, so branch protection is unchanged — runs
`scripts/test-integration-reconcile.sh` to prove the slices are complete
and disjoint against one discovery before merging the binary coverage pods
(shards plus the new unit-coverage job) into the `coverage.out` SonarCloud
reads. Slices are count-based; `INTEGRATION_JOBS=16` per runner (the lane
is DB-bound, not core-bound) removes the heavy-tail straggler that
count-based slices dealt at 8 and 12 shards, and twelve runners stay under
the org's concurrent-runner ceiling that queued shards at sixteen.
Measured: backend PR wall-clock ~8m → ~5m, the lane itself 6m30s → 3m20s.
Rounded out by two fixes the fleet surfaced: a gate-binaries cache hit now
skips `make tools` in deterministic-gates (~40s — `go install` re-proves an
existing binary against a cold build cache), and the compose Postgres
healthcheck probes TCP instead of the unix socket the entrypoint's
temporary first-boot server also serves (twelve fresh first-boots per push
turned that latent race live).

**Voice DNA became a working engine, consumed by drafting, with the impress
surface.** The queued `voice_build` row finally has an executor: a River
worker claims it crash-safely (snapshot → extract → evaluate → activate,
started_at-fenced terminal writes on a detached context), derives the
artifact through one stylometry-grounded model pass whose quoted signature
moves must appear verbatim in the exact corpus snapshot, and scores the
candidate against held-out samples — real `evaluation_json` replaces the
placeholder constants, regressions and material drift land as
review-required candidates, budget exhaustion defers to the router's own
window, and a starter corpus too small for held-out scoring activates
honestly as the starter voice (first build only). Reply drafting consumes
the actor's active profile (personality doc first, up to two verbatim exemplars,
stats as negative guardrails) behind the deterministic EN/DE anti-AI floor
with one critic retry and a clean plain fallback that records a rejected
learning signal; the draft response stamps `voice_profile_version` +
Art. 50 disclosure. Both the onboarding success card and Settings → Voice
render the structured insights (thinking pattern as the headline, signature
moves with the user's own quoted words, cached sample drafts with the
draft-only pill, what-to-add-next guidance), and the settings screen gained
candidate review, version history with rollback, the delta timeline, the
learning counters, and a band-drop warning before source removal.
Deferred to the next arc: automatic learning (sent-mail corpus capture, the
auto-rebuild sweep). Spec reconciliation to raise upstream: the code's
800-word build floor vs ONBOARD-PARAM-5's 4,000; the `ADR-0066` citation in
`voice_constants.go` names an ADR absent from the spec repo; VOICE-WIRE-N-1
still says no voice wire ops are pinned while 22 shipped; the pinned
`held_out_prompts` const 5 cannot express a smaller actual run.

**Conversational Margince AI workbench with exact run transparency.** The
website-assisted company setup now presents Margince as a persistent,
professional collaborator: a compact Core header identifies the configured AI,
the exact provider-served model(s), calls, tokens, latency, and estimated USD
provider cost for the current research dossier. Cost is computed from the same
effective-dated model rates and canonical four-bucket pricing used by AI usage;
missing rates are shown as unpriced rather than as a false zero. The research
stage separates conversation from evidence, supports grounded follow-up
questions, and presents cited field suggestions as a reviewable artifact that a
human must explicitly apply to the draft. The reusable workbench component is
small enough for later AI-assisted product surfaces. Backend and frontend tests
cover grouping, pricing, citation binding, model disclosure, conversation, and
apply-on-approval behavior; the fully styled Storybook state passes automated
accessibility checks. Review hardening keeps the conversation to eight bounded
turns, requires every suggested dossier change to carry evidence that contains
its value (or a value the administrator stated), distinguishes configured from
provider-reported model identity, and reports terminal-call latency without
double-counting retries. The responsive workbench, localized empty states,
keyboard/IME behavior, reduced motion, long messages, and citation identity are
covered by the 610-test frontend lane; `make check` and all 18 real-Postgres
integration packages pass with zero skips. A cold-start browser regression is
also covered: the detailed authenticated model profile no longer collides with
the smaller public login-profile cache, and explicit requests to suggest an
interpretive field such as the ICP produce a cited approval card. Synthesized
recommendations are limited to relevant dossier evidence; legal identity,
registered address, and VAT/register values remain exact-evidence-only.

**Unified conversational Company onboarding and live Margince workspace.** The
visible wizard is now Company → Voice → Results → Connect; the separate Review
screen is gone. Website research, one-question-at-a-time manual collection,
live discoveries, the proposed company profile, corrections, legal-entity
choice, and confirmation share one responsive workbench. The right-hand
artifact fills while the crawler runs and keeps legal identity, address and
register/VAT data ahead of offer, products, ICP, pains, outcomes, positioning,
history, industry and sales motion. Confirmation saves directly and advances
to Voice. A typed, bounded company-conversation endpoint covers both modes;
ordinary/status/off-topic replies cannot smuggle proposed changes, while an
explicit correction or recommendation remains evidence-checked and
human-approved. The regression phrase “Does this work?” now returns a factual
first-person status response with no apology or mutation.

The reusable Core/workbench header separates the complete configured model
bindings from the provider-served models actually used for this task, and shows
the cumulative calls, tokens, terminal-call latency, estimated USD cost and any
unpriced calls. The authenticated detailed AI profile uses the same
operational-configuration grant as AI call and usage telemetry; the anonymous
assistant profile remains deliberately minimal. A browser cold start against
`gradion.com` streamed from 1 to 40
pages, surfaced five intact legal entities and 110 cited details, produced the
offer and ICP, answered an ordinary chat message correctly, saved the chosen
German legal entity, and advanced directly to Voice. That pass also exposed and
fixed the ingestion regression: a single `http.Client` page timeout was being
misread as the crawler's global deadline. Page timeouts now record that page as
unreadable and discovery continues; the irreplaceable seed and sitemap each get
one bounded transient retry, with localized company/legal probes as fallback.

**Website-ingestion quality and the Core research stage.** The onboarding
read was benchmarked against Stripe, Notion, Linear, Personio, DeepL, Celonis,
Contentful, Forto, GetYourGuide, and Miro, then tuned against the same corpus.
The systemic failure was page selection: any URL containing `legal` became an
imprint, policy libraries consumed most of the 40-page budget, and the one-shot
company profile fired after twelve pages even when all twelve were legal. Legal
identity paths are now narrow and path-shaped, the crawler probes publisher and
regional legal routes plus one bounded policy fallback, guide/template slugs no
longer masquerade as Team/Product pages, the profile waits for commercial
evidence and takes a kind-diverse corpus, and home/about pages can state
offerings and markets. The legal census folds punctuation-only company variants
and bare brand aliases; it carries a legal block across one safe passage
boundary and reuses already-gated single-entity address/register fields. Focused
live proofs recovered the full name/address/register blocks for Celonis SE,
Contentful GmbH, and Forto SE; Linear's policy-only contracting entity is
recovered without inventing an absent address. Personio consistently returns
HTTP 429 to the root and legal notice, so that site remains an honest failure;
Notion's commercial profile is strong but its unlinked, unique-slug German
imprint remains undiscoverable without a search-engine dependency.

The run also found and fixed the apparent forever-reading failure: a commit
could discover new links in the same wave that hit the byte/deadline cap, then
the skip reporter indexed the new queue with the old selection bitmap. The
boundary is regression-tested, and the worker ownership boundary now closes a
dossier as failed on any future unexpected panic instead of leaving a zombie.
The onboarding Core is a vertically centred research stage with a live progress
halo, legal → offer → customer track, grounded counters, ambient field, and
first-person English/German copy. Browser passes covered intro, URL entry, live
progress and completed evidence; `make check` and the 18-package real-Postgres
lane pass with zero skips.

**Cold-start dev stack, the machine sweep, and the legal-entity census
(#151, #156).** Three things that were wrong together.

`make dev` seeded the demo workspace on every boot, so the state a developer
worked against was never the state a first customer sees — onboarding and empty
states were permanently skipped. It now boots the installation the api
bootstraps from `config/margince.yaml` and nothing else; demo data is the
explicit `make seed-dev`, and `make dev-fresh` rebuilds the database when a
previous session left data behind.

It also **sweeps**: every margince api/worker/vite on the machine — recorded,
orphaned, or from another checkout — is killed, anything holding the port is
evicted, and stray `margince_dev_*` databases are dropped. The app now owns
`:8080` (the api sits behind it on `:18080`, with `/v1` and the probes proxied
through), because `localhost:8080` used to answer `404 page not found` and that
is the first thing anyone types. Two review rounds hardened it: port scans match
LISTENERS only (`lsof -ti tcp:` also lists clients — the sweep could kill the
developer's browser), recorded pids are re-verified before being killed (PIDs get
recycled), and the database sweep runs after `db-up` (it silently did nothing on
a stopped stack).

**The site read now reads what a company sells.** A 40-page read produced 315
"facts" that were mostly not facts: UX methods listed as services, and vendors
(Temenos, Mambu, Kong) recorded as products the company sells — while not one of
the eight Solutions its own navigation enumerates appeared. The cause was
upstream of the prompt: `classifyKind` had no keyword for "solution", so the
index that lists the offerings ranked below every leaf detailing one of them, and
the crawl hit its cap on leaf pages and six translations of the imprint. The
ranker learns the taxonomy and prefers an index over its own leaves, the fetch
wave shrank so discovery can correct the order (which also makes the progress
counter move), the legal-locale bypass is bounded, and the extraction states the
two rules it was missing — name offerings at the level they are sold, and a
platform made by someone else is `technology`, never `product`. Facts are curated
to `identity.MaxSelectedFacts` by bands, since the confirm step preselects every
one of them.

**The legal identity a site states is kept, and the human picks.** A group's
legal notice states one block per entity; the read refuses to guess which one an
installation is, which was right, but it also discarded the blocks — leaving a
human to retype what the page already printed. The census now carries each
entity's registered address and register number (migration `0112`), the contract
exposes them, and the confirm step offers them: one click fills the three fields.
They are marked as the human's input, because that is what confirmation records —
binding the census entry server-side, so the "read from site" label would be
true, is the follow-up. The grounding rules took three rounds to get right: a
detail is judged against the entity's own cited block, by whole contiguous
tokens, so a truncated identifier (`1234` against `HRB 123456`), a sibling
block's address, and one recombined from unrelated tokens (`HRB 24114`) are all
refused.
**Legal-first Margince Core onboarding** — the post-login company setup now
continues the Core presence introduced at authentication instead of falling back
to a conventional form. The Core first explains why organization context is
needed, then offers either a website-assisted read or a one-question-at-a-time
manual interview. Both paths lead with the legal identity (display and legal
name, registered address, register/VAT/UID details, industry and history), then
cover the offer and products, ICP and buying center, customer pains and outcomes,
and sales signals and motion using the existing onboarding contract fields. Live
website phases, page and finding counts, budget deferral, partial coverage and
failures are spoken inside the Core; cited evidence and final human confirmation
remain outside where dense details are legible. The orbital presence is now a
reusable design-system component for smaller product surfaces. English/German
copy, reduced-motion behavior, Storybook states, and the full frontend suite
cover the flow. The deep crawler probes legacy `impressum.html` pages, keeps
the richest locale variant of each legal entity without collapsing distinct
register numbers, and preserves website evidence when an administrator chooses
one entity from a multi-company imprint. A live cold-start browser pass against
`gradion.com` read 40 pages, presented five legal entities plus ten profile
fields and 100 cited facts, and persisted the selected name, address, VAT ID,
offer, ICP, positioning, pains, outcomes, history, industry, and sales motion.

**AI cost pre-flight estimation — the cost hand-off is complete (ADR-0068/A114,
phase 2 of 2).** Phase 1 priced actuals (`/ai/usage`); phase 2 fills the backfill
preview's money figure. For N messages the preview now shows a data-driven priced
cost, factored per task into per-unit cost (from `ai_call` served rows grouped by
`(task, tier, provider, model_id)` over a 7-day window, priced with phase-1's
`PriceCall`) × expected units (from `capture_backfill` yields), **priced at the model
that will run** — served-if-bound, else the slice's own tier's current binding keyed on
`ai_call.tier` (never the ladder head), so a rebind re-prices instantly. Classify counts
per labeled message (`activity.capture_labeled_at`), enrich per person, embeddings per
entity. Unpriced ⇒ cost **suppressed** (never a silent 0); a new additive
`estimate_quality` (`observed`|`heuristic`) labels the source; cold-start uses a priced
work-shape floor (retiring `estTokensPerMessage = 900`). Pure read + `compose/costestimate`
plus one additive index migration (`0111`, indexing the synchronous
`activity.capture_labeled_at` count); cost stays transparency, never a gate. A latent embed-lane gap
was fixed in passing (`routeMeta[TierEmbedLane]` was never populated → embed `ai_call` rows
carried empty provider/model; now folded in at both router constructors). **Two follow-ups:**
`capture_backfill.people_created`/`organizations_created` are not yet written by the backfill
loop, so enrich currently floors (honest `heuristic`) instead of pricing per-person (also
leaves the backfill *status* payload's people/org counts at 0); and the FE consent screen
renders cost only when `> 0` and ignores `estimate_quality`, so an honest `$0` and the
quality signal don't yet reach the human.

**Margince Core login presence + first-person AI voice** — the login experience
now introduces the built-in AI as a governed participant instead of a generic
product illustration. A responsive, reduced-motion-safe orbital Core visual
reacts to authentication state while readable copy states the real boundary:
Margince cannot use a person's context until authentication succeeds. A new
anonymous, deliberately minimal `GET /assistant/profile` surface derives its
configured/development posture, local/cloud/hybrid mode, and provider names from
the same validated routing decision used at boot; it exposes no model ids,
endpoints, secrets, budgets, usage, errors, organization data, or health claim.
The form remains first on mobile and fully usable when that profile request
fails. English/German copy, Storybook states, backend disclosure/allowlist tests,
and auth regressions cover the change. The normative character, copy, login, AI
runtime, and contract amendments are tracked durably in foundation PR #1126
(`feat/margince-core-login`).

**Voice DNA end to end, and the settings surface it needed (#134, #143, #145,
#147)** — ADR-0066's owner-private, human-only Voice lifecycle is merged
(migration 0107): durable builds, immutable versions, candidate deltas,
apply/reject/rollback, corpus clear, source-driven staleness, learning
summaries, and the seven Voice-stream events. A legacy built profile is
preserved as its first active immutable version and obsolete team-scoped rows
are quarantined. Corpus clear drops `qualifies_as_source` in the same UPDATE
that nulls `final_text`, so erasing a corpus carrying a qualifying
`edited_sent` signal no longer trips `voice_learning_signal_qualifies_check`.

On top of it, the **cold-start Voice step is real**: it was a client-only
simulation (hardcoded preset word counts, a `setTimeout` "build", static
result copy, uploads counted then discarded). It now ensures the profile,
ingests the actually-uploaded/pasted text, creates an onboarding build, polls
the durable row, and renders the real derived artifact, with honest queued /
budget-deferred / failed states. The meter counts only real corpus; the preset
chips are "learns from this once connected" examples, never fabricated counts.
The build button gates at the server's real 800-word floor (it gated at 300,
so 300–799 words clicked straight into a 422).

Settings split into **Your settings** (account, Voice DNA, and the AI tab,
which carries the caller's own agent passports) and an admin/ops-only
**Organization** group (company, users, catalog, privacy, audit). That also
puts the company website-refresh behind the admin-only group. The per-user
**Voice DNA tab** is the "…later in Settings" surface onboarding promises:
derived voice, owner-authored preferences (If-Match guarded), corpus sources,
rebuild. The user-facing `voice profile` → **Voice DNA** rename is finished in
the contract summaries and i18n; identifiers, paths, tables and events stay
frozen.

**Admin user management (#147)** closes the contract's `/users … CRUD
fast-follow`: invite (create with no password + role grant + a single-use
set-password token, mailed when a sender is wired), change role, deactivate,
reactivate, plus `include_inactive` on the roster (admin-only) so a deactivated
member is visible to reactivate. Registers `user.invited`/`user.reactivated`.
Two guards worth knowing: the **last active admin** can be neither deactivated
nor demoted (409), and that check takes a per-workspace transaction advisory
lock, because without it two transactions each deactivating a *different* admin
both pass and commit, leaving zero admins.

**AI cost pricing base (phase 1/2, price-on-read)** — every model call is now
priceable in US dollars without any money logic on the write path. The router,
meter and adapters collect four token buckets (`tokens_in` pinned
cache-inclusive, plus `cached_tokens`, the new `cache_write_tokens`, and
`tokens_out`); cost is computed only on read by joining each `ai_call` row to
the `ai_model_rate` row effective on its day (an fx_rate-style, effective-dated,
per-(provider, model) sheet — new table, FORCE RLS, seeded per workspace with
explicit all-zero rows for local providers so local reads an honest 0). One
four-bucket formula lives in two agreeing places (`PriceCall` and the
`CostReport` SQL). `/ai/usage` now serves per-(day, task, tier)
`cost_est_minor` in USD minor units with `currency: "USD"`; a call with no rate
row for its day is counted unpriced, never a silent 0. The cert lane prices its
runs with the same formula and records four-bucket token means. Cost stays
display-only — the budget guardrail is untouched and token-denominated
(ADR-0067/A113, spec PR margince-foundation#1111). **Phase 2 (deferred, own
plan):** a history-data estimator + pre-flight estimate API (the backfill-preview
money figure, "what would N messages cost") over these accumulated rows.

**AI runtime observability UI** — Settings → AI now leads with the
live usage/budget meter and a keyset-paged call trace over the existing
`ai_call`/`ai_call_payload` records. Admins see economy/queued shell advisories;
trace detail exposes the configured-versus-served identity, attempt ladder,
context provenance, and honest capture-off/no-payload/payload states. Captured
runs export client-side as explicitly unreviewed certification-scenario YAML.
The implementation checklist, manual verification guide, and upstream P3
findings live in `.tmp/ai-observability-ui/`.

**Durable AI budget deferral** — the compiled task contract now distinguishes
interactive from background work and includes the ratified `voice_build` task
with CompanyContext explicitly disabled. At the monthly hard cap, background
calls return a typed next-window deferral before any provider attempt or
`ai_call` trace; interactive calls retain the local-small degraded path.
Website reads persist that decision as `deferred` with safe status detail and
`next_attempt_at`, retain progressive findings, keep their one in-flight slot,
and snooze the same River job without consuming an attempt. Both onboarding and
organization read surfaces show the safe deferral reason and automatic-resume
time; migration 0104 and the real-Postgres lane prove join-before-due,
resume-when-due, and reverse/reapply.

**Cold-start company context — durable knowledge, setup, and refresh (Phases 1–5)** —
the installation's anchor organization is now the normal company record with a
governed, typed context view over canonical identity, confirmed profile fields,
and evidence-bearing facts. Website reading is optional: the progressive
onboarding dossier persists no company data before confirmation, supports a
bound accept-subset, preserves web versus human provenance, and produces the
same company shape as manual entry. The model path now owns one exhaustive
per-task context policy: agent, reply, and offer drafting receive bounded scopes
as escaped user data; extraction, classification, enrichment, embeddings,
brief ranking, and deal health explicitly receive none. AI traces store scopes
plus context fingerprint and cache keys bind the fingerprint, preventing stale
answers after a company edit. Reply drafting is shared across HTTP, governed
tools, and workflows and falls back deterministically without sending. The
five-step Read · Confirm · Voice · Results · Connect UI now presents website and
manual entry as equal paths, progressively reveals grounded website evidence,
supports accept-subset confirmation, and ends with a real confirmed-data reveal.
Per-human server state survives reload/OAuth returns with creator/member routing,
optimistic conflicts, RLS, audit, and the identity-stream
`onboarding.state_changed` event; manual setup needs only company name, offer,
and ideal customer and makes zero external request. Company settings expose the
same canonical anchor with provenance-aware editing and website refresh. Refresh
classifies new, unchanged, machine-changed, and human-conflicting proposals;
human values require an explicit keep, accept, or custom decision in the same
version-bound confirmation transaction. An ordered `off < read < tasks < onboarding`
server capability makes every layer reversible without deleting data. Existing
installations receive insert-missing-only profile provenance rows without website
egress, while first-grounded/confirmation timing, extraction coverage, correction
audits, and exact per-call context byte/token estimates make rollout observable.
`voice_build` is a compiled background task; its product consumers landed with the
Voice DNA arc below. Natural-language search remains dormant until its surface is
ratified.

**AI runtime contract + certification (four phases, one arc)** — the AI
task/tier vocabulary is now a compiled contract:
`backend/api/ai-tasks.yaml` (15 tasks, 4 tiers, execution modes, ladders + budget
posture) generates `tasks_gen.go` and `config/ai-routing.schema.json`
via `tools/gen-aitasks` (drift-gated, like `crm.yaml`) — editing routing
POLICY is a rebuild; binding a tier to a provider/model stays runtime
config. One gate serves every AI call: `--ai-fake` now rides the real
Router (metering, tracing, budget — fake provider only), the DB-less
seam is `ai.NewLocalRouter`/`compose.NewLocalModelPath`, and
`FakeModelPath` is deleted with arch fitness tests
(`TestNoModelClientOutsideTheGate`, `TestOneModelPathPerRole`) keeping
it that way. Tracing moved to the certification grain (migration 0100):
one `ai_call` row per ATTEMPT (retries/degrades/escalations visible,
terminal-only metrics), served-model identity reported from the wire
(`response|echo|configured`, never overclaimed), embeddings traced,
config snapshots hash-keyed in `ai_call_config`, embedding rows aging
out at 90 d. On top sits `compose/aicert`: a scenario corpus
(hand-authored, provenance-attested, ≥1 per task — completeness
fitness-tested), structural checks + a pinned rubric judge
(`cert_judge`, own router, never the candidate's binding), N-odd
cache-off repeats, spec §5 verdict math, and committed JSON records —
`make e2e-ai TASK=x MODEL=prov:model` certifies any binding;
`make e2e-ai-report` prints the matrix. Boot warns loudly on unbound
ladders; `/readyz` names the AI state. A payload trace (`TRACE=1`, on by
default) dumps every candidate+judge request/response — the post-stripper
`ai_call_payload` shape — to a gitignored `.tmp/aicert/*.jsonl` for prompt
tuning. First full-corpus Gemini sweep committed (2026-07-19): of 13 tasks,
6 certified, 2 supported_degraded, 5 not_supported (mostly Gemini emitting
`confidence` as a JSON string where the schema wants a number), and
`offer_draft` blocked — Gemini 2.5's thinking exhausts its 300-token cap
scenario before it answers. The verdicts are an honest snapshot, not a
target to game.

**Email ingestion — from fragment to nightly, every-user pipeline
(ADR-0063, 2026-07-19)** — capture was operationally fragile (one 429
permanently killed a connection) and mail never became a person. It is
now a production feature: connect a mailbox, a bounded backfill fills the
CRM under a preview-before-spend estimate, and a continuous + nightly
pipeline grows it — persons, companies, employment edges, timeline
activities, AI classification and signature enrichment, all deduped
through one resolver. Landed across ten PRs:

- **Sync hardening** (#106): a transient failure never kills a
  connection — the `capture_sync_state` sidecar, the error taxonomy
  (429/Retry-After, unreachable backoff, auth→reauth), the per-connection
  dispatcher; `error` is degraded-and-probed-daily, never a tombstone.
- **Gmail** — one-click connect (#107), the Pub/Sub push webhook (#110)
  with Google **OIDC** token verification (#113, salvaged + credited from
  a duplicate community PR).
- **IMAP** as a standing connection (#112): UID cursor bound to its
  mailbox, vault-sealed credentials, bounded incremental fetch.
- **Bounded backfill** (#117): 3/6/12-month widen-only windows, the
  ADR-0020 estimate-before-spend, per-page cursor commits with honest
  resume, cancel keeps captured rows; the M2 window→estimate→activation
  UI.
- **Auto-create + core AI** (#120): every captured mail ensures its
  counterparty through the **ONE dedupe chokepoint** (PO-F-1/PO-F-2) —
  exact reuses, fuzzy creates-and-records; person + domain-named company
  + employment edge + person-only activity link, owner-visibility until a
  human promotes, punycode/impersonation quarantine, erased addresses
  stay dead (A13); `engagement.reply` (CAP-FORMULA-1) enters the event
  catalog. The §2.8 **classify** batch (commitment/meeting/noise, per-call
  commit, budget-clean stop), §2.9 evidence-or-omit **signature enrich**
  (`person_profile_field`, fill-only-empty, never overwrites a human),
  the DH-EXT-1/2 **dedupe review queue** (+ the M4 screen) executing the
  one merge verb, the CAP-DDL-6 morning **digest** (+ `GET /digest` + home
  card) and `GET /ai/usage`.
- **Manual creates** meet the same chokepoint (#118): exact still 409s,
  fuzzy creates and records the near-match.
- **Microsoft Graph** connector (#119): delta-cursor sync with 410
  re-anchor, bounded backfill, one-click connect — sharing the extracted
  `capture/oauthflow` handshake with Gmail (the OAuth2 flow lives once,
  not mirrored).

The spec package landed first, contract-first, in the sibling
`margince-foundation` repo (ADR-0063 + the capture / people-and-orgs /
ai-operational / data-hygiene chapter amendments); this code is built
from it.

**Deep read v3 — reference evidence + page-parallel lanes (founder
target ≤15 s, 2026-07-18)** — v2's one corpus call hit the output-token
wall (~9k quoted-evidence tokens ≈ 150 s). v3 makes the model *read*
everything and *write* almost nothing: pages are segmented into
numbered passages, the model cites `"e":"s12"` (schema-enum'd — an
uncitable id can't be generated) and Go resolves + verifies the
reference, storing the page's own text as evidence. Extraction is one
compact call per fact-bearing page (fast tier, `site_fact_extract`) +
ONE premium profile call over the top excerpts (`site_extract`), all
OVERLAPPED with the frontier-wave crawl — page calls launch as pages
commit, the profile fires once the identity-dense prefix is in. Live on
gradion.com: **~25 s end-to-end** (360→150→42→25 across the arc; the
remaining floor is gradion's own server throttling the crawl burst —
snappier origins land ~12–15 s), with MORE extracted than ever: 8/8
profile fields, ~200 facts (69 services, 69 technologies, 25 locations),
**11 people** (first roster), 5-entity census → correct abstention.
E2E floor gains duration ceilings + a paraphrase-warning watchdog.

**Deep read v2 — ONE corpus call (founder decision 2026-07-18)** — the
per-page extraction (1–2 model calls per page, ~6 min for gradion.com,
plus a synthesis pass and three cross-page merges) is replaced by ONE
streamed model call over the whole labeled site corpus (~78k tokens for
gradion.com; chunked fallback ≤4 for outsized sites). The no-guess gate
survives intact — every fact re-verified against its NAMED page, and a
new `legal_entities[]` census makes the multi-entity abstention explicit
(gradion.com's five-entity imprint → no legal identity proposed +
warning). Extraction taxonomy v2 adds `company/location` and
`signal/technology` (migration 0088). The crawl bursts (12-wide waves,
committed in order — byte-identical to serial by test; ~10 s, <5 s needs
the pipelined-fetch follow-up), and the dossier now reports live
`phase`/`pages_read` (migration 0089) so the SPA poll shows movement.
Anthropic Complete rides SSE above 8k max_tokens (the API drops silent
non-streaming connections). Extraction routes premium-first
(`site_extract`); for Anthropic the premium tier must be SONNET-class+
(Haiku paraphrases evidence away) — judged by the pinned E2E floor
`make -C backend e2e-siteread` with taxonomy floors (locations ≥ 4,
technologies ≥ 5, offerings ≥ 10, ≤4 calls). Live: gradion.com in
~2.5 min end-to-end, 60+ facts, 3 people, correct abstention.

**Deep-read quality loop — debug CLI + ingestion quality** — the answer
to "12 pages, missing facts, wrong company": crawl caps are now
operator-tunable with raised defaults (40 pages / 32 MiB / 240 s;
`--deepread-*` worker flags), and `worker siteread <url>` runs the whole
crawl→extract→merge pipeline **without the stack** (no DB/Redis/staging)
printing every intermediate — pages, skips, every extracted field with
evidence, every finding the gate DROPPED with its reason, merge
decisions, per-call model telemetry, diffable `--json`. Quality fixes:
the evidence gate now falls back to presentation-normalized matching
(quotes/dashes/whitespace/case — words never forgiven) and reports every
drop instead of silently discarding; the crawl queue is kind-ranked
(impressum/about/team before blog archives; tracking params stripped);
extraction has its own routing dial (`site_extract` task) so its tier is
an `ai-routing.yaml` edit; a site-level synthesis pass reconciles
contradictions across pages (still evidence-gated per named page,
degrades to the merge on failure); and the legal-page override is
hardened (path-depth ≤ 2 authority rule; disagreeing legal pages cancel
the override entirely). Model comparison per site:
`worker siteread <url> --model anthropic:<model>`. Spec reconciliation
pending upstream: the R2 caps (12/8 MiB/90 s) were raised by founder
decision 2026-07-18.

**Website deep read — crawl a company's whole site (PR #103)** — the
generic, powerful ingestion: an async River-queued crawl of a company's
site (bounded — ≤12 pages / 8 MiB / 90s, robots-honored, SSRF-guarded,
discovery deterministic and never model-chosen) that extracts far more
than the cold-start fields — company facts, offerings, market signals,
and team members — through the same evidence-or-omit gate, and stages
every finding as a confirm-first 🟡 proposal. New home
`organization_fact` (closed per-category vocabularies, enforced in prompt
+ schema + DB CHECK); the 11 cold-start fields keep
`organization_profile_field`. Team members become thin, published-only
`site_lead` proposals landing through the capture Sink as segregated
leads (NEVER-8 kept). `POST /organizations/{id}/deep-read` (202 + poll),
`GET .../site-reads/{readId}` reports pages read, pages skipped *with
reasons*, and any early-stop cause. Reused for onboarding *and*
enrichment of any org. Live-verified against gradion.com (12 pages, 40
facts, accepted end-to-end). The live run also caught a defect no fake
could: River's silent 1-min job timeout killed real crawls and the
exhausted context wedged the dossier `running` — fixed with an 8-min
worker timeout + `terminalCtx` (WithoutCancel + fresh deadline) so the
terminal write survives the work's death.

**Website read-back reads the SITE, and the onboarding design fix (PR #101)**
— the read-back now fetches the given page *plus* the well-known
Impressum/legal-notice paths and merges per-page (legal facts prefer the
page that legally states them), so German sites finally ground
legal_name/VAT/registered_address; `display_name` joined the
ColdStartField vocabulary. The fetcher moved to `platform/webread` and
keeps ADR-0006's promise: robots.txt honored (RFC 9309 semantics, named
UA), SSRF-guarded via the socket Control hook. The onboarding company
form was rebuilt on the design-system atoms (it had bespoke CSS that read
as a foreign screen).

**Onboarding first-run — a bare installation lands in a company form (PR #98)**
— a cold-start admin used to land in the main menu on top of a nameless
org; now the app shell gates on `GET /company` (404 = undescribed) and
routes them into a mandatory company step. `PUT /company` is the human's
confirm-first write (the unsaved form IS the 🟡 staged state, marked
`human-only`); `POST /coldstart/preview` pre-fills it without staging.
The anchor org is marked `organization.is_anchor` (0083). Required
identity block (name, legal entity, VAT, address, industry); the step
cannot be skipped.

**Cloud-provider review remediation (PR #102)** — the top-10 correctness
findings from the post-merge review of the cloud model providers (#96):
streams surface failure/truncation terminals instead of clean EOF (openai
`response.failed`/`incomplete`/`error`, gemini mid-stream error objects +
abnormal `finishReason`, applied to `Complete` too), one shared SSE
scanner with a 4MiB line cap, cache keys cover model override + response
schema, `OutputTokens` is reasoning-inclusive on every adapter (gemini
normalized), Responses API `store:false` pinned, `dimensions` omitted on
the generic OpenAI wire, canonical `models/…` ids accepted, vLLM
top-level errors decoded, and `make dev` enables real routing only when
every bound cloud provider's key is present.

**Single-organization installation (ADR-0061/A107, PR #90)** — the
ratified single-org concept, end to end. One installation serves one
organization: bootstrap moved off the public wire into a strict
`margince.yaml` deployment file (`platform/deployconfig`) consumed at
API boot under a pg advisory lock — organization + first admin + system
roles + configurable seeds (pipeline stages, consent purposes, starter
automations, booking page) in one transaction; 0 workspaces → create,
1 → bind, >1 → refuse for an operator-led migration (boot-enforced,
deliberately NOT a schema constraint so the cross-tenant RLS suites keep
proving isolation). `POST /workspaces`, the `{workspace}` subdomain
template, and every tenant selector (`X-Workspace-Slug`, MCP
`--workspace`) are gone; pre-bootstrap requests answer 503
(availability, never auth). The A74 account-recovery pair is live:
`auth_token` (0081), a STARTTLS-required SMTP mailer behind the
`email:` config section, enumeration-resistant forgot/reset (the whole
account-dependent path runs off-request), and `migrate reset-password`
as the operator recovery. Anonymous `GET /auth/capabilities` drives the
login UI, which is now a login-first single column (no signup mode, no
hero, no slug field) with capability-gated forgot/reset screens.
Spec-side: margince-foundation ADR-0061 + DECISIONS A107 + ADR-0043
Amendment 2 (merged there first, contract-first).

**Craft gate de-vendored** — `cli/craft` is now a first-class, locally-owned
part of this repo rather than a hash-pinned vendored copy: the
`craft-manifest.sha256` hash pin, the `craft-drift`/`craft-sync` targets,
and all "vendored / hash-pinned / fix upstream" language are gone (its own
Go tests gate its behaviour). `infra/branch-protection.json` and its
wiring fitness test were retired with it; live GitHub branch protection
remains the enforcement.

**OSS-baseline batch** — this repo is being groomed into the
baseline for the official open-source Margince repository, absorbing the
tooling and gate suite the baseline needs. Merged so far:

- **PR A** — craft gate v3, SHA-pinned GitHub Actions + an image-pin
  gate, `concurrency:` cancel groups, `.env.template`, `make tools`
  bootstrap, `config/ai-routing.example.yaml`.
- **PR B** — `infra/docker-compose.dev.yml` dev stack, the API-driven
  demo seed (`make seed-dev` / `seed-reset` / `verify-boot`), the README
  boot/log-in/verify quickstart, and the `live-boot` CI job.
- **PR C** — gate parity: oasdiff contract breaking-change gate, TS type
  drift gate, test-lane hygiene, zero-skip integration enforcement, the
  new-code-strict golangci arm, and the file-length ratchet.
- **PR D** — frontend RBAC primitives (`useMe`, `RoleBadge`,
  `FieldGuard`, role-aware automations editor) and the design-token
  purity gates.
- **PR E** — OSS-publication sanitization: this STATUS scrub,
  CONTRIBUTING rewritten for external contributors, the README
  internal-narrative scrub.
- **Identity fix** — the public auth paths (`/v1/auth/login`,
  `/v1/auth/logout`, `/oauth/token`, `/oauth/register`) now answer their
  protocol's client error instead of a 500 when the workspace slug
  resolves to nothing, without disclosing whether the workspace exists.
- **Blobstore seam** — `platform/blobstore` (S3/MinIO + in-memory fake),
  the object-bytes substrate behind the `attachment.storage_key` the
  schema already committed to. Ships with its first production consumer:
  the minimal `/attachments` surface (upload/download/list/soft-delete,
  owned by `activities`, authority inherited from the parent entity) and
  the Art. 17 erase-path object purge, so erasure reaches the bytes not
  only the rows. MinIO is in the dev compose stack and both CI integration
  jobs; a `/readyz` probe covers it.
- **Keyvault seam** — `platform/keyvault` (AES-256-GCM local provider +
  in-memory fake), secret-material storage behind an opaque,
  workspace-scoped `credential_ref`. Ships with its first real secret
  migrated: `connector_connection.auth` (bytea) moves off the tenant row
  onto the vault, leaving only a ref on the row (the `auth` column is
  dropped in a later additive migration after backfill). Isolation is
  cryptographic — the ref carries its workspace and the GCM AAD binds it,
  so a stolen ref is inert across the tenant edge; the `vault_secret`
  ciphertext table is operational infra (no `workspace_id`, no RLS), like
  River's tables. `WithKeyvault` feeds a `/readyz` probe; the worker
  backfills legacy rows at boot (idempotent). Env-only root key
  (`MARGINCE_KEYVAULT_ROOT_KEY`, base64 32-byte). The connector port is
  unchanged — capture resolves the ref and still hands the connector its
  `Auth`.
- **Field-history read** — `GET /field-history`: a per-field change
  timeline projected read-time from the audit spine's before/after
  diffs, homed in the privacy module beside the audit-log read. Gated
  exactly like every other record read (human-only + object-read +
  row-scope, activities dispatching through the link-walk); no new
  table or migration — the projection runs entirely off `audit_log`.
  First arc of the poc-1 feature-delta port.
- **Org hierarchy roll-up read** — `GET /organizations/{id}/hierarchy-
  rollup`: a tree or self account roll-up (weighted pipeline,
  current-quarter closed-won, 30-day activity) with RBAC-honest
  restricted-node disclosure and base-currency FX conversion (422 on a
  missing rate, never a silent rate=1). Compose-homed — the read spans
  organization, deal, stage, activity, and fx_rate — with no new table.
  Arc 1b of the poc-1 feature-delta port.
- **Record history read** — `GET /records/{entity_type}/{id}/history`:
  chronological plain-language history lines with actor + agent-authority
  attribution, viewer-masked before/after (by omission), keyset
  pagination, and the erasure boundary (pre-scrub rows withheld, the
  tombstone's own line served); third audit-spine read in the privacy
  module; the erase tombstones now carry their tallies on the evidence
  channel. Arc 1c — closes Wave 1 of the poc-1 delta port.
- **Custom-fields catalog + governed schema-change engine** —
  workspace-defined scalar fields on core objects (create 🟡/rename
  🟢/retire 🟡/picklist options 🟡), a new `modules/customfields` service
  running the one sanctioned runtime ALTER through a dedicated
  boot-optional owner pool (`--schema-dsn`/`MARGINCE_SCHEMA_DSN`, unwired
  ⇒ 501 — see
  [docs/reference/configuration.md](docs/reference/configuration.md))
  with the DDL-first-then-SET-ROLE single-tx dance, cross-workspace
  column-collision 409s, and an AST fitness gate pinning the privilege
  downgrade. Values-on-records parity — reading and
  writing the new fields through the record surface — is the follow-on
  arc, arc 2a-ii.
- **Custom-field VALUES ride person/organization/deal payloads**
  (create/update/read/list, top-level `cf_` keys via the contract's
  x-extension mechanism), the fieldcatalog seam
  (`shared/ports/fieldcatalog` provided by customfields, injected by
  compose), and the first real list-sort implementation — DM-VOCAB-
  aligned single-field sort + typed `cf_` equality filters on an
  extended keyset cursor (sort-fingerprinted, crafted-token-hardened);
  active columns join the vocabulary, retired leave it. Arc 2a-ii
  completes CF-T05's core parity (collections/saved-views cf-awareness
  flagged as follow-up; a merged-away record's cf values stay on the
  archived source row — merge survivorship fill is core-columns-only in
  V1).
- **Formula fields as database-GENERATED artifacts** (RD-T08) —
  `deal.amount_minor_base` GENERATED column + the
  `organization_open_pipeline_rollup` security_invoker view, surfaced as
  gated `computed_fields[]` display rows on the org 360 read (STATE-4:
  key absent without `computed_field:read`, a new read-only-everywhere
  RBAC object); the hierarchy-rollup closed-won and brief SQL adopt the
  column; schema-proof + no-runtime-authoring fitness tests stand guard.
  Closes Wave 2 of the poc-1 delta port.
- **Quotas & attainment (RD-T06)** — the `quota` aggregate (owner XOR
  team, explicit period, human-set target; workspace-shared config
  gated by the new `quota` RBAC object) with full CRUD and the
  server-computed attainment read: Σ closed-won `amount_minor_base` ÷
  base-converted target, decomposed per contributing deal
  (golden-number reconciliation), honest 422s for zero targets and
  missing FX, pace/band derivations on an injected clock. Wave 3 opener
  of the poc-1 delta port.
- **Attachment AI extraction (RD-T05/RD-T10 backend)** — `scan_status`
  gating (`scanning`/`blocked` refuse the download stream with typed
  409s; the module-local Scanner seam has no product, so uploads default
  `scanning`), the evidence-or-omit staged extraction read behind the
  `shared/ports/extraction` seam (NoOp default — honest empty), and the
  compose-orchestrated `extraction:accept` writing an allowlisted set of
  grounded fields onto the deal with per-field audited provenance
  (human-only V1). Closes Wave 3 of the poc-1 delta port.
- **DE/EN offer templates + branded PDF render (offers-depth arc 4a)** —
  the `offer_template` catalog (workspace config, one default per locale,
  name-unique, the two named 409s) with CRUD gated by the new
  `offer_template` RBAC object, and `POST /offers/{id}/render` producing
  a go-pdf/fpdf branded DE/EN PDF (labels driven by the offer's template
  locale) stored to the blobstore as `pdf_asset_ref` — render totals
  equal the server-computed totals exactly (no drift). poc-v1's offer
  lifecycle (send/accept/reject/FX-freeze/totals) is untouched. First
  half of Wave 4; AI-drafted regeneration (delta 1) is arc 4b.
- **AI-drafted offer regeneration (offers-depth arc 4b)** — a
  compose-orchestrated evidence-gated AI draft: on regenerate, the
  mechanical revision-mint runs first, then (when the OfferDraft model
  lane is wired via `--ai-routing`/`--ai-fake`) the orchestrator calls
  the model, keeps ONLY lines whose price + snippet are verbatim-grounded
  in the deal's captured context (drops the rest, never fabricates;
  blank price when ungrounded), stages them via the deals
  `AddStagedOfferLines` seam (excluded from server-computed totals until
  a human accepts), and returns the Art. 50 disclosure + a diff — all
  transient. Secret-stripped model calls; totals never AI-computed; the
  send/accept/reject/FX lifecycle untouched; unwired = mechanical-only.
  This CLOSES Wave 4 and the entire poc-1 delta port.

## Pick up here

Open work, roughly in priority order:

- **Capture quality gates + captured-company auto-enrichment — spec ratified,
  implementation in flight (plan
  `/Users/lars/.claude/plans/yes-and-while-we-crispy-stearns.md`).**
  **Phase 0 (spec):** ADR-0072/A118 authored in `margince-foundation`
  (renumbered from the plan's "ADR-0070", now taken by A116/A117) — the tiered
  creation gate, the `capture_counterparty_verdict` no-payload AI task,
  noise=hide-then-redact, `organization.name_source` authority, and the
  `capture_auto_enrich` setting + daily cap (foundation PR #1184, G1 green).
  **Phase 1 (build, landed):** transactional/ESP suppression (CAP-PARAM-6:
  `capture/transactional.go` — exact-eSLD infra suppresses standalone, prefix
  rules only with List-Unsubscribe/machine-localpart corroboration, PSL/IDNA
  normalization, `capture.transactional_extra`/`_never` config) runs T2 in the
  Sink (person+org suppressed, activity stands, `system_log` breadcrumb); and
  honest org display names (`people/orgname.go` `DisplayNameFromDomain`:
  "gitex.com"→"Gitex") with the `organization.name_source` provenance column
  (0118; capture stamps `'domain'`, a human edit stamps `'human'`). `make check`
  + the full zero-skip integration lane green.
  **Still open (contract-first-gated on ADR-0072):** Phase 4A (the
  `capture_auto_enrich` setting + `/capture/settings`), 4B (auto deep-read
  trigger + auto-apply + sweep), 2a (counterparty identity + disposition
  ledger + deferred creation), 2b (the verdict job + review queue + noise
  disposition), 3 (corroborated signature org-name promotion).

- **Site-read legal census — three known gaps (#162).** `FinishSiteRead`'s CAS
  guards only on `status = 'running'`, so a reclaimed-then-returning worker can
  overwrite the dossier (pre-existing; the finish half now lives in
  `people/sitereadfinish.go`). A VAT group can fold two real companies into one
  census entry, because the dedupe keys on the register number — which is what
  lets a market heading collapse into the entity it labels. And a read whose only
  surviving output is the legal census is recorded as failed, because the
  survivor check ignores `merged.entities`.

- **Conversational Company workspace — baseline implemented; reconciliation open.**
  [The consolidated concept](docs/explanation/margince-conversational-workspace-concept.md)
  replaces the website/manual chooser and separate Review step with one scoped
  conversation: optional live website research, legal-first website-free
  collection, a progressively filled company artifact, corrections and
  version-bound confirmation in place. It also defines abuse controls and a
  reusable `assistantflow` direction proven by onboarding plus company-context
  maintenance. The four-step Company → Voice → Results → Connect baseline is
  implemented. Remaining upstream reconciliation covers the canonical wizard
  description, legal must-resolve semantics, response-intent vocabulary, and
  compatibility contract for the reusable framework.

- **Voice DNA follow-ons** — the lifecycle, the real onboarding step and the
  settings surface are merged (see *Recently landed*). Still open: the
  structured Voice builder, and canonical reply usage/learning (drafts that
  actually consume the active artifact and feed `voice_learning_signal` back).
  Operationally, `voice_build` only completes where its tier is bound to a
  reachable provider in `ai-routing.yaml`; on an unbound stack the build stays
  `queued` and the UI honestly says so, which is easy to mistake for a bug.

- **User administration follow-ups (#147)** — the roster read is first-page
  only (fine for a single-org install, wrong at scale); an invite issues a
  set-password token but delivers nothing when no mailer is configured, and the
  recovery today is the operator path (`migrate reset-password`), so returning
  the link on the response is an open contract-shape decision; and `User`
  carries no per-user role, so the admin control sets a role without showing
  the current one.

- **Endpoints without a caller** — the recurring shape this week (MISSING-UI-V4
  §5/§6/§7 landed as #146/#142/#148; the Voice API and the identity
  deactivate/role methods were the same story). Worth treating as a standing
  check rather than a backlog item: a handler-backed, routed endpoint with zero
  frontend callers is not done, and the gap is invisible from the green gates.

- **aicert follow-ups** (from the certification arc): the
  trace-extraction pipeline (scenarios from production `ai_call` rows
  with a real pseudonymizer — `extracted:` provenance is refused until
  it exists), a certification-badge surface (records are committed
  JSON, ready to `go:embed`), a nightly scheduled lane, deeper corpora
  for the tasks that have only starters, and the §6 upstream spec notes
  (contract file location, verdict rules, served-identity vocabulary)
  to reconcile in `margince-foundation`. Seven tasks in the contract
  (`enrich`, `capture_classify`, `deal_health`, `draft_reply`,
  `nl_search`, `summarize`, `transcript`) have no production call site
  yet — their starter scenarios are documented placeholders.

- **Cold-start + company-context refresh** — all phases (0–5) are delivered;
  the executed state is explained in
  [docs/explanation/company-context.md](docs/explanation/company-context.md)
  (the phase plan lives in git history as
  `docs/explanation/coldstart-company-context-plan.md`).
  Upstream, foundation PR #1104 is merged at `f97ef6b`; ADR-0065/A111 pins the
  anchor/profile/fact/site-read schema, the optional three-field manual path,
  the reusable deep-read wire, the typed context policy, progressive
  budgets/events, and the five-step UI. Downstream, the phases landed as PRs
  #127 (read substrate), #128 (onboarding dossier), #130/#131 (task injection +
  five-step wizard), and #132/#133 (budget deferral + refresh/rollout) — all
  merged; the per-phase delivery narrative is in those PRs.

- **Email ingestion — deferred pieces of ADR-0063** (the pipeline is
  live; these were scoped out, not missed):
  - **Graph webhook (PR-7b)** — the connector is poll-only; the
    change-notification subscription (validationToken handshake,
    clientState, ≤3-day renewal riding the existing watch sweep) is
    unbuilt, so Outlook latency is the poll interval, not the 60s p95.
  - **Microsoft has no onboarding connect UI** — the Graph connector (#119)
    is live, but the wizard's last step still renders a "coming soon" panel
    for Microsoft and offers only skip; Google OAuth and IMAP are real there.
    A first-run Outlook user cannot connect without leaving onboarding.
  - **Graph refresh-token rotation** — Microsoft rotates the refresh
    token on each redemption; the stored original works within its
    ~90-day confidential-client window (active mailboxes never reauth),
    but persisting the rotated token needs a **credential-update seam**
    (Sync surfacing an updated credential for the registry to re-seal) that
    `connector.Connector` does not have — a cross-connector follow-up.
  - **Dedupe undo of a *merged* pair** answers `409 not_undoable` — the
    merge verb's reversibility (PO-AC-M6) is not built; dismissals undo
    fine.
  - **Nightly dispatcher consolidation** — classify, enrich and digest run
    as their own daily River jobs (run-on-start); the ADR-0063 staggered
    coordinator (catch-up → classify → reconcile → enrich → dedupe sweep →
    digest, one ordered pass) is not yet a single dispatcher, and the
    `capture_reconcile` sweep over link-less connector activities is
    unbuilt.
  - **`ai_usage` RBAC object** — `GET /ai/usage` is gated on the
    admin-held `automation:update` permission (no `ai_usage` noun exists
    in the closed RBAC object set); a dedicated object should be pinned
    upstream (spec-repo reconciliation).

- **Overlay branch 1b — the review-deferred hardening** (from PR #91's
  three-lens review; the branch itself ships read + poller sync with the
  human `/v1` surface seam-backed). **Landed (2026-07-21):**
  - **Deletion/archive feed** — MERGED #159 (`fc95b15`). `Incumbent.Deletions`
    + HubSpot `?archived=true` + `MirrorStore.PurgeRecord` (row + assoc +
    visibility + atomic `mirror.deleted` emit, no tombstone) + full-scan
    `ReconcileDeletions` (no watermark — the archived feed is unordered) +
    purge indexes + `/metrics` counter. Spec pin: foundation #1123.
  - **Visibility concurrency + ambiguity** — MERGED #160 (`078a388`). One
    per-workspace visibility advisory lock (`lockWorkspaceVisibility`) taken
    by every visibility mutator; distinct-owner-set ambiguity + late-ambiguity
    revoke; GUC-unset fails closed.

  - **A3 live force-fresh + atomic budget reserve** — MERGED #161 (`fbeea10`).
    Per-request vault-backed `resolveIncumbent` wired into FreshnessReader
    (force-fresh reaches HubSpot per workspace, no longer `inc:nil`); atomic
    `Meter.Reserve` + reserve-before-`inc.Get` (review #56); `ActiveConnection`
    per-workspace read (split into `connectionreads.go`). NOTE the
    `datasource.Freshness` verb still has **no production caller** — A3
    completed the seam; a "refresh"/🟡-action surface that INVOKES force-fresh
    is a tracked follow-up (see the backlog memory).

  - **A4 reconcile robustness (failing-connection backoff)** — MERGED #165
    (`9d9dabe`). `overlay_sync_state` sidecar + `RecordSweepFailure`/`Success`
    (classify + a 2min·2^n ladder capped at 4h *before* ±20% jitter, so
    ~4h48m effective + rate-limit floor) + `DueOverlayConnections` due-gate +
    `reconcileConnection` distinguishing connection-level (abort+backoff) from
    per-object (log+skip) failures.
  - **A5 disconnect-race fencing** — MERGED #166 (`d103080`). Opt-in
    `MirrorStore.WithFence()`: a `FOR SHARE` assert on the active
    `incumbent_connection` row (fail-closed) on every resurrection-risk write
    (incl. `RecordSweep*`), contending with Disconnect's FOR UPDATE so a
    mid-sweep write either commits-then-purged or aborts with
    `ErrConnectionGone`; the sweep + worker treat that as a clean stop. Covers
    the tables the mirror tombstone cannot (associations, checkpoints,
    user-map, sync-state) + a tombstone-less new row. `mirrorcheckpoints.go`
    split out.
  - **A6.1 mapping-fidelity (value-level rules)** — MERGED #173 (`ad905af`).
    OVA-MAP-2 (`hs_call_duration`
    ms→seconds), OVA-MAP-3 (`full_name` assembled firstname+lastname → email
    local part → placeholder, never empty; new `AlwaysEmit` assembler flag),
    OVA-MAP-4 (deal `amount`→`amount_minor` scaled by the ISO-4217 exponent of
    `deal_currency_code`, not a blanket ×100; null when no currency). New
    transforms `uppercase`/`ms_to_seconds`/`full_name`/`amount_minor_by_currency`
    (replacing `amount_to_minor`); golden OVA-AC-4 cases. Spec: foundation
    #1124 (merged).
  - **A6.2 engagement-class split (OVA-MAP-1)** — IN FLIGHT
    (`feat/overlay-mapping-fidelity-engagements`). HubSpot v3 has no generic
    engagements object, so the five classes (calls/meetings/emails/notes/tasks)
    are swept separately — each its own `/crm/v3/objects/<class>` endpoint (the
    old `engagements` class hit a non-existent path) — and each maps to
    `activity` with a FIXED `kind` via a new `Const` mapping-IR field, no
    generic fallback. The canonical→incumbent translator went **plural**
    (`IncumbentClassesFor`): `activity` ← all five, so `backfillCompleteFor`
    requires all five cursors done and force-fresh honestly degrades a
    multi-source type to the mirror. Extracted `transforms.go` (file-length).
    **Reworked against the merged pin (foundation #1131, OVA-MAP-7/8):** the
    activity mirror `external_id` is namespaced `<class>:<id>` (adapter
    produces/strips it; the UUID bridge packs a 1-based class code in byte 7,
    reversibly — fixes the cross-class id collision AND lets force-fresh
    recover the class); the five engagement mappings now carry the owner field
    (were ingesting invisible); task `hs_timestamp`→`due_at` with `occurred_at`
    from `hs_createdate`; the wire projection surfaces `duration_seconds` +
    `due_at`; `size_band` buckets fixed to the contract enum
    (201-500/501-1000/1001-5000/5000+).
  - **A6 remaining slices** (own PRs, structural): OVA-MAP-5 leads via real
    Leads API props + contact association, OVA-MAP-6 null overlay pipeline/stage
    + `raw` + stage→`semantic` for advance-tier.

  Still open in 1b (the next branches, roughly in priority order):
  - **A3b** — token-bucket burst limiter (HubSpot 100–250/10s); shared
    cross-process meter (PG/Redis) so `/overlay/budget` reflects the worker
    poller; **and the force-fresh CALLER** (the surface that invokes the now-live
    Freshness verb) — without it A3's live read is latent infra.
  - **A4b** — the composite keyset watermark for a >10k same-timestamp
    block (the seam can't signal mode-switch — an upstream spike); atomic
    ingest+`mirror.conflict` in one row-locked tx; propagate aggregate/`ctx.Err()`
    to handlers.go's 503 path; derive sync staleness (`syncstatus.go` never
    marks stale).
  - **A5b** — teardown.go's post-commit vault-credential delete isn't retryable
    across a Disconnect retry (inert orphaned sealed blob; branch-1 has no
    reconnect); needs a durable-cleanup design.
  - **A7 assoc/backfill fidelity**;
    **webhook-as-signal** (only WITH portal-id→workspace binding in the HMAC
    basis — the unmounted receiver was deleted, not fixed); a **reconnect flow**
    (Connect refuses a workspace with any connection row) that clears teardown
    tombstones. The nullable `pipeline_id`/`stage_id` overlay-deal contract
    question is reconciled upstream (foundation #1124, merged).

- **Overlay evaluation-window SPA read-subset UX** (partly landed) — the
  overlay mirror serves only a read subset (get-by-id, `q`, cursor,
  `include_archived`); every other list dial (`sort`, `owner_id`, `status`,
  `tag`, `kind`, `pipeline_id`/`stage_id`/`organization_id`, …) answers
  `422 unsupported_in_overlay_mode`, and the context-graph/embeddings surfaces
  hold no mirror data. `GET /me.system_of_record.mode` now signals overlay so
  the SPA gates its list UI. **Done:** the shared `useListQuery`/`ListToolbar`
  lists (contacts, companies, leads) and the bespoke Deals screen (drops the
  refused dials, forces the flat table view, hides the pipeline/filter pickers
  — the stage-keyed board cannot place a zero-UUID-stage mirror deal).
  **Still broken / tracked:**
  - **Tasks** (`GET /activities?kind=task`): `kind` is a *defining* filter the
    mirror cannot honor; dropping it would mislabel all activities as tasks.
    Needs an honest "not available in overlay" state (or client-side kind
    filtering if the mirror carries it).
  - **Related evidence** (`GET /records/{type}/{id}/context`): 404 — branch 1
    builds no context graph/embeddings over mirror content (by design). Needs
    an honest "not available in overlay" state.
  - A full **record-360 panel audit** (timeline, related records, strength,
    etc.) for the same read-subset assumptions, converging on one shared
    "unavailable in overlay" affordance rather than per-panel error states.

- **Deep-read durability-hardening pass** (from the #103 review, deferred
  as cross-cutting rather than rushed per-effect) — the redeem-then-execute
  accept effects (coldstart/scrape/deepread/site_lead) share the ADR-0036
  pattern where a consumed-but-unapplied approval can't be retried; the
  correct fix is transactional redeem+apply at the approvals-framework
  level, repo-wide. Plus: transactional River enqueue (Start→enqueue and
  stage→finish are separate module txns today; `closeUnqueued` is the
  current compensation), and a stale-`running` dossier reclaim/sweeper (a
  crash between Begin and Finish wedges the org's one in-flight slot;
  `terminalCtx` shrinks but doesn't close the window). Recorded in PR #103's
  tracking comment.
- **Website ingestion — upstream ratifications to reconcile** (spec repo,
  contract-first): founder ratifications R1–R5 (well-known-path probes
  within ADR-0006, crawl caps/robots posture, the `organization_fact`
  category home, thin-lead sourcing under NEVER-8) recorded in the #101/#103
  PR bodies; the two-page quick read measures ~13.3s vs ONBOARD-PARAM-1's
  8s p95 (re-pin the budget for the multi-page read, or parallelize once the
  fake client scripts per-page); and `crm.yaml`'s `deepReadCompany`
  description still mentions a `deepread`-vs-`enrich` proposal kind and a
  `budget` stop reason the v1 does not emit.
- **No scanner product + no boot wiring** — new uploads stay
  `scanning`/undownloadable until an admin or test drives
  `activities.Store.MarkScanResult`; no real scanning product is
  integrated anywhere in this codebase (operational gap, poc-1 parity).
  A production deployment needs a real Scanner behind the seam, or an
  admin verdict path, before new uploads are downloadable end-to-end.
- **The RD-AC-2 "every download audited" clause is NOT ported** — poc-v1
  audits only attachment create/archive; a deliberate
  delta from the spec, not an oversight.
- **`extraction:accept` carries no idempotency key on its notes** — the
  deal update and its per-field notes now commit atomically (one shared
  `database.WithWorkspaceTx`, driven via `deals.Store.UpdateDealTx` +
  `activities.Store.LogActivityTx`: a note failure rolls the deal update
  back too), but a client retry on a dropped response still re-applies the
  deal update (last-write-wins, harmless) and duplicates the provenance
  notes — there is no natural key on a note the way capture's
  `(source_system, source_id)` gives `LogActivity` its own idempotency.
- **The 🟡 agent-staged accept path (approvals effect) is deferred** —
  V1 ships human-only; an agent cannot currently propose an
  extraction-accept for confirm-first approval.
- **`RequestAttachmentAccess` is a courtesy-audit-only op** — poc-v1 has
  no restricted-but-disclosed state to actually gate on; the note is the
  entire effect. The final review ruled it a keep (honestly labelled,
  contract parity), not a defect.
- **The extraction read and the accept-write share the raw download's
  scan gate** — `GetAttachmentExtraction`/`extraction:accept` now refuse
  a `scanning`/`blocked` attachment with the same typed 409s before the
  extractor ever sees the bytes (defense-in-depth, RD-T05). Inert today
  under the NoOp/Fixture seams; essential the moment a real extractor
  (riding `modules/ai`) reads unvetted content.
- **§0 baseline ratification** (founder decision): confirm this repo as
  the OSS baseline and reconcile the spec tree with this
  repo's actual architecture. Until it lands, the docs refer to the spec
  as "a separate spec repo" without a literal path; they gain a concrete
  public spec URL once the canonical public spec home is decided.
- **EP05 §B capture-connection reshape** — now unblocked by the keyvault
  seam: multiple per-user connections, the connection-management contract
  surface + UI, and connector credential *rotation* (the ref/AAD scheme
  already carries a key version so rotation is not foreclosed). Its own PR
  arc. The `oauth` signing keypairs (`workspace_signing_key`) fold onto the
  same vault next, as a distinct migration.
- **ADR track** (parallel, each an open call recorded in the PR that resolves it): the
  design-system of record, and the optional advisory LLM craft-review CI
  job. (River shipped in #35, the blobstore seam in the prior batch, the
  keyvault seam in this one. The embedded SPA is retired — the API binary
  serves `/v1` only; the web UI is served separately.)
- **Frontend DECISION items**: router migration and a
  Storybook/component-test lane — adopt when the design system
  stabilizes, not before.
- **Publication mechanics** (founder decision): whether to publish full
  git history or squash-import into the public repository.

Next product arcs beyond the baseline groom live in the spec's build
backlog; route findings as you work — implementation decisions recorded in the
commit and PR that makes the change; spec/ticket defects reconciled upstream
against the spec.

## Cloud providers — upstream discrepancies to reconcile

Filed upstream as `gradionhq/margince-foundation` **#1073** (contract
reconciliation: interfaces.md §4 additive fields, ADR-0020 env-key posture,
`provider: local` naming gap, Mistral alias, richer `model.Message`) and
**#1074** (model-capability catalog incl. embedding dimensionality, = §7 #6).
Per-provider AIUC conformance (§7 #9) and the eval-binding matrix (§7 #4) are
already tracked in foundation #974 / #975 / #976.

Raised by the cloud BYOK model-providers change (generic `openai_compatible`
plus native `openai`/`gemini` adapters). Paths use the **live** foundation
layout (verified against `gradionhq/margince-foundation@main`, 2026-07-17 — the
local sibling checkout is 299 commits behind and still on the old
`specs/spec/…` tree). These are for the foundation session; never edited from
this build repo. The governing rule is contract-first / **spec wins** (the
`architecture.md` invariant), cited by name to avoid the P-number collision in
§7 #10 (product `principles.md` P3 = "agent-readable by construction", a
different principle).

- **#1 / #1a — reconciled in this change (the build side of the contract).**
  `specs/contract/interfaces.md §4` predates reasoning/attachments/rich-usage.
  This change adds the additive `Request.ProviderOptions`/`Attachments`,
  `Response.CachedTokens`/`ReasoningTokens`/`ProviderMetadata`, and the
  `Attachment` type + `ErrAttachmentUnsupported` capability error to
  `ports/model` — a model *capability* error parallel to
  `ErrEmbeddingsUnsupported`, **not** an `apperrors` domain sentinel, so the
  fixed `apperrors` registry and `interfaces.md §0` are untouched. The
  interfaces.md §4 struct listing should gain the same additive fields upstream.
- **#2 — fixed here.** `specs/adr/ADR-0020` §2 + `interfaces.md §4` name OpenAI
  and Gemini as BYOK providers; the build had only `fake`/`anthropic`/`ollama`/
  `vllm`. This change ships all three (`openai_compatible`, `openai`, `gemini`).
- **#3 — raise.** `specs/contract/ai-operational-spec.md §1.4` example binds
  `embeddings: {provider: local, …}` / `stt: {provider: local}` — a bare `local`
  provider name no adapter implements (`SelectBrain` has `ollama`/`vllm`, not
  `local`). A naming gap independent of this change; no `local` alias invented here.
- **#4 — raise.** `ai-operational-spec.md §1.1` names GPT/Gemini classes for
  cheap-cloud/premium, and the WP3 exit gate requires evals on "the local-default
  **and** the cloud-default bindings"; cloud-default is Anthropic, so OpenAI/Gemini
  are named-but-untested. This change ships the adapters + unit coverage; which
  cloud provider WP3 gates on is a spec/WP3 call.
- **#5 — raise.** Mistral is spec-named only as an open-weight **local** model
  (ADR-0012/A23), yet La Plateforme is an OpenAI-compat **cloud** endpoint —
  reachable now via `openai_compatible` + `base_url`. Whether to add a named
  `mistral` cloud alias is a product call.
- **#6 — raise.** No model-capability catalog exists (context window,
  supports-vision/-caching/-reasoning). Out of scope here (YAGNI — the router
  keys on tier); noted as a future item, not half-built.
- **#7 — raise.** `model.Message` is `{Role, Content}` — no per-part slot for
  Gemini-3.x thought signatures or OpenAI reasoning items, so full *native*
  multi-turn thought continuity can't be expressed on the seam. This change
  rides the `ProviderMetadata`→`ProviderOptions` pass-through instead (the Gemini
  thought-signature round-trip); a richer typed-parts `model.Message` is a future
  seam change. Single-shot tasks are unaffected.
- **#8 — documented (no code change).** `openai_compatible` `/embeddings` 404s on
  OpenRouter/Groq/DeepSeek (chat-only); Mistral `-latest` aliases drift/deprecate.
  Captured in `config/ai-routing.example.yaml` + `docs/reference/configuration.md`
  (bind embeddings to a vendor that serves the lane or a local model; pin explicit
  model versions).
- **#9 — raise + follow-up.** `specs/adr/ADR-0050`/A65 (per-provider AI-quality
  conformance, catalog at `specs/contract/ai-acceptance-catalog.md`) certifies AI
  quality *per provider* (Certified / Supported-degraded / Not-supported). Adding
  `openai`/`gemini`/blessed `openai_compatible` targets pulls them into that AIUC
  matrix — a test/catalog obligation to mark them "supported", tracked as a
  separate change, not shipped here. ADR-0050 explicitly leaves the ADR-0013/0020
  invariants and the `Client` seam untouched, so this is not a seam blocker.
- **#10 — no code.** Cite "contract-first / spec wins" (the `architecture.md`
  invariant) by name, not the bare "P3", in commits/comments — `product/principles.md`
  P3 is a different principle.
- **#11 — BYOK key sourced from the environment, not the routing file (reconcile
  upstream).** ADR-0020 / `interfaces.md §4` model the customer key as an
  `api_key` in `ai-routing.yaml`. This build instead reads each cloud provider's
  key from its conventional environment variable (`GEMINI_API_KEY`,
  `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_COMPATIBLE_API_KEY`) at boot and
  fails closed (naming the var) if missing; the config carries no `api_key` field
  (a stray one is a parse error). This is a deliberate security-posture decision —
  secrets in the environment, config names only providers (12-factor) — to
  reconcile with ADR-0020's wording. The `Client` seam and the no-inference
  invariant are unchanged.

Implementation follow-ups deferred from this change (honest floors shipped now):

- **Image mapping on the generic `openai_compatible` wire.** The shared chat
  wire is text-only, so `openai_compatible` currently *rejects* every attachment
  (image and document) with `ErrAttachmentUnsupported` rather than accept-and-drop.
  Native `openai`/`gemini` carry images+PDFs today; mapping images to `image_url`
  content parts on the generic wire is the follow-up. `base_url` for the OpenAI-wire
  providers is the vendor host root with **no** `/v1` segment (the adapter adds it).
- **Gemini batch embeddings.** `gemini` Embed makes one `:embedContent` call per
  input (spec §3.5's named endpoint); a large retrieval batch is N sequential
  round-trips. Folding onto `:batchEmbedContents` is the follow-up.
- **Embedding dimensionality is provider/model-specific — own PR.** The store
  column is a fixed `vector(1024)` and `search.embeddingDims` pins it; cloud
  embedders default wider (Gemini 3072, OpenAI 1536), so this change adds
  `EmbedRequest.Dimensions` and the adapters truncate to 1024
  (`outputDimensionality` / `dimensions`). But native widths differ per
  provider/model, and mixed models cannot rank against each other. A proper
  design (store the dimension — and ideally the model — alongside each embedding
  row so the lane can change without a full re-embed, or make the column width
  configurable) is a separate PR. Until then, switching the embed binding means
  wiping the store (as the module comment already notes). Filed upstream as
  foundation #1074. Truncation applies to native `openai`/`gemini` only:
  the generic `openai_compatible`/`vllm` wire omits the `dimensions` knob
  entirely (vLLM rejects it on non-matryoshka models), so a model bound
  there must natively emit the store's width.
- **Native tool-use mapping for `openai`/`gemini`.** The tasks run in JSON mode
  today, so no caller sets `req.Tools`; the native adapters currently **reject**
  a non-empty `Tools` (loud, not a silent drop) rather than map it. Mapping to
  the Responses `tools` / Gemini `functionDeclarations` shapes is the follow-up
  when a tool-using task routes to these providers.
