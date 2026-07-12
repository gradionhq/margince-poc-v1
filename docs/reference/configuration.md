# Configuration reference

Four process-role binaries live under `backend/cmd/`. Configuration is
flags; where a flag has an environment fallback it is listed. An empty
required value is a boot error, as is an invalid `--log-level` /
`--log-format`.

## Common log flags (api, worker, mcp)

| Flag | Env | Default | Values |
|---|---|---|---|
| `--log-level` | `MARGINCE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `--log-format` | `MARGINCE_LOG_FORMAT` | `text` | `text` (slog text), `json` |

api and worker log to stdout; mcp logs to **stderr** (stdout is the
stdio protocol channel). Log lines carry the per-request
`correlation_id` via the correlation slog wrapper.

## cmd/api — the HTTP process role

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--dsn` | `MARGINCE_DSN` | — (required) | Postgres DSN, runtime app role |
| `--schema-dsn` | `MARGINCE_SCHEMA_DSN` | — | Postgres DSN, **owner** role, for the customfields runtime-DDL pool; unset = `createCustomField`/`updateCustomFieldOptions` answer 501 (decisions/0024) |
| `--addr` | — | `:8080` | listen address |
| `--redis` | `MARGINCE_REDIS` | `localhost:56379` | Redis address (event bus) |
| `--inline-relay` | — | `true` | run the outbox relay in-process; set `false` when `cmd/worker` runs it |
| `--ai-routing` | `MARGINCE_AI_ROUTING` | — | path to `ai-routing.yaml`; enables the cold-start read-back, per-org enrichment, the Morning-Brief L2 re-order, and AI-drafted offer regeneration |
| `--ai-fake` | — | `false` | offline fake model (dev/test only); drives the same AI surfaces as `--ai-routing` |
| `--public-base-url` | `MARGINCE_PUBLIC_BASE_URL` | — | canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe / preference center); required to send marketing mail — a send refuses rather than derive the token-bearing link from the request Host |

With `--inline-relay` (the default) an unreachable Redis fails the boot:
without a relay every committed write would strand its outbox row.

Operational endpoints (served next to `/v1`):

- `/healthz` — liveness: a dumb 200 (a database outage must not
  restart-loop the process).
- `/readyz` — readiness: every dependency probe (Postgres; Redis too
  when the relay is inline; the object store when a blobstore is
  configured; the secret vault when a keyvault is configured; the
  customfields schema pool when `--schema-dsn` is set) must pass within
  2s, else 503 naming the unready dependency.
- `/metrics` — Prometheus text format: `margince_outbox_unpublished`,
  `margince_relay_published_total`, `margince_pgxpool_conns{state=…}`.

