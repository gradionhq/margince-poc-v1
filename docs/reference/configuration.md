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
| `--addr` | — | `:8080` | listen address |
| `--redis` | `MARGINCE_REDIS` | `localhost:56379` | Redis address (event bus) |
| `--inline-relay` | — | `true` | run the outbox relay in-process; set `false` when `cmd/worker` runs it |
| `--ai-routing` | `MARGINCE_AI_ROUTING` | — | path to `ai-routing.yaml`; enables the cold-start read-back |
| `--ai-fake` | — | `false` | offline fake model (dev/test only) |
| `--public-base-url` | `MARGINCE_PUBLIC_BASE_URL` | — | canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe / preference center); required to send marketing mail — a send refuses rather than derive the token-bearing link from the request Host |

With `--inline-relay` (the default) an unreachable Redis fails the boot:
without a relay every committed write would strand its outbox row.

Operational endpoints (served next to `/v1`):

- `/healthz` — liveness: a dumb 200 (a database outage must not
  restart-loop the process).
- `/readyz` — readiness: every dependency probe (Postgres; Redis too
  when the relay is inline) must pass within 2s, else 503 naming the
  unready dependency.
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
(kept parseable by a fitness test in `modules/ai`);
`backend/ai-routing.yaml` is the terse dev default the repo ships.
