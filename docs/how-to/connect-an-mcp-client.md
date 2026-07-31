# Connect an MCP client

The api serves the one governed agent tool surface at `/mcp` on its own
origin, alongside `/oauth/*` and the discovery documents.

RFC 9728 does permit an authorization server on a different origin from the
protected resource, so the co-location here is *this deployment's* decision,
not a protocol requirement: one origin means one thing to configure, one
certificate, and a discovery chain with nothing to cross-reference. The retired
`cmd/mcp` binary served the transport from a second origin while hosting none
of those documents, which is what made it not worth keeping (SCR-9).

Every call re-authenticates and re-loads the granting human's RBAC, so
revoking a passport, disabling the client, or deactivating the human binds
mid-session rather than at the next reconnect.

## Turn the connector on

It is **off by default**: enabling it exposes the OAuth authorization server
and the tool surface to the internet.

```yaml
# config/margince.yaml
mcp:
  connector_enabled: true
```

It also requires `--public-base-url` (or `MARGINCE_PUBLIC_BASE_URL`) as a
**bare origin** — no path, query, or fragment. The advertised MCP resource is
that value with `/mcp` appended, and the api refuses to boot on a value it
cannot publish. `make dev` passes the flag already, so a local stack just
works.

## Connect

The client discovers everything else — authorization server, scopes, the
consent screen — from the URL alone:

```bash
claude mcp add --transport http margince http://localhost:8080/mcp
```

For a deployment, use `<public-base-url>/mcp`. The first call answers `401`
with an RFC 9728 `WWW-Authenticate` pointer at
`/.well-known/oauth-protected-resource`; the client follows it, registers
itself (DCR), and opens the consent screen. Approving it mints a passport
bound to your own seat and RBAC — an agent can never exceed the human who
granted it.

The default scope hint is the conservative `read draft`; widen it on the
consent screen if the client needs to write or send.

## A passport as a REST credential

The same token is a REST Bearer credential, governed identically (ADR-0055):
🟢 tools auto-execute, 🟡 ones stage for confirm-first approval, all capped by
the granting human's live seat and RBAC. See
[mint-a-passport.md](mint-a-passport.md) to issue one directly.

## Inspect the surface

The [MCP Inspector](https://github.com/modelcontextprotocol/inspector) speaks
streamable HTTP, so point it at the running stack and let it do the OAuth
handshake in the browser:

```bash
npx @modelcontextprotocol/inspector
# then, in the UI: Transport = "Streamable HTTP", URL = http://localhost:8080/mcp
```

`tools/list` shows only what the presenting passport's scopes could actually
invoke — a read-only passport does not see the write tools — so the surface an
inspector reports is the surface that client really has.

## Turn it off

Setting `connector_enabled: false` (or removing the block) removes the whole
route group — `/mcp`, all four `/oauth` endpoints, and both well-knowns —
behind `404`s a prober cannot tell apart. Existing credentials stop working
because the routes that honour them no longer exist.