## cmd/worker — the background process role

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--dsn` | `MARGINCE_DSN` | — (required) | Postgres DSN, runtime app role |
| `--redis` | `MARGINCE_REDIS` | `localhost:56379` | Redis address (event bus) |
| `--ai-routing` | `MARGINCE_AI_ROUTING` | — | path to `ai-routing.yaml`; enables the Surface-B runner + embeddings |
| `--ai-fake` | — | `false` | run the Surface-B runner on the offline fake model |
| `--runner-interval` | — | `30s` | Surface-B scheduler tick |
| `--retention-interval` | — | `24h` | retention evaluator pass interval |

Without a declared model (`--ai-routing`/`--ai-fake`) the runner and the
embedding lane simply do not start; the relay, retention, and workflow
lanes always run. Shutdown is graceful: in-flight subscriber handlers
finish their ack before the process exits.

## Object storage (api, worker) — attachments

Env-only, shared by both roles; secrets never appear on the command line
(argv is world-readable). Leave `MARGINCE_BLOBSTORE_ENDPOINT` unset and the
`/attachments` endpoints answer 501; set it to enable them (decisions/0022).
If attachment rows already exist (uploaded while a store was configured) but
the erasing process has none, Art. 17 erasure **fails and rolls back** rather
than stranding the bytes — it stays retryable until a store is configured. The bucket is created on first connect,
and the store tolerates a still-starting backend with a bounded retry.

| Env | Default | Meaning |
|---|---|---|
| `MARGINCE_BLOBSTORE_ENDPOINT` | — | S3/MinIO `host:port`; set to enable attachments |
| `MARGINCE_BLOBSTORE_ACCESS_KEY` | — | access key |
| `MARGINCE_BLOBSTORE_SECRET_KEY` | — | secret key |
| `MARGINCE_BLOBSTORE_BUCKET` | — | bucket name (created on first connect) |
| `MARGINCE_BLOBSTORE_REGION` | `us-east-1` | region |
| `MARGINCE_BLOBSTORE_USE_SSL` | `false` | `true` for TLS to the store |

## Secret vault (api, worker) — connector credentials

Env-only, shared by both roles; the root key never appears on the command
line (argv is world-readable) or in any log or error. A connector credential
is sealed with AES-256-GCM under this key and stored as ciphertext in the
operational `vault_secret` table; the `connector_connection` row carries only
an opaque, workspace-scoped `credential_ref`, never the credential bytes
(decisions/0023). Leave `MARGINCE_KEYVAULT_ROOT_KEY` unset and the vault is
absent: the transient one-shot IMAP pull (which persists no credential) still
works, and the persisting paths (Connect seals, Sync resolves) refuse loudly
rather than store a credential in the clear. Set it and the api gains the
`/readyz` keyvault probe and the vault-backed path, and the worker migrates
any legacy `auth`-bytea rows onto the vault at boot (idempotent). A key that
is SET but not exactly 32 bytes (base64-decoded) is a boot error — never a
silent fallback.

| Env | Default | Meaning |
|---|---|---|
| `MARGINCE_KEYVAULT_ROOT_KEY` | — | base64 (std) of 32 bytes; set to enable the vault. Generate: `openssl rand -base64 32` |

## Custom-field schema pool (api) — runtime DDL

`--schema-dsn`/`MARGINCE_SCHEMA_DSN` is the api-only owner-role DSN behind
`createCustomField` and `updateCustomFieldOptions` (decisions/0024): the
customfields engine's single chokepoint for a runtime `ALTER TABLE`. Leave
it unset and both operations answer `501` (`ErrSchemaChangesUnavailable`)
rather than nil-derefing a pool that was never mounted — `renameCustomField`,
`retireCustomField`, and `listCustomFields` need no schema pool and always
work. When set, the api opens a **second** pgxpool sized to `pool_max_conns=3`
(unless the DSN already sets `pool_max_conns` itself, matching
`database.NewPool`'s DSN-wins-over-default rule): every schema change is
serialized behind a transaction-scoped advisory lock keyed on the target
table, so this pool never runs more than one `ALTER` against the same
table at a time — concurrent `ALTER`s against different tables are not
serialized against each other, just against races on their own table — a
small, deliberate footprint next to the app pool's `MaxConns=16` default. The
transaction runs the DDL as the owner role, then downgrades itself
(`SET LOCAL ROLE margince_app`) before the catalog/audit write, so the
credential this DSN names must be the same owner role `cmd/migrate` uses.
Configured, it also gains the api's `/readyz` `customfields-schema-pool`
probe.

## cmd/mcp — the agent tool surface

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--dsn` | `MARGINCE_DSN` | — (required) | Postgres DSN, runtime app role |
| `--workspace` | `MARGINCE_WORKSPACE` | — (required for stdio) | workspace slug the passport belongs to |
| `--listen` | — | — | serve the hosted A2 transport on this address instead of stdio |

The stdio transport additionally requires the env var
**`MARGINCE_PASSPORT_TOKEN`** (`mgp_…`, minted via `POST /v1/passports`).
It is deliberately not a flag: argv is world-readable.

## cmd/migrate — schema migrations

```
migrate <up|down> --dsn <owner-dsn> [--steps n]
```

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--dsn` | `MARGINCE_DSN` | — (required) | Postgres DSN, **owner** role |
| `--steps` | — | `1` | migrations to revert (`down` only) |

## Other environment variables

| Var | Used by | Meaning |
|---|---|---|
| `MARGINCE_ENV` | api (identity handlers) | `dev` enables dev-only trust switches (the `X-Workspace-Slug` header). The Makefile exports `dev`; production must not set it. |
| `MARGINCE_TEST_DSN`, `MARGINCE_TEST_APP_DSN`, `MARGINCE_TEST_REDIS` | integration tests | owner DSN / app-role DSN / Redis address for the real-Postgres lane; exported by the Makefile for the dev containers. |

Model credentials (BYOK cloud tiers) are configured in
`ai-routing.yaml`, not through binary flags. The annotated reference is
[`config/ai-routing.example.yaml`](../../config/ai-routing.example.yaml)
(kept parseable by the fitness test in
`backend/internal/modules/ai/exampleconfig_test.go`);
`backend/ai-routing.yaml` is the terse dev default the repo ships.
