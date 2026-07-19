# Run the MCP server

`cmd/mcp` serves the one governed agent tool surface over two
transports. Both re-authenticate every call and re-load the granting
human's RBAC, so revoking a passport binds mid-session.

You need a passport token first — see
[mint-a-passport.md](mint-a-passport.md).

## A1: stdio (the default)

The passport token comes from the **environment**, never a flag — argv
is world-readable in `ps`:

```sh
MARGINCE_PASSPORT_TOKEN=mgp_… \
MARGINCE_DSN='postgres://margince_app:…@localhost:55432/margince' \
mcp
```

There is no workspace flag: one installation serves one organization
(A107/ADR-0061), so the process binds the bootstrapped installation's
workspace itself at boot — against a pre-bootstrap database it refuses
to start ("start the API with a margince.yaml first").

The process speaks MCP JSON-RPC on stdin/stdout; diagnostics go to
stderr (stdout belongs to the protocol). A dead or revoked token fails
loudly at boot, not on the first tool call.

An MCP client config looks like:

```json
{
  "command": "mcp",
  "env": {
    "MARGINCE_PASSPORT_TOKEN": "mgp_…",
    "MARGINCE_DSN": "postgres://…"
  }
}
```

During development, run it straight from the repo:

```sh
cd backend && MARGINCE_PASSPORT_TOKEN=mgp_… go run ./cmd/mcp \
  --dsn 'postgres://margince_app:margince_app_dev@localhost:55432/margince'
```

## A2: hosted (streamable HTTP)

```sh
mcp --listen :8081 --dsn 'postgres://…'
```

serves one JSON-RPC exchange per `POST /mcp`. Tokens arrive as Bearer
credentials minted by the `/oauth` flow (they are passport tokens);
`--workspace` is not required per-process on this transport.

## Governance on the wire

Whatever the transport, every tool call passes the same admission gate
as HTTP: scope ∧ seat ∧ RBAC ∧ autonomy tier. 🟢 tools execute and are
audited; 🟡 tools (send, merge, archive, deal close, …) stage an
approval a human must decide in the inbox. See
[../explanation/architecture.md](../explanation/architecture.md).

All flags: [../reference/configuration.md](../reference/configuration.md).
