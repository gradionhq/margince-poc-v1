# The governed tool catalog

Every tool an agent can invoke, what it costs in passport scope, whether it runs
by itself or waits for a human, and what it does when the workspace's records
live in somebody else's CRM. The governance *model* — passports, the autonomy
tiers, the one admission gate — is explained in
[explanation/authorization.md](../explanation/authorization.md) and
[explanation/agent-surface.md](../explanation/agent-surface.md); this page is the
inventory.

## How to read this page

The catalog is derived from the `mcp.ToolSpec` each tool returns from `Spec()`,
so it says what the server registers, not what a design document intended. But
**the running server is the authority, not this page.** Two things can make a
live surface differ from the table below:

- **`tools/list` is scope-filtered per caller.** The dispatcher drops any tool
  whose `RequiredScope` the presenting passport does not hold
  (`invocableByCaller` in `backend/internal/modules/agents/dispatch.go`), so an
  agent minted with `["read"]` sees the read tools and nothing else. That filter
  mirrors the scope arm of the admission gate deliberately: a surface that
  advertises what the gate will refuse is a surface that lies. It answers the
  **scope axis only** — the seat ceiling and the granting human's object RBAC are
  re-derived per call and can still refuse a tool the listing showed.
- **Extensions register onto the same registry.** `registerComposedTools` runs
  last in `internal/compose/registry.go`, after the core registrars, so an
  extension unit can add verbs (and a name that collides with a core verb fails
  loudly at boot). A served extension tool declares 🟢 and an inbound cap — the
  boot refuses a handler-bearing confirm-first or `send`/`enrich` declaration,
  because neither could be staged for the human this surface has no way to ask.
  That governs what a unit may CLAIM; what its handler does is bounded by the
  composed set being a trust boundary, not by the gate. The
  vanilla tree ships two first-party units: `extensions/de` registers no tools,
  and `extensions/yogi` adds `yogi_quote` (🟢/read), so on a vanilla install the
  catalog below plus that one verb is the whole surface.

**Where it is served:** `cmd/api` mounts the tool surface at `/mcp` over
Streamable HTTP, on the same origin as `/oauth/*` and the discovery documents.
There is no stdio transport and no `cmd/mcp` binary — `backend/cmd/` is `api`,
`migrate`, `worker`. Connecting a client:
[how-to/connect-an-mcp-client.md](../how-to/connect-an-mcp-client.md); minting
the credential: [how-to/mint-a-passport.md](../how-to/mint-a-passport.md).

## The catalog

**26 tools**, listed in the order `Registry.Specs()` sorts them — which is the
order `tools/list` returns.

Columns:

- **Tier** — 🟢 runs immediately; 🟡 refused until a human releases the staged
  approval; **dynamic** resolves per call from the target stage's semantic
  (`open` → 🟢, won/lost → 🟡), and may only ever *raise*.
- **Scope** — the passport cap `Gate.Admit` demands before `Handle` runs.
- **Egress** — the spec's `Egress` flag: true when the tool reaches outside the
  workspace. It is what `tools/list` publishes as `openWorldHint`.
- **In overlay mode** — what the tool does when `workspace.x_sor_mode` puts the
  records in an incumbent CRM (see
  [explanation/overlay-augmentation.md](../explanation/overlay-augmentation.md)).

