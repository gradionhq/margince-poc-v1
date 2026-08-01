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

The **code** default is off: an installation whose deployment file carries no
`mcp` block serves none of these routes, and each answers `404`. The shipped
[`config/margince.example.yaml`](../../config/margince.example.yaml) declares
the gate on and `make dev` seeds `config/margince.yaml` from it, so **a local
stack serves `/mcp` with no edit**. A deployment writing its own file opts in
explicitly:

```yaml
# config/margince.yaml
mcp:
  connector_enabled: true
```

The gate also requires `--public-base-url` (or `MARGINCE_PUBLIC_BASE_URL`) as a
**bare origin** — no path, query, or fragment. The advertised MCP resource is
that value with `/mcp` appended, and the api **refuses to boot** on the gate
without it: the audience a token is checked against and the resource clients
discover are deployment decisions, never derived from the request `Host`. So an
installation that copies the example config cannot serve the surface by
accident — it fails loudly on first start. `make dev` passes the flag
unconditionally, which is why the local stack just works.

## Connect

The client discovers everything else — authorization server, scopes, the
consent screen — from the URL alone:

```bash
claude mcp add --transport http margince http://localhost:8080/mcp
```

For a deployment, use `<public-base-url>/mcp`. The first call answers `401`
with an RFC 9728 `WWW-Authenticate` pointer at
`/.well-known/oauth-protected-resource`; the client follows it, registers
itself (DCR), and opens the consent screen. If nobody is signed in to Margince
in that browser, the sign-in screen comes first and the consent screen follows
it — the pending request survives the sign-in.

The consent screen does not grant a client whatever it asked for. It asks the
signed-in human to **lend one of their own existing agent passports** — the
connection's scopes come from that passport, intersected with what the client
requested, so the client can end up with **less** than the passport carries,
never more. There is a real Deny too: it sends the client `access_denied`
instead of leaving it hanging.

**A human with no passport yet cannot approve anything.** The screen shows a
guide instead of an approve control — mint a passport in Settings (the
"AI & autonomy" tab) and it brings you back to finish connecting. In practice
this means **`claude mcp add` no longer completes unattended for a brand-new
account**: it stops at the guide until a passport exists. See
[mint-a-passport.md](mint-a-passport.md) to create one ahead of time.

Once a passport is lent, the connection is bound to that passport's own seat
and RBAC — an agent can never exceed the human who granted it.

## What a connection actually receives

Two independent decisions meet, and the grant is their **intersection**:

1. **What the client requests.** The client puts a `scope` parameter on the
   authorize URL. It learns what it may name there from `scopes_supported` on
   `/.well-known/oauth-protected-resource` (the five record verbs: `read`,
   `draft`, `write`, `send`, `enrich`) and from the conservative
   `scope="read draft"` hint on the `401` challenge. It is free to ask for
   less, and free to ask for nothing.
2. **What the human lends.** The passport chosen on the consent screen is the
   ceiling. The connection can come out with less than that passport carries,
   never with more.

A client asking for less than the passport allows is therefore capped at what
it asked for, and **a client that names no scope at all receives `read` only**,
however broad the passport lent. An absent `scope` parameter is not "everything
the passport allows": the authorize endpoint reads it as nothing requested and
falls back to the least authority, because the alternative — a zero-scope
connection — would fail every tool call it ever made.

That fallback is what a reader should expect from `claude mcp add` today: it
sends no `scope`, so a fresh connection is read-only — the read tools only, with
no `create_record`, `update_record`, or `send_email` among them. Lending a
`read draft write send enrich` passport does not widen it, because the ceiling
is not the request. Widening it is the client's move, not the passport's: a
client that names the verbs it needs (the MCP Inspector lets you type them) gets
them, up to the lent passport.

Two ways to see what a connection has, rather than assume:

- **Before approving**, on the consent screen: the chips under the selected
  passport are its scopes, and every chip this connection will *not* receive is
  dimmed and labelled "not granted". The solid chips are the grant.
- **After connecting**, from the connection itself: `tools/list` returns only
  the tools the granted scopes can invoke, so the tool list is the proof. A
  connection that lists no write tool did not receive `write`.

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

`tools/list` shows only what the connection's **granted** scopes could actually
invoke — not what the lent passport carries — so the surface an inspector
reports is the surface that client really has. The Inspector also lets you type
the scopes it requests, which is how to confirm that a wider grant is available
and that only the client's request was narrowing it.

## Turn it off

Setting `connector_enabled: false` (or removing the block) removes the whole
route group — `/mcp`, all four `/oauth` endpoints, and both well-knowns —
behind `404`s a prober cannot tell apart. Existing credentials stop working
because the routes that honour them no longer exist.
