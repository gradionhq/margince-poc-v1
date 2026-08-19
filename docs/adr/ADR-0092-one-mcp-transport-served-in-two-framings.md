# ADR-0092 — There is one MCP transport, and it serves both the modern and the legacy framing

**Status:** Active
**Decided:** 2026-08-07

## The decision

MCP over HTTP to the operator's own service is the only transport. The local
stdio surface and its `cmd/mcp` binary are retired. Every agent — a user's own
assistant and the resident runner alike — arrives over that one door under
OAuth 2.1 with PKCE.

The cost is stated rather than glossed: an operator must run the service for
any inbound agent at all. The stdio surface previously offered a
no-deployment path for one developer at a desk, and that path is withdrawn, not
replaced.

One dispatcher serves two protocol framings and picks per request. A request
that names its own protocol version is parsed as modern; one that arrives
without is parsed as the handshake era. The framing decides how a call is
parsed and never what it may do — there is one admission gate underneath both.

A response whose content depends on the caller's scopes is cached private,
never shared. `tools/list` is filtered by the presenting passport's scopes, so
a shared cache entry would let one agent read the tool surface of a more
privileged one, and that disclosure leaves no audit trail because the second
request never reaches the server.

## Why

The stdio surface authenticated from a static credential written into a file on
the user's machine. The product promises that every call re-authenticates and
that revoking a passport binds mid-session, and a file-borne credential cannot
honour that, because there is no next lookup to fail closed. Keeping a second
door with a weaker identity model made that revocation promise true of one
transport and quietly false of the other.

Nothing is lost operationally. `claude mcp add --transport http <base>/mcp`
walks discovery, dynamic client registration, consent and the token exchange
with no binary to install and no credential to place on disk.

The handshake era pins connection state to one process, so a call cannot land
on any API replica. Retiring the second transport is what made removing that
in-process state a single-caller change instead of a two-identity-model one.

## What it binds in this repository

- `backend/internal/compose/routes.go` mounts `/mcp` on the same origin as
  `/oauth/*` and the discovery documents:
  `/.well-known/oauth-authorization-server`,
  `/.well-known/oauth-protected-resource` and
  `/.well-known/oauth-protected-resource/mcp`.
- `backend/cmd/` holds three binaries: `api`, `worker`, `migrate`. There is
  no `cmd/mcp`. The string `local_stdio` appears nowhere in the tree.
- `backend/internal/modules/agents/modern.go` serves the `2026-07-28` framing;
  `modernProtocolVersion` is the constant.
- `backend/internal/modules/agents/dispatch.go` holds
  `legacyProtocolVersions = []string{"2025-11-25", "2025-06-18"}`. The older
  `2025-03-26` revision is no longer served.
- `backend/internal/modules/agents/httpmcp.go` answers neither era with an
  `Mcp-Session-Id`, so a modern call carries its own state and may land on any
  API replica.
- Dynamic client registration is served at `/oauth/register`
  (`backend/internal/compose/oauthedge.go`), retained for the compatibility
  window.
- `backend/internal/modules/identity/oauth_cimd.go` implements Client ID
  Metadata Documents, the forward path: a modern client identifies itself with
  an HTTPS URL resolving to its own metadata document rather than registering
  first. The file's own comments name the SSRF guards each fetch rule closes.
- The integration lane exercises `server/discover` end to end in
  `backend/internal/compose/integration/agentaccess/`
  (`mcp_modernframing_integration_test.go`), so the modern framing is covered
  alongside the legacy one.

## History

Adopted from the retired specification, decided 2026-08-07. Rewritten in plain
language 2026-08-19. Everything the source decided has shipped.

The source insists the per-passport volume counters be live before the session
registry is removed, because the session was silently acting as the volume
bound. Removing a bound and adding its replacement in the same change is how a
surface ends up briefly unbounded in production.