| Tool | Tier | Scope | Egress | In overlay mode |
|---|---|---|---|---|
| `account_coverage` | 🟢 | `read` | — | Native relationship read; carries no mode guard |
| `advance_deal` | dynamic | `write` | — | `unsupported_by_sor` (no incumbent stage map) |
| `archive_record` | 🟡 | `write` | — | Seam-routed: write-back through the incumbent |
| `at_risk_relationships` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `book_meeting` | 🟡 | `send` | yes | Staging refuses a mirror-held link |
| `catch_me_up_on` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `check_availability` | 🟢 | `read` | — | Calendar seam; not mode-routed |
| `create_record` | 🟢 | `write` | — | Seam-routed: write-back through the incumbent |
| `draft_email` | 🟢 | `draft` | — | Activities seam; not mode-routed |
| `draft_follow_ups_for` | 🟢 | `draft` | — | `unsupported_by_sor` (native-only guard) |
| `intro_path_to` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `list_pipelines` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `log_activity` | 🟢 | `write` | — | Seam-routed: write-back through the incumbent |
| `merge_records` | 🟡 | `write` | — | `unsupported_by_sor` (no atomic incumbent projection) |
| `prep_for_meeting` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `progress_deal` | dynamic | `write` | — | `unsupported_by_sor` (shares `advance_deal`'s seam) |
| `promote_lead` | 🟡 | `write` | — | `unsupported_by_sor` (no atomic incumbent projection) |
| `qualify_lead` | 🟢 | `write` | — | Seam-routed: read + patch through the provider |
| `read_record` | 🟢 | `read` | — | Mirror-backed; result carries `trust_tier: external` |
| `run_report` | 🟢 | `read` | — | `unsupported_by_sor` (no incumbent analogue) |
| `search_records` | 🟢 | `read` | — | Mirror-backed; results carry `trust_tier: external` |
| `send_email` | 🟡 | `send` | yes | Staging refuses a mirror-held anchor |
| `send_message` | 🟡 | `send` | yes | Staging refuses a mirror-held anchor |
| `update_record` | 🟢 | `write` | — | Seam-routed; see the per-field split below |
| `whats_slipping_this_week` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `who_knows` | 🟢 | `read` | — | Native relationship read; carries no mode guard |

Three rows deserve their footnote:

- **`update_record` is 🟢 with a 🟡 residue.** The patch splits per field: the
  fields no human last wrote apply immediately, and the fields a human *did*
  last write are staged for approval and named in the result's
  `staged_approval`, together with the exact replay call that redeems them. A
  machine does not silently undo a person, and a person does not block the
  machine's own fields.
- **The dynamic pair reads the stage's *semantic*, not its label.** A custom
  pipeline's renamed "Won" column still resolves 🟡, because
  `advanceDealTier` trusts the configured semantic; anything not provably `open`
  resolves 🟡, so an unknown or unreadable semantic fails *toward* the approval
  gate.
- **The "native-only guard" rows are a declared refusal, not a bug.** Those
  tools ground on the report engine, the retrieval index, the pipeline
  configuration or the interaction projection — none of which the incumbent
  mirror holds. Unguarded they would return a well-formed empty answer, and "no
  deals are slipping" is a worse failure than "this is not available here",
  because only one of them is visibly wrong. The wrappers live in
  `internal/compose/nativeonlytools.go`.

## What each scope buys

The passport vocabulary is closed: `read`, `draft`, `write`, `send`, `enrich`
(`principal.Scope`). Effective authority is always the intersection of the
passport's scopes and the granting human's live RBAC and seat — never the union,
and never the passport alone.

| Scope | Tools it unlocks | What it means |
|---|---|---|
| `read` | 12 | Reads only. It is also the sole scope that makes a tool `readOnlyHint: true`, and the only scope a **read seat** may spend at all. |
| `draft` | 2 | Proposes text. Not read-only: `draft_email` returns a proposal and writes nothing, while `draft_follow_ups_for` persists a draft activity on the deal's timeline. |
| `write` | 9 | Creates, patches, archives, advances, merges, promotes — every change that stays inside the workspace. |
| `send` | 3 | The three egress verbs. All three are 🟡, so the scope buys the right to *ask*, never the right to send unattended. |
| `enrich` | **0** | See below. |

**No registered MCP tool requires the `enrich` scope.** All 26 specs declare
`read`, `draft`, `write` or `send`. The cap is not decorative, though — it
governs **REST routes**, under ADR-0055's rule that a passport is a Bearer
credential for `/v1` governed exactly like `/mcp`. Five contract operations
consume it:

| Operation | Route | Tier |
|---|---|---|
| `coldStartPreview` | `POST /v1/coldstart/preview` | confirmation_required |
| `coldStartReadback` | `POST /v1/coldstart` | confirmation_required |
| `deepReadCompany` | `POST /v1/organizations/{id}/deep-read` | confirmation_required |
| `scrapeCompany` | `POST /v1/organizations/{id}/enrich` | confirmation_required |
| `reconcileOverlay` | `POST /v1/overlay/reconcile` | auto_execute |

So an `enrich` passport is spendable — just never over `/mcp`. Grant it when the
agent's job is outward-looking research on the REST surface, and leave it off a
passport that only drives tools.

## Verbs the contract declares with no registered tool

`api/crm.yaml` annotates mutating operations with `x-mcp-tool`, and
`tools/gen-agentpolicy` compiles those annotations into
`internal/compose/agentpolicy_gen.go` — the table the REST admission gate reads.
Eleven verbs named there have **no tool in the registry**. They are fully
governed on REST at the tier and cap the contract declares, and simply invisible
on `/mcp`: an agent reaches them by calling the endpoint with its passport, not
by `tools/call`.

| Verb | Operations | Tier | Scope |
|---|---|---|---|
| `advance_project_phase` | `advanceProjectPhase` | confirmation_required | `write` |
| `connect_incumbent` | `connectOverlay` | confirmation_required | `write` |
| `disconnect_incumbent` | `disconnectOverlay` | confirmation_required | `write` |
| `disqualify_lead` | `disqualifyLead` | confirmation_required | `write` |
| `draft_offer` | `regenerateOffer` | auto_execute | `write` |
| `enrich` | `coldStartPreview`, `coldStartReadback`, `deepReadCompany`, `scrapeCompany` | confirmation_required | `enrich` |
| `reconcile_overlay` | `reconcileOverlay` | auto_execute | `enrich` |
| `relink_activity` | `relinkActivity` | auto_execute | `write` |
| `render_offer` | `renderOffer` | auto_execute | `write` |
| `send_offer` | `sendOffer` | confirmation_required | `send` |
| `share_record` | `createRecordGrant`, `revokeRecordGrant` | confirmation_required | `write` |

The traffic runs the other way too: eleven registered tools name no contract
verb, because they are *intents* composed over several operations rather than a
transport for one — `account_coverage`, `at_risk_relationships`,
`catch_me_up_on`, `draft_follow_ups_for`, `intro_path_to`, `list_pipelines`,
`prep_for_meeting`, `progress_deal`, `qualify_lead`,
`whats_slipping_this_week`, `who_knows`. Their `OpenAPIOp` field records the
composition (`progress_deal` reads `advanceDeal + logActivity`) as documentation,
not as a policy key.

## The two things a spec cannot lie about

`tools/list` publishes two annotation hints, and neither is hand-set:

- **`readOnlyHint` is derived from the scope** — `ToolSpec.ReadOnly()` is
  `RequiredScope == ScopeRead`, full stop. A second, hand-written copy could
  disagree with the scope the gate actually enforces, and the hint is the half a
  client would believe. `draft` is deliberately *not* read-only: one scope covers
  both a tool that writes nothing and a tool that persists a draft activity, so
  the scope cannot answer the question and the conservative half is the only
  honest one.
- **`openWorldHint` is the `Egress` flag** — the same boolean the catalog above
  reports, read off the same spec.

`destructiveHint` and `idempotentHint` are deliberately absent: the protocol
defaults (destructive, non-idempotent) are already the conservative reading, and
only the *looser* value would need a per-tool judgement with nothing to hold it
true.

Four gates keep the catalog and the contract from drifting apart:

| Gate | Where | What it holds |
|---|---|---|
| `TestTheContractScopeMatchesTheRegisteredToolScope` | `backend/internal/compose/agentscopeparity_test.go` | One verb, one cap, both wires — a passport refused a verb on REST cannot spend it over MCP. |
| `TestEveryToolRouteDeclaresAGrantableScope` | same file | No contract route demands a cap no passport can hold. |
| `TestNoWritingToolIsAdvertisedAsReadOnly` | `backend/internal/modules/agents/conformance_test.go` | The derived hint stays true across the whole registered set. |
| `TestEveryToolScopeIsGrantableAndEgressNeedsAnOutboundCap` | `backend/internal/modules/agents/scope_fitness_test.go` | An egress tool cannot ride a non-outbound cap. |

Both sweeps in the parity file are derived from the generated policy table, so a
verb added to the contract tomorrow is covered without anyone extending a list.

## Refusal shapes

A tool failure is **not** a JSON-RPC error. It comes back as a normal
`tools/call` result with `isError: true` and one text block, because the agent is
supposed to read it and adapt. `Dispatcher.explain`
(`backend/internal/modules/agents/explain.go`) turns the sentinel taxonomy into
that text — the distinction between "you may never", "a human must say yes" and
"you typed the id wrong" is exactly what decides the agent's next move.

| Sentinel / error | What the agent is told | Retry? |
|---|---|---|
| `ErrRequiresApproval` | Confirm-first (🟡) action; needs human approval; nothing was changed | No — wait for the approval, then replay carrying `approval_id` |
| `ErrScopeExceeded` | The passport does not grant the scope this tool needs | No — the cap is fixed for the passport's life |
| `ErrPermissionDenied` | The human this passport acts for is not permitted to do this | No — the agent inherits exactly their access |
| `ErrNotFound` | No such record in this workspace, or outside the acting user's row scope | No — existence-hiding is deliberate |
| `ErrVersionSkew` | The record changed since it was read | **Yes** — re-read and retry with the new version |
| `ErrApprovalTokenInvalid` | Token consumed, expired, or for a different call | **Yes** — after asking for a fresh approval |
| `ErrUnsupportedBySoR` | This workspace's system of record cannot serve this tool | **Never** — a declared capability gap; use another tool or tell the user |
| `UnknownToolError` | The name is not on the surface | No — call `tools/list` and use a name from it |
| `BadArgsError` | Named argument rejected *before* the tool ran; nothing changed | No — fix the argument against `inputSchema` first |
| anything else | Classified through `httperr.Classify`: a transient fault says "the same call can succeed later"; any other 4xx says "refused as issued" | Per the message |
| unclassified | "The tool failed for an internal reason" — the only unactionable answer on the surface | Yes, then escalate |

Two properties worth relying on:

- **The refusals name what changed, and it is always nothing.** Every branch
  above is reached before or instead of the write.
- **Internals never cross the boundary.** Driver errors, hosts and wrap chains
  are logged server-side; the agent sees the sentinel's own words. Text echoed
  back from the caller's own arguments is bounded and escaped, so a newline in a
  tool name cannot forge a frame in the run transcript later prompts read.

## The protocol surface

`/mcp` speaks the tools-only subset of MCP over Streamable HTTP, dispatched by
`backend/internal/modules/agents/dispatch.go` behind the transport in
`httpmcp.go`.

**Methods answered:** `initialize`, `ping`, `tools/list`, `tools/call`,
`resources/list`, `resources/templates/list`, `prompts/list`. Anything else is
`-32601 method not found`.

**Protocol versions**, newest first: `2025-11-25`, `2025-06-18`, `2025-03-26`.
`initialize` echoes the client's requested revision when the server satisfies it,
and otherwise answers with the newest — never the client's unsupported one, which
would promise a handshake the server cannot honor. A *present* and unsupported
`MCP-Protocol-Version` header on a non-`initialize` request is refused with a
plain `400` whose body is prose, deliberately **not** a `-32022`
`UnsupportedProtocolVersionError`: a dual-era client identifies this server as
legacy by seeing a 4xx *without* a recognized modern error body and then falls
back to `initialize`. `2024-11-05` is excluded because it predates Streamable
HTTP.

**`resources/list` and `prompts/list` answer empty rather than `-32601`.** This
server has no resources and no prompts, but claude.ai calls both right after
`initialize` regardless, and an unadvertised capability answering "method not
found" reads as a broken server rather than as a legitimate empty catalog.

**`GET /mcp` is `405`.** The transport serves `POST` (one JSON-RPC exchange) and
`DELETE` (close the session this passport opened); the GET SSE stream is a later
phase. That is also why `initialize` reports
`capabilities.tools.listChanged: false` — the notification travels on the GET
stream, so claiming it would promise a message that can never arrive. The surface
really does change per caller, but the honest answer is that this server cannot
announce it.

**A tool result carries the answer twice.** Every registered tool declares an
`outputSchema`, so a successful `tools/call` returns the serialized JSON both in
a `TextContent` block and as `structuredContent` — the same bytes passed through
rather than a re-marshalled copy, so a client comparing the two never finds a
widened integer or a reordered key. What is actually checked is object-ness, not
the full schema; every `outputSchema` on this surface is the bare
`{"type":"object"}`, for which the two are the same claim.

**Sessions.** A successful `initialize` mints one and returns it as
`Mcp-Session-Id`. `DELETE /mcp` closes only the session the *presenting*
passport opened; a session id that does not exist under that passport — whether
it never existed or belongs to someone else — answers `404` identically, so a
probe cannot tell the two apart.

**Every call re-authenticates.** The binder runs per call, not per session, so
revoking the passport or demoting the granting human takes effect on the very
next `tools/call`, mid-session. A credential the server cannot *reach* a verdict
on answers `503`, never `401` — a 401 would tell a well-behaved client its good
token is bad and turn an outage into mass re-consent.

## Where to go next

- What a human may do, which every agent is capped by:
  [rbac-matrix.md](rbac-matrix.md).
- The gate, the tiers, and what a passport is:
  [explanation/authorization.md](../explanation/authorization.md).
- The reason-act-observe loop that drives these tools from inside the product:
  [explanation/agent-surface.md](../explanation/agent-surface.md).
- What the `agents` module owns and where it sits: [modules.md](modules.md).
