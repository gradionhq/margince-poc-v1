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

The **33 core tools**, listed in the order `Registry.Specs()` sorts them — which
is the order `tools/list` returns. An enabled extension unit adds its own verbs
to the same listing, so a vanilla install answers 34: these plus `yogi_quote`
(🟢, `read`), which is not tabled here because the catalog tracks the core
surface.

This table and `api/crm.yaml` cannot disagree: every operation carrying
`x-mcp-tool` has a registered tool of that verb, and every registered tool is
either declared by an operation or listed as a composed intent — both directions
gated by `TestEveryDeclaredToolVerbIsRegistered` /
`TestEveryRegisteredToolIsDeclaredOrAnIntent`. An operation that cannot honestly
have a tool carries `x-agent-access: human-only` instead.

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
| `advance_project_phase` | 🟡 | `write` | — | Runs: a project is native-only, so its table is the live one in either mode |
| `archive_record` | 🟡 | `write` | — | Seam-routed: write-back through the incumbent |
| `at_risk_relationships` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `book_meeting` | 🟡 | `send` | yes | Staging refuses a mirror-held link |
| `catch_me_up_on` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `check_availability` | 🟢 | `read` | — | Calendar seam; not mode-routed |
| `create_record` | 🟢 | `write` | — | Seam-routed: write-back through the incumbent |
| `disqualify_lead` | 🟡 | `write` | — | `unsupported_by_sor`: a lead is mirrored and the provider cannot serve this write, so the native table is empty |
| `draft_email` | 🟢 | `draft` | — | Activities seam; not mode-routed |
| `draft_follow_ups_for` | 🟢 | `draft` | — | `unsupported_by_sor` (native-only guard) |
| `enrich` | 🟡 | `enrich` | yes | Reads the company's own website, not a record store; the write-back is seam-routed |
| `intro_path_to` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `list_pipelines` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `list_records` | 🟢 | `read` | — | Mirror-backed unfiltered; a FILTERED call is `unsupported_by_sor` (see below) |
| `log_activity` | 🟢 | `write` | — | Seam-routed: write-back through the incumbent |
| `merge_records` | 🟡 | `write` | — | `unsupported_by_sor` (no atomic incumbent projection) |
| `prep_for_meeting` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `progress_deal` | dynamic | `write` | — | `unsupported_by_sor` (shares `advance_deal`'s seam) |
| `promote_lead` | 🟡 | `write` | — | `unsupported_by_sor` (no atomic incumbent projection) |
| `qualify_lead` | 🟢 | `write` | — | Seam-routed: read + patch through the provider |
| `read_brief` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `read_record` | 🟢 | `read` | — | Mirror-backed; result carries `trust_tier: external` |
| `query_workspace` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `relink_activity` | 🟢 | `write` | — | Runs: a link row is not an SoR record write, so it is available in either mode |
| `run_report` | 🟢 | `read` | — | `unsupported_by_sor` (no incumbent analogue) |
| `search_records` | 🟢 | `read` | — | Mirror-backed; results carry `trust_tier: external` |
| `send_email` | 🟡 | `send` | yes | Staging refuses a mirror-held anchor |
| `send_message` | 🟡 | `send` | yes | Staging refuses a mirror-held anchor |
| `update_record` | 🟢 | `write` | — | Seam-routed; see the per-field split below |
| `whats_slipping_this_week` | 🟢 | `read` | — | `unsupported_by_sor` (native-only guard) |
| `who_knows` | 🟢 | `read` | — | Native relationship read; carries no mode guard |

Four rows deserve their footnote:

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
- **`list_records` splits on whether it was asked to narrow.** Unfiltered, it is
  an enumeration the mirror can serve like any other read. Filtered, it cannot
  be: the mirror holds the incumbent's rows as opaque fields, so `owner_id` or
  `stage_id` has nothing to bind to — and answering the unnarrowed page would
  return a superset of what was asked for wearing the shape of the right answer.
  So the overlay provider refuses the filtered call outright (AC-OV-2). Which
  filters exist at all is not written here or in the tool: they are the
  intersection of each list operation's own `crm.yaml` parameters and what the
  record's store can bind, resolved at boot and published in the tool's schema.

## What each scope buys

The passport vocabulary is closed: `read`, `draft`, `write`, `send`, `enrich`
(`principal.Scope`). Effective authority is always the intersection of the
passport's scopes and the granting human's live RBAC and seat — never the union,
and never the passport alone.

Counts are of the core catalog above; an enabled unit's verbs add to them
(vanilla: `yogi_quote` makes `read` 16).

| Scope | Tools it unlocks | What it means |
|---|---|---|
| `read` | 15 | Reads only. It is also the sole scope that makes a tool `readOnlyHint: true`, and the only scope a **read seat** may spend at all. |
| `draft` | 2 | Proposes text. Not read-only: `draft_email` returns a proposal and writes nothing, while `draft_follow_ups_for` persists a draft activity on the deal's timeline. |
| `write` | 12 | Creates, patches, archives, advances, merges, promotes, disqualifies, re-links — every change that stays inside the workspace. |
| `send` | 3 | The three egress verbs. All three are 🟡, so the scope buys the right to *ask*, never the right to send unattended. |
| `enrich` | 1 | `enrich` — the one verb that fetches from a third party. 🟡 and `Egress: true`, like the `send` three: the cap buys the right to ask. |

The `enrich` cap governs the two organization read routes — `scrapeCompany`
(`POST /v1/organizations/{id}/enrich`) and `deepReadCompany`
(`POST /v1/organizations/{id}/deep-read`) — on REST, and the `enrich` tool that
composes them on `/mcp`, under ADR-0055's rule that a passport is a Bearer
credential for `/v1` governed exactly like `/mcp`. The cold-start routes spent
it once; they are human-only now, because they create the organization rather
than enrich one. Grant the cap when the agent's job is outward-looking research
on a record that already exists.

## Operations an agent may not reach at all

`api/crm.yaml` annotates mutating operations with `x-mcp-tool`, and
`tools/gen-agentpolicy` compiles those annotations into
`internal/compose/agentpolicy_gen.go` — the table the REST admission gate reads.
An operation that no tool can honestly back does not keep the annotation; it
carries `x-agent-access: human-only`, and the gate rejects an agent principal
outright, whatever its scope or seat. Ten operations say so, and the reasons
differ enough to be worth reading:

| Operation | Why no agent may call it |
|---|---|
| `coldStartReadback`, `coldStartPreview` | They CREATE the organization, so there is no record for a record-shaped verb to target. The `enrich` tool keeps the two organization routes. |
| `createRecordGrant`, `revokeRecordGrant` | The grant verbs refuse a non-human principal at redemption, so an agent-staged, human-approved share was refused every time it would have applied. |
| `connectOverlay`, `disconnectOverlay` | Sealing a credential and flipping the system-of-record mode is an installation decision, not an act an agent performs. |
| `reconcileOverlay`, `renderOffer`, `regenerateOffer` | No tool backs them, and none can today. |
| `sendOffer` | Human-only until the contract and the implementation agree on what sending an offer does — the description says it leaves the workspace; the code flips status, freezes `fx_rate_to_base` and snapshots buyer/issuer, with no transport (poc-v1#481). |

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

**Two framings, one dispatcher, chosen per request** (ADR-0092/A141). A request
that declares its own protocol version in
`params._meta["io.modelcontextprotocol/protocolVersion"]` — or whose
`MCP-Protocol-Version` header names the modern revision — is served as
**modern** (`2026-07-28`): no handshake, no session, and everything the call
needs travels with it. Anything else is served as **handshake-era** exactly as
before. The framing decides how a call is *parsed and rendered*, never what it
may do: both reach the same registry and the same admission gate.

**Methods answered in both framings:** `tools/list`, `tools/call`,
`resources/list`, `resources/read`, `resources/templates/list`, `prompts/list`.

**Methods each era owns:** `initialize` and `ping` in the handshake framing,
`server/discover` in the modern one. Each is `-32601` in the *other* framing.
For the two opening calls, answering one would tell a client it had reached the
era it was probing for; `ping` is simply gone — the `2026-07-28` revision
removed it along with the handshake it kept alive. Anything else is
`-32601 method not found` — with HTTP `404` in the modern framing, which is what
lets a dual-era client tell this server from one that does not host the endpoint.

**Protocol versions**, newest first: `2026-07-28` (modern, per request),
`2025-11-25` and `2025-06-18` (handshake era). `2025-03-26` was **dropped** from
the compatibility window; `2024-11-05` was never served, because it predates
Streamable HTTP. `initialize` echoes the client's requested revision when the
server satisfies it in the handshake era, and otherwise answers with the newest
one it does — never the client's unsupported one, and never the modern
revision, which needs no handshake. A version this server does not serve is
refused `400` with `-32022 UnsupportedProtocolVersion` and a `data.supported`
list naming every revision it does, so a client retries rather than guesses.

**A modern request must carry what it declares.** `_meta` must hold both
`io.modelcontextprotocol/protocolVersion` and
`io.modelcontextprotocol/clientCapabilities` (absent → `400` + `-32602`), and
the `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` headers must each say
what the body says (missing or contradicting → `400` + `-32020 HeaderMismatch`).
The headers exist so an intermediary can route without parsing the body; the
body is what this server executes, and the comparison is what stops those two
readings from parting company — and the comparison is against the value the
handler will actually act on, decoded exactly as the handler decodes it, because
`encoding/json` matches members case-insensitively and takes the last of a
duplicate pair while a map lookup does neither.
`-32021 MissingRequiredClientCapability` is never emitted: no tool here needs
sampling, elicitation or roots.

**One caveat for anyone putting a gateway in front of `/mcp`.** `Mcp-Name` may
arrive Base64-sentinel encoded (`=?base64?…?=`), and the protocol lets a client
encode *any* value that way, including plain ASCII. This server decodes before
comparing; an intermediary that filters on the raw header without implementing
the sentinel is bypassed by encoding the value. Route on these headers only if
you decode them the same way.

**Every modern result carries `resultType: "complete"` and
`_meta["io.modelcontextprotocol/serverInfo"]`**, and every cacheable one carries
`ttlMs` + `cacheScope`. `server/discover` is `public` — its bytes are the same
for every caller, and a test holds that claim. Every catalog
(`tools/list`, `resources/*`, `prompts/list`) is **`private`**: they are
filtered per passport, and a shared cache entry on a scope-filtered response is
a disclosure that never reaches the server to be audited. A TTL is a freshness
hint, never a permission — every call re-authenticates, so a stale catalog
cannot make a refused call succeed. A `tools/call` result carries no hint at all.

**`resources/list` and `prompts/list` answer empty rather than `-32601`** when
nothing is wired. claude.ai calls both right after `initialize` regardless, and
an unadvertised capability answering "method not found" reads as a broken server
rather than as a legitimate empty catalog. A `resources/read` **not-found**
refusal answers `-32002` in the handshake era and `-32602` in the modern one,
which retired that code; the rest of that method's refusal surface is
era-independent — a missing or empty `uri` is `-32602` in both, and a read that
fails internally is `-32603` in both.

**`GET /mcp` is `405`.** The transport serves `POST` (one JSON-RPC exchange) and
`DELETE` (close the session this passport opened); the GET SSE stream is a later
phase. That is also why the capabilities report
`tools.listChanged: false` — the notification travels on a stream this transport
does not open, so claiming it would promise a message that can never arrive. The
surface really does change per caller, but the honest answer is that this server
cannot announce it.

**A tool result carries the answer twice.** Every registered tool declares an
`outputSchema`, so a successful `tools/call` returns the serialized JSON both in
a `TextContent` block and as `structuredContent` — the same bytes passed through
rather than a re-marshalled copy, so a client comparing the two never finds a
widened integer or a reordered key. What is checked is the **declared schema** —
each tool advertises the exact shape its handler marshals, and a result that
misses it is withheld from `structuredContent` and logged as this server's own
defect rather than served in violation of a promise it just made.

**No sessions, in either era.** `initialize` still answers a handshake-era
client, and it mints no `Mcp-Session-Id`; a presented one is ignored rather than
echoed, and `DELETE /mcp` answers `405` — this server establishes no session, so
there is none to close. The id was never authority (every call re-authenticates
on its Bearer passport), and what it cost was real: it pinned a conversation to
the one process that answered `initialize`.

**Four volume counters instead, per Passport, per window** (`MCP-SESS-*`,
ADR-0092 §6) — the bound the session was implicitly carrying, made explicit and
kept in Redis where every replica reads the same number. Which counter a call
spends is derived from what it already declares, never from a list of tool
names: an egress-flagged tool spends `egress`, a read-only one spends `reads`
per **record** served, anything else spends `writes`, and every admitted call
also spends one of `calls`. Crossing one does one of two things. `reads` and
`writes` are **step-ups**: the call is refused *and* the question — "this agent
has been handed N of its M records for this window; continue?" — goes to the
human who lent the passport, whose approval widens that window by one more
allowance. Nobody else can answer it, not an admin and not the workspace owner:
an agent's ceiling is its lender's authority. `egress` and `calls` are **hard
stops** that no approval lifts and only the window ends. A fifth counter,
`cost`, is soft — it refuses nothing, and says on the answer when this
credential has spent its share of the workspace AI budget.

**Every call re-authenticates.** The binder runs per call, not per session, so
revoking the passport or demoting the granting human takes effect on the very
next `tools/call`, mid-session. A credential the server cannot *reach* a verdict
on answers `503`, never `401` — a 401 would tell a well-behaved client its good
token is bad and turn an outage into mass re-consent.

**How a client gets a `client_id`.** Two ways, and a client that reads the
profile's own priority order picks the first:

- **A Client ID Metadata Document (CIMD)** — the forward path. The `client_id`
  IS an `https` URL with a path, resolving to a JSON document that states its
  own `client_id`, `client_name` and `redirect_uris`. This server fetches it,
  and the fetch is the part worth knowing about: **redirects are not followed**
  (a followed hop is a second URL the caller chose), the address is refused at
  connect time if it resolves anywhere inside the deployment, the body is capped
  at 64 KiB, the timeout is 5s, and the document's own `client_id` must equal
  the URL it came from **byte for byte** — no normalizing, because a normalizer
  is a second reading of one value. A validated document becomes an ordinary
  `oauth_client` row with `created_via = 'cimd'`, so an admin disables, deletes
  and revokes it exactly as they would a registered one, and it is refetched
  when the client's own cache headers say it has gone stale (clamped to between
  5 minutes and 24 hours).
- **Dynamic client registration** (`POST /oauth/register`) — deprecated in the
  profile, and **retained here for the compatibility window** (ADR-0092 §4), so
  a client registered before any of this is not stranded by a revision it never
  asked for. `client_id_metadata_document_supported: true` and
  `registration_endpoint` are both advertised in the authorization-server
  metadata, on purpose.

Either way the consent screen names the **host** the authorization will be sent
back to, and says so again when that host is an address on this computer — a
metadata document can prove what a client calls itself, and cannot prove which
program holds a loopback port.

## Where to go next

- What a human may do, which every agent is capped by:
  [rbac-matrix.md](rbac-matrix.md).
- The gate, the tiers, and what a passport is:
  [explanation/authorization.md](../explanation/authorization.md).
- The reason-act-observe loop that drives these tools from inside the product:
  [explanation/agent-surface.md](../explanation/agent-surface.md).
- What the `agents` module owns and where it sits: [modules.md](modules.md).
