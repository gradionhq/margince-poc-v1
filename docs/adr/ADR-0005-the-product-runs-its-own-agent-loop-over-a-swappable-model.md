# ADR-0005 — The product runs its own agent loop over a swappable model client

**Status:** Active — the loop, the swappable model client and the served tool
surface are built.
**Decided:** 2026-06-04

## The decision

The product owns its server-side agent loop. It writes the reason-act-observe
cycle itself rather than handing orchestration to a vendor's hosted agent
service, and it calls whichever model the operator bound through one
provider-agnostic client. A local model is a first-class engine on that client,
not a degraded fallback.

Bringing your own model means an API key or a local endpoint. It never means a
chat subscription: the vendor agent SDKs forbid subscription auth for
programmatic use, so promising to run agents on a customer's Claude or Copilot
plan would be a promise the vendor blocks.

The governed tool surface comes first and matters more than the loop. It is the
one artifact both a customer's external agent host and the product's own loop
consume. The product's own loop exists for what an external host structurally
cannot do: run headless on a schedule with nobody's laptop open, run against a
local model for a customer who lets no data leave, and answer to central
governance rather than one person's per-host permissions.

## Why

An agent SDK runs the loop inside your own process; the vendor supplies only
inference. So an "autonomous agent" is mostly your code with a swappable engine,
and outsourcing that code to a hosted orchestration service buys little while
tying the product to one vendor and ruling out local models.

The served tool surface is the durable position. A scheduled agent in someone's
chat app is useless over a CRM until the CRM appears in its connector list. Once
it does, the flagship agent behaviours ship on somebody else's runner at no
runner cost.

## What it binds in this repository

- `backend/internal/modules/agents/runner/runner.go` is the loop. Its `Invoker`
  interface is its only path to an action, and `agents.Registry` is the only
  type that satisfies it — the same entry point an external agent hits, so
  there is one audit stream and no privileged back door.
- `backend/internal/modules/agents/runner/catalog.go` holds the scheduled goals
  as code, each with its own budget and a narrowed tool list.
- `backend/internal/modules/ai/selectbrain.go` turns one config binding into a
  provider client; `backend/internal/shared/ports/model` is the frozen seam.
- Local engines are `ollama.go` and the vLLM path in the same module;
  `ProviderIsLocal` in `routing.go` names the set that may serve the sovereign
  zero-egress profile.
- The inbound surface is HTTP-only, served at `/mcp` by `cmd/api`. The former
  local stdio connector is gone: a static configured passport could not honour
  revocation mid-session.

## What is owed

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19.

Amended three times in the source. The first amendment moved the emphasis from
the loop to the tool surface. The second split the inbound surface into a local
one and a hosted one, because a scheduled agent that runs while the laptop is
closed can only reach a hosted endpoint. The third recorded that the operator —
a hosting partner or the self-hosting customer — runs that endpoint, never
Gradion. The local stdio surface was retired in 2026-08 for the revocation
reason above, leaving the hosted HTTP surface as the only inbound one.

The original decision also reserved a delegated-worker protocol: hand a scoped
task to an external vendor agent — a cloud coding agent, an editor agent — and
let it report back through the same tool surface and audit trail. It was
retired 2026-08-19 without ever being built. Nobody asked for it, and reserving
it carried a cost: it left an unanswered security question standing in the
record — which scopes such a worker may hold, and how its egress is bounded
when it reads the record graph from outside the deployment. An unanswered
question about a feature that does not exist is better closed than kept open.
If the need returns it is a new decision, and the security question is its
first one rather than an inherited assumption.
