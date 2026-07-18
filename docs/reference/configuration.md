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
| `--config` | `MARGINCE_CONFIG` | `margince.yaml` | the deployment configuration file (A107/ADR-0061: bootstrap + auth — organization, bootstrap_admin, seeds, email; strict decoding, secrets as `*_file` references). A missing file boots an existing installation; bootstrapping an empty database requires `organization` + `bootstrap_admin` |
| `--schema-dsn` | `MARGINCE_SCHEMA_DSN` | — | Postgres DSN, **owner** role, for the customfields runtime-DDL pool; unset = `createCustomField`/`updateCustomFieldOptions` answer 501 |
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
| `--config` | `MARGINCE_CONFIG` | `margince.yaml` | the deployment configuration file; the worker reads it for the `ai.capture_payloads` posture the Surface-B runner honors (capture applies to **both** the api and worker roles — the worker runs the richest content source, the agent runs). A missing file boots with capture off |
| `--redis` | `MARGINCE_REDIS` | `localhost:56379` | Redis address (event bus) |
| `--ai-routing` | `MARGINCE_AI_ROUTING` | — | path to `ai-routing.yaml`; enables the Surface-B runner + embeddings |
| `--ai-fake` | — | `false` | run the Surface-B runner on the offline fake model |
| `--runner-interval` | — | `30s` | Surface-B scheduler tick |
| `--retention-interval` | — | `24h` | retention evaluator pass interval |
| `--time-scan-interval` | — | `1h` | clock-trigger automation scan interval (`no_activity_reminder` et al. — the River periodic job `TimeScanner.Scan` drives) |
| `--deepread-max-pages` | `MARGINCE_DEEPREAD_MAX_PAGES` | `0` (= built-in 40) | deep-read crawl page cap |
| `--deepread-max-bytes` | `MARGINCE_DEEPREAD_MAX_BYTES` | `0` (= built-in 32 MiB) | deep-read crawl aggregate byte cap |
| `--deepread-wall` | `MARGINCE_DEEPREAD_WALL` | `0` (= built-in 4m) | deep-read crawl wall clock |

### `worker siteread` — the deep-read debug loop (no DB)

