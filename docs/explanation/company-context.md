# Company context — cold-start onboarding and governed AI grounding

One installation serves one organization — and Margince keeps a confirmed,
durable understanding of that company: what it sells, to whom, and what proves
it. That understanding is born in first-run onboarding (from a website read, a
manual form, or both), lives as provenance-bearing rows in the `people` module,
and is injected into AI tasks as **governed, scoped data** — never as ad-hoc
prompt prose. This page explains the whole lane; the model runtime it feeds is
[ai-runtime.md](ai-runtime.md).

## The shape at a glance

```text
 FIRST RUN (the wizard)                 THE PROFILE (people module)       AI TASKS (compose + ai)
 ─────────────────────                 ───────────────────────────      ───────────────────────
 Read → Confirm → Voice                 organization        (identity)   CompanyContextProvider
      → Results → Connect               organization_profile_field         task → scopes | none
   │                                    organization_fact   (evidence)        │  (closed policy,
   ├─ "Read my website"                 site_read           (dossier)         │   fitness-gated)
   │    progressive, evidence-or-omit        ▲                                ▼
   │    confirm = accept-subset ─────────────┘                        <company_context_data>
   └─ "Enter it myself"                 human edits outrank                data block in the
        3 required fields                machine refreshes                 prompt → ai.Router
                                                                       (fingerprint → trace + cache)
```

## The four canonical layers

There is no denormalized "AI profile" that can drift from company data. The
governed source of truth is four distinct concepts, all workspace-scoped and
provenance-bearing:

1. **Identity** — canonical `organization` columns and the primary domain.
2. **Business profile** — human-confirmable single-value statements
   (`organization_profile_field`: offer summary, ICP, value proposition, USP,
   buyer roles, …) with evidence, confidence, source, and capture actor.
3. **Evidence facts** — repeatable, source-grounded findings
   (`organization_fact`: locations, services, products, certifications, named
   customers, technologies, …), linked back to their `site_read`.
4. **Operational dossier** — the crawl record itself (`site_read`: progress,
   pages read/skipped, stop reason). Never prompt context by itself.

The standing rule across all of them: **human edits outrank machine
refreshes.** A website re-read may *propose* a change; it never silently
replaces a human-held value.

Over these rows the people module exposes one typed, read-only
**`CompanyContext`** read model (`GET /company/context`) — deterministic field
ordering, named scopes (identity, positioning, sales, offer, markets, proof,
capabilities, administrative), and a deterministic fingerprint. Consumers get
bounded views of this model; nothing assembles its own company prompt from
tables.

## The five-step onboarding wizard

First login lands in a resumable wizard — **Read · Confirm · Voice · Results ·
Connect** — whose state persists server-side (`/onboarding/state`,
`onboarding_wizard_state` in the identity module), so a reload or an OAuth
round-trip reconstructs the same step.

- **Website ingestion is an optional accelerator, never a gate.** "Enter it
  myself" needs exactly three fields — company name, *what do you sell?*
  (`offer_summary`), *who do you sell it to?* (`icp`) — and works with AI
  routing disabled and zero egress. Legal/VAT/address fields are conditional,
  never a universal block.
- **A website read is progressive and honest.** The onboarding dossier
  (`/company/site-reads` start → poll → confirm, an *unbound* `site_read` that
  needs no pre-existing organization) streams grounded findings as pages are
  read, under the standing crawl guarantees: SSRF guard, robots handling, hard
  page/byte/time bounds, and evidence-or-omit — an ungrounded value stays
  empty rather than guessed.
- **Confirmation is accept-subset in one transaction.** The confirm request
  binds the inspected read version, writes only the selected fields/facts
  (audited, outbox-evented), and treats any edited value as a human assertion.
  Nothing persists to domain tables before confirmation. Published team
  members are never company rows — each is staged as a separate lead proposal.

After first run, the **Company context** settings screen edits the same
canonical rows, and "Refresh from website" runs the same dossier pipeline —
classifying every proposed value as *new*, a *machine change*, or a *human
conflict*; only the first two can be bulk-accepted, a human conflict demands
an explicit keep/accept/edit decision.

## Injection: every task declares a policy

"Use it everywhere" means every AI task declares what it gets — not that every
model call receives a generic company blurb. The compose-layer provider
(`companycontextprompt.go`) holds a **closed policy registry**: each task in
the [task contract](ai-runtime.md) declares either `none` (context would bias
the task — capture classification, website extraction, embeddings) or named
scopes with a token budget (agent loop, reply drafting, offer drafting,
NL-search vocabulary). A fitness test fails the build when a task has no
explicit declaration.

The safety frame:

- Context is rendered into a delimiter-escaped **`<company_context_data>`
  user-data block** — data, never system instructions; website-derived text
  stays untrusted even after acceptance.
- The selected scopes and the exact **context fingerprint** ride the `ai_call`
  trace and the response-cache key, so a profile edit changes both — no stale
  cached answer survives it.
- The lane sits behind the ordered `company_context.rollout` kill switch in
  `margince.yaml` (`off < read < tasks < onboarding`, default fully on);
  migration `0105` backfilled existing installations from their anchor rows
  without crawling or touching provenance.

## Reference

| Concern | Where |
|---|---|
| Read model + policy | `GET /company`, `GET /company/context` (people module); scopes/fingerprint |
| Onboarding dossier | `/company/site-reads` start/poll/confirm; unbound `site_read` (people) |
| Wizard state | `/onboarding/state`, `onboarding_wizard_state` (identity; migration `0103`) |
| Provider + prompt block | `internal/compose/companycontextprompt.go` |
| Rollout switch | `company_context.rollout` (`margince.yaml`, `platform/deployconfig`, migration `0105`) |
| Trace provenance | `ai_call` context columns (migration `0102`) — see [ai-runtime.md](ai-runtime.md) |
| The UI | `frontend/src/screens/onboarding.tsx`, `onboarding-read.tsx`, `company-context.tsx` |
| Deep read (existing orgs) | `POST /organizations/{id}/deep-read` — same engine, approval-staged |

**Related:** [ai-runtime.md](ai-runtime.md) (the Router, tracing, budget) ·
[agent-surface.md](agent-surface.md) (the agent loop that consumes the compact
profile) · [privacy-and-consent.md](privacy-and-consent.md) (erasure/retention
over the same rows). The ratified design lives upstream in
`margince-foundation` (ADR-0065/A111); the delivery plan that built this lane
is in this repo's git history.
