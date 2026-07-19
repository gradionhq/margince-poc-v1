# Mint an Agent Seat Passport

A passport is the credential an AI agent uses on both agent transports
(MCP and REST). It is scoped, expiring, revocable, and bound to the
human who minted it — the agent never has more rights than that human,
and the human's seat + RBAC are re-derived on every call, so revocation
binds mid-session.

## Prerequisites

A running API (`make dev`) and a browser/API session in the target
workspace. Passport issuance is session-authed and human-only: an agent
cannot mint credentials.

## Mint

```sh
curl -X POST http://localhost:8080/v1/passports \
  --cookie 'crm_session=<your session>' \
  -H 'Content-Type: application/json' \
  -d '{"label": "Claude Desktop", "scopes": ["read", "write"], "ttl_hours": 720}'
```

The response contains the raw `mgp_`-prefixed bearer token **once** —
only its SHA-256 is stored, so copy it now. Scopes are the verb classes
read/draft/write/send/enrich (effective authority is always scopes ∩
the granting human's RBAC); `ttl_hours` defaults to 720 (30 days) and
is capped at 2160 (90 days).

## Use

- **MCP (stdio)**: pass it as `MARGINCE_PASSPORT_TOKEN` to `cmd/mcp` —
  see [run-the-mcp-server.md](run-the-mcp-server.md). Env, not argv:
  argv is visible in the process list.
- **REST**: send it as `Authorization: Bearer mgp_…` against the same
  `/v1` surface. The identical governance applies on both transports: 🟢
  mutations execute with agent-stamped provenance, 🟡 mutations stage an
  approval, human-only governance routes refuse agent principals.

## Revoke

Delete the passport over the API (`DELETE /v1/passports/{id}`) or in the
web UI. Because admission re-authenticates every call, a revoked
passport stops working immediately — including for an MCP session that
is already connected.