`worker siteread <url…> [--urls-file f]` runs the whole crawl→extract→merge
pipeline in memory — no Postgres, no Redis, no staging — and prints every
intermediate: pages with skip reasons, every extracted field/fact with its
evidence, every finding the gate DROPPED (with why), merge decisions, and
per-model-call token/latency telemetry. Exactly one model selection is
required: `--ai-routing <yaml>`, `--model provider:model` (e.g.
`anthropic:claude-opus-4-8` — needs the provider's BYOK env key), or
`--ai-fake` (crawl dry-run). `--max-pages/--max-bytes/--wall` override the
caps per run; `--json <path|->` writes a diffable machine-readable report;
`--dump-pages <dir>` saves each page's reduced text.

Extraction quality is MODEL-SENSITIVE: the no-guess gate demands verbatim
evidence quotes, and cheap-tier models paraphrase them away. The
`site_extract` task therefore routes premium-first, and the premium tier
needs a mid-tier model at minimum — for Anthropic that is Sonnet-class or
better (Haiku loses fields Sonnet keeps). Judge any candidate binding
against the pinned quality floor: `make -C backend e2e-siteread` with
`MARGINCE_E2E_MODEL=provider:model` (paid, network E2E vs gradion.com —
a different model must do the same or better to pass).

Without a declared model (`--ai-routing`/`--ai-fake`) the runner and the
embedding lane simply do not start; the relay, retention, the event-triggered
workflow dispatch (`cg:workflows`), and the clock time-scan always run.
Shutdown is graceful: in-flight subscriber handlers finish their ack before
the process exits.

## Object storage (api, worker) — attachments

Env-only, shared by both roles; secrets never appear on the command line
(argv is world-readable). Leave `MARGINCE_BLOBSTORE_ENDPOINT` unset and the
`/attachments` endpoints answer 501; set it to enable them.
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
an opaque, workspace-scoped `credential_ref`, never the credential bytes.
Leave `MARGINCE_KEYVAULT_ROOT_KEY` unset and the vault is
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
`createCustomField` and `updateCustomFieldOptions`: the
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
| `MARGINCE_ENV` | api (identity handlers) | `dev` enables dev-only trust switches. The Makefile exports `dev`; production must not set it. |
| `MARGINCE_TEST_DSN`, `MARGINCE_TEST_APP_DSN`, `MARGINCE_TEST_REDIS` | integration tests | owner DSN / app-role DSN / Redis address for the real-Postgres lane; exported by the Makefile. The lane runs on its own `_test` namespace (the `margince_test` DB, never the dev `margince` DB), so it can run alongside `make dev`. |
| `MARGINCE_TEST_REDIS_DB` | integration tests | Redis logical db for the lane (default 15). db 0 is reserved for a running `make dev`; a valid value is 1..15, and the parallel runner assigns one per package so concurrent packages never share a stream. Out-of-range fails loudly. |

The **deployment configuration** (`--config`, default `margince.yaml`) is
seeded the same way for local dev. The annotated reference is
[`config/margince.example.yaml`](../../config/margince.example.yaml); `make dev`
copies it to a gitignored `config/margince.yaml` on first run and then
**leaves it** (create-if-missing / leave-if-exists, exactly like
`config/ai-routing.yaml` below), so an engineer's edits — organization,
`bootstrap_admin`, or the `ai.capture_payloads` posture — persist across
`make dev-stop` / `make dev` rather than being regenerated each boot. The
admin `password_file` it references (`config/margince-admin-password`) is
seeded alongside on first run; both are gitignored. `--config` reaches
**both** the api and worker, so a posture like `ai.capture_payloads` applies
to every role. Delete `config/margince.yaml` and re-run `make dev` to reset.

Model credentials (BYOK cloud tiers) are configured in
`ai-routing.yaml`, not through binary flags. The annotated reference is
[`config/ai-routing.example.yaml`](../../config/ai-routing.example.yaml)
(kept parseable by the fitness test in
`backend/internal/modules/ai/exampleconfig_test.go`). `make install` /
`make dev` copy it to a gitignored `config/ai-routing.yaml` — the
per-engineer local config each engineer edits to bind their own models;
delete it and re-run either target to reset.

The providers a binding may name, and what each requires. A cloud provider's
BYOK key is **read from an environment variable** at boot — the routing file
names only the provider (a stray `api_key:` there is a startup error):

| provider | key env var | `base_url` | notes |
|---|---|---|---|
| `fake` | — | — | offline deterministic stub (dev/test) |
| `ollama` | — | optional (default `localhost:11434`) | local; sovereign-eligible |
| `vllm` | — | optional (default `localhost:8000`) | local; sovereign-eligible |
| `anthropic` | `ANTHROPIC_API_KEY` | optional (default `api.anthropic.com`) | BYOK cloud |
| `openai_compatible` | `OPENAI_COMPATIBLE_API_KEY` | **required** | BYOK cloud, generic OpenAI wire (OpenAI, Mistral, DeepSeek, Groq, Together, OpenRouter, …) |
| `openai` | `OPENAI_API_KEY` | optional (default `api.openai.com`) | BYOK cloud, native Responses API |
| `gemini` | `GEMINI_API_KEY` | optional (default `generativelanguage.googleapis.com/v1beta`) | BYOK cloud, native `generateContent` |

`base_url` for the OpenAI-wire providers (`openai_compatible`, `openai`, and
`vllm`) is the vendor **host root with no version segment** — the adapter
appends `/v1/chat/completions` (or `/v1/responses`), so a base ending in `/v1`
would double it (`…/v1/v1/…` → 404). Use `https://api.mistral.ai`, not
`https://api.mistral.ai/v1`. `gemini` is the mirror: its default base keeps the
`/v1beta` segment and the paths are version-relative.

A cloud binding is refused at startup under `profile: sovereign` (zero
egress by construction). An editor with a YAML language server picks up
[`config/ai-routing.schema.json`](../../config/ai-routing.schema.json)
(referenced from the example's first line) for autocomplete, enum
validation, and hover docs; the parser remains the sole runtime authority.

Two operator gotchas, verified against current vendor docs:

1. **`openai_compatible`'s embeddings lane 404s on OpenRouter, Groq, and
   DeepSeek** — they serve chat only. Bind `embeddings:` to a vendor that
   has the lane (OpenAI, Mistral, a Gemini-compat layer, Together) or a
   local model (ollama `bge-m3`).
2. **Vendor `-latest` model aliases drift and some are being deprecated**
   (e.g. Mistral). Pin an explicit versioned id, or resolve via the
   vendor's `/models` endpoint, rather than hardcoding an alias.
