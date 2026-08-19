# ADR-0003 — The customer's own agent drives, and the product ships the governed tools

**Status:** Active

**Decided:** 2026-06-04

## The decision

The product is the system of record and the system of action; the reasoning
comes from outside it. A customer points their own agent host — Claude, an
in-house runner, anything that speaks MCP — at the served tool surface, and
that agent works the CRM through named tools rather than through the database.
Every tool call re-authenticates against a passport that binds the agent to one
human, so an agent can never reach further than the person who granted it.

The product also ships its own AI features for people who bring no agent:
capture and enrichment, summarizing, draft assistance, natural-language search
and reporting. Those features run inside the product's own code against the
same governed paths. The two are the same product, split by who drives the
loop, not by who pays or what is allowed.

Underneath both sits shared plumbing: one provider-agnostic model client, the
capture pipeline, retrieval over Postgres text search and pgvector, and a
credential stripper on every outbound model payload.

## Why

The competing move is to sell intelligence by the seat on a data model never
designed for it. That charges the customer twice and locks their reasoning into
one vendor's model. Serving governed tools instead means the product competes on
where intelligence runs and what it is permitted to do, which is the part a
model vendor does not supply.

Without the passport and the tool boundary, "let an agent use the CRM" means
handing it a database connection. Then nothing records who did what, and no
permission the human has actually constrains the agent.

## What it binds in this repository

- `backend/internal/modules/agents/` owns the tool surface: `registry.go` and
  `tools.go` hold the catalog, `tools_records.go` through `tools_report.go` the
  individual verbs, and `tierfloor.go` the per-tool autonomy floor.
- `backend/internal/modules/agents/runner/` is the product's own reasoning loop
  and reaches actions only through `Invoker`, the same `Registry.Invoke` an
  inbound agent hits.
- `backend/internal/modules/ai/` owns the model runtime: `router.go` for tiered
  routing, `selectbrain.go` for turning config into a provider client, and
  `stripper.go` for the credential pass over every outbound payload.
- Passports live in `backend/internal/modules/identity/passport.go`; the
  seat and scope ceiling is applied in `backend/internal/platform/auth`.
- The tool surface is served at `/mcp` by `cmd/api`; there is no separate agent
  binary.
- `backend/internal/modules/agents/doc.go` records that the module owns no
  tables — records stay with the domain modules behind the datasource seam.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19.

The original record funded the built-in AI tier out of the seat licence.
ADR-0020 retired that in 2026-06-16: the features stayed, the funding clause
went, and all inference is now supplied by the customer.
