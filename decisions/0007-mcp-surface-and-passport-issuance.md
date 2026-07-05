# 7. The A1 MCP surface: local passport issuance, per-call binding, and the tool subset

Date: 2026-07-04
Status: accepted

## Context

WP4 (B-EP06.8–.11) needs the governed MCP tool surface: agents call CRM tools under an Agent
Seat Passport, admitted by scope ∧ tier before any handler runs. The spec leaves three PoC-scale
choices open:

1. **How a passport is minted.** The contract validates bearer tokens but specifies no issuance
   path (`fable feedback/04`); the full answer is OAuth2 + PKCE + DCR on the hosted A2 surface.
2. **How the stdio server authenticates**, given RLS makes the passport row invisible without a
   workspace GUC.
3. **Which tools ship now**, given the EP07 approval inbox (the 🟡 redemption path) is not built.

## Decision

1. **Session-authenticated `POST /passports` mints; `DELETE /passports/{id}` revokes.** The
   issuer is always `on_behalf_of` themselves — the request cannot name another human, so a
   passport is structurally ≤ its issuer. Scopes are the closed verb vocabulary
   (read/draft/write/send/enrich); TTL is capped at 90 days; the raw `mgp_`-prefixed token
   appears exactly once and is stored as a SHA-256 hash. This is the local/A1 path in the
   contract itself (P3) — A2 replaces it with the OAuth2 flow, it does not extend it.
2. **The workspace is an explicit input** (`--workspace` flag / subdomain slug), and the token is
   looked up inside `WithWorkspaceTx` — the same RLS-scoped shape as session auth, no infra
   bypass. The stdio server **re-authenticates per tools/call**: revocation and human demotion
   bind on the next call of a live session, because the granting human's RBAC is loaded live
   ("agent ≤ human" is a runtime property, not a mint-time snapshot).
3. **Only tools whose Handle can legally run are registered**: the 🟢 CRUD set
   (search/read/create/update/log_activity) plus `advance_deal` with its `TierDynamic` resolver
   (🟢 open→open, 🟡 to won/lost — resolved from the target stage's *semantic*, never the
   request). The static-🟡 set (archive/merge/disqualify/enrich/send) waits for the EP07 approval
   inbox: registering tools that can never execute would be surface theater, and their handlers
   would be dead code. The gate still enforces the 🟡 floor — a dynamic call resolving yellow is
   refused with `ErrRequiresApproval` and zero side effects.

Supporting shape: the tools compose over `sor.SystemOfRecordProvider` (crm-core's SoR-mode
implementation binds it to the store, so MCP rides the same RBAC/row-scope/audit path as HTTP);
the admission gate is its own package (`internal/gate`) so nothing can mint an admitted
capability elsewhere; tool inputs are validated by strict decoding (unknown argument names are
errors) — the declared JSON schemas describe the surface for `tools/list`, a schema-validator
dependency was not worth taking for the PoC.

## Consequences

- Any MCP client (Claude Desktop, Cursor, a script) can drive the CRM today:
  `crm mcp --workspace <slug>` with `MARGINCE_PASSPORT_TOKEN` in the env (env, not argv — argv
  is world-readable).
- Agents also get the REST surface with the same bearer token (ADR-0013, one surface); the
  middleware maps GET→read / mutations→write scopes there.
- When EP07 lands, the 🟡 tools register, `gate.Admit` gains the approval-token redemption
  branch, and nothing else moves.
- `sor.AdvanceDealInput` gained `LostReason` — the seam as specified could not close a deal as
  lost (recorded as spec feedback 16).
