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
| `--config` | `MARGINCE_CONFIG` | `margince.yaml` | the deployment configuration file (bootstrap + auth — organization, bootstrap_admin, seeds, email; strict decoding, secrets as `*_file` references). A missing file boots an existing installation; bootstrapping an empty database requires `organization` + `bootstrap_admin` |
| `--schema-dsn` | `MARGINCE_SCHEMA_DSN` | — | Postgres DSN, **owner** role, for the customfields runtime-DDL pool; unset = `createCustomField`/`updateCustomFieldOptions` answer 501 |
| `--addr` | — | `:8080` | listen address |
| `--redis` | `MARGINCE_REDIS` | `localhost:56379` | Redis address (event bus) |
| `--inline-relay` | — | `true` | run the outbox relay in-process; set `false` when `cmd/worker` runs it |
| `--webhook-key` | `MARGINCE_WEBHOOK_KEY` | — | base64 32-byte key sealing outbound-webhook signing secrets at rest; unset = the mutating `/webhook-subscriptions` paths (create/rotate, replay) answer 503, never an unsigned fallback; the read surface still lists |
| `--ai-routing` | `MARGINCE_AI_ROUTING` | — | path to `ai-routing.yaml`; enables the cold-start read-back, per-org enrichment, the Morning-Brief L2 re-order, and AI-drafted offer regeneration |
| `--ai-fake` | — | `false` | offline fake model (dev/test only); drives the same AI surfaces as `--ai-routing` |
| `--public-base-url` | `MARGINCE_PUBLIC_BASE_URL` | — | canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe / preference center); required to send marketing mail — a send refuses rather than derive the token-bearing link from the request Host — and for the Gmail/Graph OAuth callback |
| — (env-only) | `MARGINCE_OVERLAY_BACKFILL_LIMIT` | `0` (uncapped) | same knob `cmd/worker` reads (below) — `cmd/api` also boots on it (an invalid value is a boot error here too) so the on-connect/Connect-time seeding path sees the same cap the periodic sweep does |
| `--oauth-access-token-ttl` | `MARGINCE_OAUTH_ACCESS_TOKEN_TTL` | `0` (= the passport default, 720h / 30 days) | lifetime of the access token the MCP connector's OAuth handshake mints. That token IS an Agent Seat Passport, so unset it inherits the 30-day passport default, while connector norms are ~15 minutes plus refresh; set e.g. `15m` to run those norms without a code change — the refresh-rotation machinery is what makes a short lifetime cheap for a client. It applies to **both** mints of a connection's life, the code exchange and every rotation. Maximum `2160h` (90 days, the mint's own ceiling); an out-of-range or non-duration value is a boot error, never a silent default |

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
  `margince_relay_published_total`, `margince_pgxpool_conns{state=…}`, the
  AI router's counters, the overlay sync-health section, and the
  **job-runtime section** below.
- `GET /v1/admin/job-health` — the per-workspace read of the same job
  table, for an admin rather than a scrape. See
  [Reading the job surfaces](#reading-the-job-surfaces).
- `/mcp` plus `/oauth/*` and the RFC 8414/9728 discovery documents
  (`/.well-known/oauth-authorization-server`,
  `/.well-known/oauth-protected-resource` and its `/mcp` suffixed form) —
  the remote MCP connector, mounted as ONE group only when the
  deployment file sets `mcp.connector_enabled: true`. They share the api
  origin because RFC 9728 discovery is a chain rooted at the resource
  server's 401, which a split origin breaks. The gate also requires
  `--public-base-url`: it is a boot error without one, because the
  advertised resource is an audience decision and must never be derived
  from the request `Host`. With the gate off — the code default — none of
  those routes exists and each answers 404, so an installation that has
  not declared the connector exposes no client registration and no
  token endpoint. The shipped example config declares the gate on, which
  is why a `make dev` stack serves the connector with no edit; the boot
  error is what keeps that a local convenience rather than an accidental
  exposure.

  Both discovery documents advertise `scopes_supported`, derived from the
  one closed passport vocabulary: the protected resource names the five
  record verbs (`read`, `draft`, `write`, `send`, `enrich`), and the
  authorization server names those plus `offline_access`, which buys token
  lifetime rather than access to a record. What a connection is granted is the
  passport the human lent, not what the client requested — these documents state
  the vocabulary a client may name, they do not bound the grant.

### Reading the job surfaces

Two readers over one table, `river_job`, answering two different questions.
Both are served by `cmd/api`, and both are read at request time rather than
counted in process: the job table is fleet-wide, so a counter kept inside
`cmd/worker` would be invisible to every scrape of the api while the api's own
copy reported a truthful-looking zero. That stays true — the worker never
re-serves a job-table gauge, and `--observe-addr` below is about the process,
not the fleet.

**`/metrics` — is a queue growing?** Nine gauge families over the job table:

| Family | Labels | Meaning |
|---|---|---|
| `margince_job_queue_depth` | `queue`, `workspace_id` | available + scheduled + retryable + pending — work nobody has done yet (OPS-MET-2) |
| `margince_job_running` | `queue`, `workspace_id` | currently executing |
| `margince_job_discarded` | `kind`, `workspace_id` | every attempt spent; will never run without intervention |
| `margince_job_cancelled` | `kind`, `workspace_id` | stopped deliberately, attempts unspent — counted apart from discarded because the operator story differs, not because it is less dead. The sweep pair counts either as a workspace missed |
| `margince_job_oldest_queued_age_seconds` | `queue`, `workspace_id` | how long the oldest runnable-and-unclaimed job has waited |
| `margince_sweep_workspaces_total` | `sweep` | workspaces with a surviving child of that fleet pass |
| `margince_sweep_workspaces_failed` | `sweep` | those whose MOST RECENT child is discarded or cancelled |
| `margince_sweep_units_total` | `sweep`, `unit` | the same reading one grain down, for the dispatchers that fan out per **connection** or per **build**: units with a surviving child |
| `margince_sweep_units_failed` | `sweep`, `unit` | those whose MOST RECENT child is discarded or cancelled |

The last two exist because the workspace pair counts each workspace once, and
four dispatchers fan out below that grain. They report **only** the kinds whose
declared `fan_out_unit` is finer than a workspace — for the other twenty the
unit *is* the workspace, so the two pairs would carry the same numbers.

The two families **overlap rather than partition**: a per-connection kind is
reported by both, at two grains, because its rows carry a workspace id as well
as a connection id. That is the point — the coarse reading answers *is every
tenant covered*, the fine one answers *did every unit of the pass run* — but it
means **never sum them**. `margince_sweep_units_failed{sweep="telegram_poll"}`
and `margince_sweep_workspaces_failed{sweep="telegram_poll"}` can both be
non-zero for one dead connection. Alert on whichever grain you mean; use
`... > 0 or ... > 0` if you want either to page you, never `+`.

A tenth, `margince_job_unrecognised_state{state,queue,workspace_id}`,
appears **only when it has something to report**: work sitting in a state
this exposition does not classify. It is a signal to investigate, not a
series to graph, which is why it is absent rather than zero the rest of the
time.

Two more families read the **declaration** — `backend/api/jobs.yaml`, where
every job kind this build runs is declared — rather than the job table. Every
gauge above is a projection of `river_job` at scrape time, so it can only name
a kind that happens to have rows, and that collapses three different situations
into one absence: a declared kind running idle, a kind nobody ever wired, and
rows of a kind the contract no longer declares.

| Family | Labels | Meaning |
|---|---|---|
| `margince_job_declared_info` | `kind`, `role`, `queue`, `fan_out_unit`, `timeout_seconds` | one series per DECLARED kind, valued 1 — the catalogue, written whether or not the job table holds a row of that kind |
| `margince_job_unrecognised_kind` | `kind` | rows whose kind the contract does not declare — a retired kind outliving itself in River's retention. Present only when such work exists |

Between them the three states are told apart: a kind in the catalogue with no
depth series is idle, a kind absent from the catalogue with rows is retired,
and a kind in neither was never wired at all. Join an alert against
`margince_job_declared_info` rather than assuming a missing depth series means
zero work.

Its labels are the declaration's, and a label the declaration does not actually
**govern** is omitted rather than filled in — a published number is one an
alert will act on:

- `queue` is absent where a kind's insert options belong to its callers rather
  than to the contract. The file records a queue for every kind but binds one
  only where it supplies the options; a caller-owned kind takes its queue from
  scattered enqueue sites, and publishing that number would reintroduce the
  declared-versus-actual drift this surface exists to detect.
- `timeout_seconds` is `-1` where the kind deliberately runs with **no**
  deadline (the two embed passes, which are bounded by their backlog and must
  stay outside River's rescuer), and **absent** where the wall clock is an
  operator's dial computed at the worker's registration — the file calls that
  one "not knowable here at all", and a guess would be worse than silence.
  It is never `0`: zero is River's silent one-minute default wearing the same
  digits as a deliberate absence, and telling those two apart is what the
  declaration is for.
- `fan_out_unit` says what ONE child of a dispatcher stands for — a workspace,
  a connection, or a build — and is absent for a kind that fans out to nothing.

Three further things the declaration states that no gauge can, worth knowing
when you read a kind's row in `river_job`:

- **Every kind has a CHOSEN timeout.** A kind with none fails generation rather
  than running on River's one-minute default, and a worker cannot answer for
  its own wall clock: the declared value is what River is handed.
- **`fault:` says whether a worker may log a failure and return nil.** Omitted —
  the case for all but four kinds — it may not, so a green row means the work
  succeeded. The four that declare it name the durable retry policy that makes
  the green row honest (a connector sidecar's backoff, a run row's own state),
  and for those a completed job means "this attempt is concluded", not "the
  work succeeded".
- **`args:` says what each field of a kind's payload carries.** River persists
  args verbatim in a table with no workspace column and no RLS, so a job names
  a row and the worker reads it: every field is declared an id, or waived as a
  scalar with the reason a value that is not an id is safe there — and a field
  whose *name* reads like content (`Body`, `Subject`, `RecipientEmail`) owes a
  written reason even when it is an id, which is the one thing a reviewer is
  forced to argue rather than assume. Reading a job's args in an incident
  should therefore never turn up message bodies or addresses — if it does, that
  is the defect, not the payload.

Four things worth knowing before you build an alert on these:

- **An empty `workspace_id` means a dispatcher**, exactly and in both
  directions — where *empty* means the `workspace_id` key is **absent or
  JSON null** in the job's args, which is what a fan-out job's args look
  like. A job that does tenant work always names its workspace.
  A row whose key is *present but an empty string* is neither: it is
  malformed, and appears under `workspace_id="malformed_workspace_id"` so it
  is visible as the anomaly it is rather than being counted as dispatcher
  work. A job that does tenant work declares its workspace, so a null
  there is a fan-out job and nothing else. The label carries the **id**,
  never a name: the exposition endpoint has no redaction path.
- **A job scheduled for the future is counted in depth but contributes no
  age.** It is queued, but it is not late. A queue holding nothing but running or
  discarded rows reports no age series at all — a running job has already
  been claimed and a discarded one never will be, so neither is what
  "oldest runnable-and-unclaimed" measures. The endpoint reports `null` for
  the same rows.
- **The sweep pair is per workspace, not per pass.** There is no such thing
  as "the last pass" in this table: River resolves a uniqueness conflict by
  updating the existing row, so a child still active from the previous
  fan-out is deduplicated and writes no new row. Any batch-keyed reading
  would report a dispatcher retried mid-fleet as covering a fraction of the
  workspaces it actually covers. Instead, each workspace's most recent child
  of that kind is what counts.
- **A sweep series can shrink or vanish because of River's retention,** not
  because the fleet shrank: the cleaner deletes finalized rows on its own
  schedule. An absent series is the honest answer — a fabricated zero would
  be indistinguishable from "the fleet is empty".
- **Both `_failed` halves see only what River sees.** They count rows that ended
  `discarded` or `cancelled`. A kind whose worker deliberately records its own
  failure and returns `nil` — declared as `fault.nil_after_logging` in
  `backend/api/jobs.yaml`, and true of `capture_sync` and `voice_build`, whose
  retry cadence belongs to their own sidecar rather than to River — completes
  green, so a handled failure of one of those does not reach either pair. For
  those kinds a zero here means "River saw no dead rows", not "nothing failed";
  their own domain state is the authority. This is a property of the sweep
  reading as a whole, not of the per-unit half.
- **The per-workspace pair reads one grain too coarse for four dispatchers,**
  and that is what `margince_sweep_units_*` is for. Gmail sync, Gmail watch and
  the Telegram poll fan out per **connection**; the voice-build retry fans out
  per **build**. A workspace holding two connections produces two children per
  pass, and if the broken one failed before the healthy one succeeded, that
  workspace's most recent child is the successful one — so the workspace pair
  reports zero failures while a connection is dead. The unit pair counts each
  connection in its own right and reports the failure. Read the workspace pair
  for fleet coverage and the unit pair for whether every unit of a pass ran;
  neither replaces the other, and no kind is in both.

**`/metrics` is fleet-wide; the endpoint is not.** The exposition carries
every workspace's id and every kind, because an operator scraping a service
is outside the tenant boundary by construction (ADR-0080/A125 admits the id
for exactly this reason, and only the id). That is a deliberate asymmetry
with the admin endpoint below, which is scoped — so `/metrics` must stay
behind the same access control as any other operator surface, never proxied
to a tenant.

**`GET /v1/admin/job-health` — whose work died, and why?** Admin-only and
human-session-only; an agent passport is refused at the middleware and again
in the handler. It reports, for each kind, the waiting/running/retrying/dead
counts and the oldest waiting age, plus up to 50 recent failures.

- **It is scoped to the caller's own workspace plus the untenanted
  dispatcher rows, never the fleet.** `river_job` has no workspace column
  and therefore no RLS, so the handler imposes the scope itself. The
  untenanted arm is a closed set of declared dispatcher kinds — an
  unrecognised untenanted row is omitted rather than shared.
- **The failure `reason` is the job layer's own vetted sentence.**
  `river_job.errors` holds whatever a worker returned, and a worker that
  bypassed the fault seam stored its raw cause — which routinely names an
  address or record a provider refused. Anything not in the closed
  vocabulary is replaced by one fixed substitute. River's stored panic trace
  is never read at all. A row that recorded no cause at all — a job cancelled
  before it ran — says so, rather than borrowing the unvettable-failure
  sentence and claiming a failure that never happened.

## cmd/worker — the background process role

**Outbound mail does not leave without this process.** Every role that accepts
a send stages it — the api's HTTP handler and the MCP `send_email` tool — but
only `cmd/worker` registers the worker that transmits (`comms_send_email`). In an api-only deployment an accepted send is
recorded on the timeline, answers `202`, and then sits `pending` in
`comms_outbound` indefinitely with no reason string, because nothing has yet
tried and failed. Run a worker, or accept that mail is queued and not sent.

**A failed outbound webhook is never re-attempted without this process either.**
Both roles run the `cg:webhooks` consumer when `--webhook-key` is set, so an
api-only deployment still makes each delivery's FIRST attempt — but the retry
sweep is a River periodic job (`webhook_retry` → one `webhook_retry_workspace`
row per live workspace) and only `cmd/worker` runs a River runner. In an api-only
deployment a delivery that fails its first attempt sits `retrying` forever, never
reaching its 6-attempt budget and so never reaching `dead_lettered` either. The
api's boot line says so; `cmd/worker` is load-bearing for E10 retry. See
[explanation/outbound-webhooks.md](../explanation/outbound-webhooks.md#6-the-two-runtime-lanes-and-where-they-run).

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--dsn` | `MARGINCE_DSN` | — (required) | Postgres DSN, runtime app role |
| `--public-base-url` | `MARGINCE_PUBLIC_BASE_URL` | — | canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe / preference center); required for a marketing send originated by this role's Surface-B agent run — without it that send refuses rather than emit a forgeable link |
| `--config` | `MARGINCE_CONFIG` | `margince.yaml` | the deployment configuration file; the worker reads it for the `ai.capture_payloads` posture the Surface-B runner honors (capture applies to **both** the api and worker roles — the worker runs the richest content source, the agent runs). A missing file boots with capture off |
| `--redis` | `MARGINCE_REDIS` | `localhost:56379` | Redis address (event bus) |
| `--ai-routing` | `MARGINCE_AI_ROUTING` | — | path to `ai-routing.yaml`; enables the Surface-B runner + embeddings |
| `--ai-fake` | — | `false` | run the Surface-B runner on the offline fake model |
| `--runner-interval` | — | `30s` | Surface-B scheduler tick — the River periodic schedule of the `agent_scheduler` dispatcher, which enqueues one `agent_scheduler_workspace` job per live workspace. It paces the fan-out, not an agent's own schedule: the catalog's daily due hour decides when a brief runs |
| `--retention-interval` | — | `24h` | retention evaluator pass interval — the River periodic schedule of the `privacy_retention` dispatcher, which enqueues one `privacy_retention_workspace` job per workspace |
| `--time-scan-interval` | — | `1h` | clock-trigger automation scan interval (`no_activity_reminder` et al. — the River periodic job `TimeScanner.Scan` drives) |
| `--close-date-interval` | — | `24h` | close-date hygiene sweep interval (INV-CLOSE-PAST) |
| `--webhook-key` | `MARGINCE_WEBHOOK_KEY` | — | base64 32-byte key sealing outbound-webhook signing secrets; unset = the delivery worker stays off (no `cg:webhooks` consumer, no retry sweep) |
| `--webhook-retry-interval` | — | `30s` | how often the outbound-webhook retry dispatcher fans one due-retry pass out per live workspace (worker role only) |
| `--reconcile-interval` | — | `24h` | overnight follow-up reconciliation pass interval |
| `--overlay-reconcile-interval` | — | `2m` | overlay-mode incumbent mirror sweep interval. Every tick spends incumbent API quota per object class even when nothing changed (9 classes ≈ 11 REST calls/tick against HubSpot's 90k/day), so lengthen it on a dev box. `POST /overlay/reconcile` ("Sync now") only marks the workspace due — the sweep still waits for the next tick, so a long interval makes that button feel slow |
| `--overlay-backfill-limit` | `MARGINCE_OVERLAY_BACKFILL_LIMIT` | `0` (uncapped) | cap the overlay INITIAL mirror backfill at N records per object class — dev/demo, so connecting a real portal doesn't pull it all onto a laptop. Only the backfill is capped: later incremental sweeps still bring in anything edited after the sweep window, which opens shortly before the connect instant (a clock-skew grace). A class the cap actually cuts short reports `backfillComplete: false` permanently (`overlay_backfill_cursor.truncated`) — unsetting the limit does NOT resume it, since the cursor is already `done`; reset that class's `overlay_backfill_cursor` row (or reconnect, which purges it) to backfill it for real. Don't change the limit mid-backfill either — the running count rides in `overlay_backfill_cursor` as a `<count>\|<inner>` prefix the uncapped adapter rejects, which fails that class every sweep until the cursor row is cleared |
| `--send-rate-limit` | — | `0` (= built-in 30) | outbound messages ONE mailbox may transmit per `--send-rate-window`. Burst pacing, not a quota: the provider enforces its own daily cap and throttles an account that bursts past it. The limiter is in-process, so a multi-worker deployment paces each replica's view of the mailbox independently |
| `--send-rate-window` | — | `0` (= built-in 1m) | the window the per-mailbox send rate is measured over |
| `--send-max-age` | — | `0` (= built-in 24h) | how long a staged send may be deferred by the pacing chain before it parks with a reason instead. Without a bound a permanently saturated policy would defer a message forever, silently |
| `--deepread-max-pages` | `MARGINCE_DEEPREAD_MAX_PAGES` | `0` (= built-in 40) | deep-read crawl page cap |
| `--deepread-max-bytes` | `MARGINCE_DEEPREAD_MAX_BYTES` | `0` (= built-in 32 MiB) | deep-read crawl aggregate byte cap |
| `--deepread-wall` | `MARGINCE_DEEPREAD_WALL` | `0` (= built-in 4m) | deep-read crawl wall clock |
| `--observe-addr` | `MARGINCE_OBSERVE_ADDR` | — (off) | address to serve this worker's `/healthz`, `/readyz` and `/metrics` on, e.g. `127.0.0.1:9101`. Empty serves nothing — see below |

### The worker's own operator surface

`--observe-addr` gives `cmd/worker` the three operational endpoints the api has
always had. It answers a question the fleet-wide gauges structurally cannot:
**which process** is not doing the work. Every job-table gauge is a projection
of a shared table, so it reads the same whichever replica served the scrape —
one wedged worker in a pool of three is arithmetically invisible in it.

What this listener carries is therefore only what is **process-local**, and it
re-serves no fleet-wide reading:

| Family | Meaning |
|---|---|
| `margince_process_goroutines` | goroutines in the scraped process |
| `margince_process_heap_bytes` / `margince_process_heap_sys_bytes` | heap in use, and heap held from the OS |
| `margince_process_gc_cycles_total` | completed GC cycles since this process started |
| `margince_pgxpool_conns` | this process's own connection pool, by class |
| `margince_relay_published_total` | outbox rows *this* relay has shipped since start |

The same `margince_process_*` section is served by `cmd/api` too — it describes
whichever process answered, which is exactly what makes it worth having on both.
`margince_outbox_unpublished`, the job-table gauges and the declared catalogue
stay a **single** reading on the api: two roles answering one fleet number is a
worse operator surface than one gap.

`/readyz` probes the two dependencies this role cannot work without — Postgres
and Redis — and answers `503` naming the one that failed. `/healthz` stays a
dumb liveness answer, so a database outage stops traffic being routed here
without restart-looping a process the outage did not break.

`/readyz` also reports **`boot`** — this replica has finished starting its
event lanes and job runner. The listener comes up before those on purpose, so a
probe answers during a slow boot; without the boot check that ordering would let
a rollout retire the last working replica in favour of one that had not yet
picked up a job.

**Off is the default, and it is not a convenience default.** Unlike the api's
`/metrics` this surface carries no workspace id and no tenant data at all — but
it is still an unauthenticated operator surface that discloses dependency health
and process capacity, so exposing it is an operator decision, and so is the
interface it binds. Bind it to a loopback or a private interface, never a public
one. An address that cannot be bound is a **boot error** naming it — a worker
that could not serve its probes must not carry on looking healthy.

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

Extraction runs two routed lanes CONCURRENTLY with the crawl (page
calls launch as pages commit): `site_fact_extract` — one compact call
per fact-bearing page, cheap-tier-first (the reply cites numbered
passages instead of quoting, which a fast model emits reliably) — and
`site_extract` — the ONE premium-first profile call over the
identity-dense excerpts. Evidence is verified in Go against the cited
passage (reference evidence: the stored snippet is the page's own
text). Judge any candidate binding against the pinned quality floor:
`make -C backend e2e-siteread` with `MARGINCE_E2E_MODEL=provider:model`
or `MARGINCE_AI_ROUTING=<yaml>` (paid, network E2E vs gradion.com — a
different model must do the same or better to pass). Typical read:
10–25 s end-to-end depending on how hard the origin throttles the
crawl burst.

Without a declared model (`--ai-routing`/`--ai-fake`) the runner and the
embedding lane simply do not start; the relay, retention, the event-triggered
workflow dispatch (`cg:workflows`), and the clock time-scan always run.
Shutdown is graceful: in-flight subscriber handlers finish their ack before
the process exits.

## Capture connector OAuth (api, worker) — Gmail / Microsoft 365

The Gmail and Outlook/M365 capture connectors are enabled by supplying the
operator's own OAuth app. Absent these, `make dev` is unchanged and the
`/connectors/gmail/*` / `/connectors/graph/*` surfaces stay their declared
501. Secrets travel via the environment, never CLI flags in production
(argv is world-readable). Roles: **api** serves connect/callback, **worker**
runs the background sync.

| Flag | Env | Role | Meaning |
|---|---|---|---|
| `--gmail-client-id` / `--gmail-client-secret` | `MARGINCE_GMAIL_CLIENT_ID` / `…_SECRET` | api + worker | the Google OAuth app; with the state key and `--public-base-url`, enables `/connectors/gmail/*` (api) and the sync poll (worker) |
| `--graph-client-id` / `--graph-client-secret` | `MARGINCE_GRAPH_CLIENT_ID` / `…_SECRET` | api + worker | the Microsoft (Entra) app; same enablement shape for `/connectors/graph/*` |
| `--graph-tenant` | `MARGINCE_GRAPH_TENANT` | api + worker | Microsoft identity tenant (default `common` — any organization) |
| `--connector-state-key` | `MARGINCE_CONNECTOR_STATE_KEY` | api | HMAC key (≥32 bytes) signing the OAuth connect `state`; required for both connect flows |
| `--api-base-url` | `MARGINCE_API_BASE_URL` | api | the api's externally-reachable base for the OAuth callback `redirect_uri`; defaults to `--public-base-url`, set only when api and SPA are on different origins (e.g. dev). Messaging channels need NO public address of their own — Telegram ingress long-polls, so nothing is ever told where to reach this installation |
| `--gmail-sync-interval` | — | worker | Gmail incremental-sync poll interval (default `2m`) |
| `--gmail-pubsub-topic` | `MARGINCE_GMAIL_PUBSUB_TOPIC` | worker | Gmail Pub/Sub topic (`projects/<p>/topics/<t>`); enables the push-watch register+renew job (empty = poll only) |
| `--gmail-watch-interval` / `--gmail-watch-renew-within` | — | worker | push-watch maintenance scan (`6h`) / renew this far ahead of the 7-day expiry (`48h`) |
| `--gmail-push-token` | `MARGINCE_GMAIL_PUSH_TOKEN` | api | shared secret on the Pub/Sub push subscription URL; enables `POST /webhooks/gmail` (empty = route absent) |
| `--gmail-push-audience` / `--gmail-push-service-account` | `MARGINCE_GMAIL_PUSH_AUDIENCE` / `…_SERVICE_ACCOUNT` | api | OIDC audience + signing service-account email; set both and the push webhook also verifies Google's OIDC token |
| `--gmail-jwks-url` | `MARGINCE_GMAIL_JWKS_URL` | api | override Google's OIDC JWKS URL; test/dev only |

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
Leave `MARGINCE_KEYVAULT_ROOT_KEY` unset and the vault is absent: every
connector's connect path (gmail, gcal, graph, imap all connect through the
same operation, sealing to the vault) refuses loudly rather than store a
credential in the clear. Set it and the api gains the
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

## cmd/migrate — schema migrations

```
migrate <up|down> --dsn <owner-dsn> [--steps n]
migrate reset-password --dsn <owner-dsn> --email <user-email>
migrate <recreate-db|drop-db|db-exists> --dsn <owner-maintenance-dsn> --name <db> [--template <db>]
```

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--dsn` | `MARGINCE_DSN` | — (required) | Postgres DSN, **owner** role. For the db verbs it must name a maintenance database (`postgres`): `CREATE`/`DROP DATABASE` cannot run inside the database being dropped |
| `--steps` | — | `1` | migrations to revert (`down` only) |
| `--email` | — | — | user email (`reset-password` only): the operator break-glass — sets that user's password directly against the database, reading the new password from **stdin** (never argv); the way back in when the admin is locked out and no outbound email is configured |
| `--name` | — | — | database name (`recreate-db`, `drop-db`, `db-exists` only): the integration lane's clone-per-package admin — drop-if-exists + create, drop-if-exists, or print `true`/`false`; the drops are `WITH (FORCE)`, so a lingering session dies rather than flaking the teardown. Runs on the same owner DSN the migrations and tests use, so the lane needs no host psql and an overridden `MARGINCE_TEST_DSN` targets one cluster throughout. A name (or template) over the server's identifier limit (63 bytes stock) is rejected, never silently truncated onto a different database |
| `--template` | — | — | template database to copy (`recreate-db` only): `CREATE DATABASE … TEMPLATE`, a fast file copy |

## Other environment variables

| Var | Used by | Meaning |
|---|---|---|
| `MARGINCE_ENV` | api (`runtimeenv.Parse`) | Read at boot and parsed **fail-closed**: only the exact values `dev`, `staging`, or `test` yield a non-production posture; unset, `production`, or any unrecognized value ⇒ production, which disables every dev-only destructive switch (today: the admin data-reset endpoint below). The Makefile exports `dev`; production must not set it. |
| `MARGINCE_TEST_DSN`, `MARGINCE_TEST_APP_DSN`, `MARGINCE_TEST_REDIS` | integration tests | owner DSN / app-role DSN / Redis address for the real-Postgres lane; exported by the Makefile. The lane runs on its own `_test` namespace (the `margince_test` DB, never the dev `margince` DB), so it can run alongside `make dev`. |
| `MARGINCE_TEST_REDIS_DB` | integration tests | Redis logical db for the lane (default 15). db 0 is reserved for a running `make dev`; a valid value is 1..15, and the parallel runner assigns one per package so concurrent packages never share a stream. Out-of-range fails loudly. |

### `POST /v1/admin/reset-data` — non-production data reset

Gated on the `MARGINCE_ENV` posture above. In production the operation does
not exist: the environment check runs **before** auth, so a misconfigured
production deployment 404s rather than leaking that the endpoint exists (never
a 403). In a non-production posture:

1. **Human-only** (`auth.RequireHuman`) — an agent/passport principal is
   rejected, 403.
2. **Admin-only** (`auth.RequireAdmin`) — the literal `admin` role; `ops` and
   every other role is rejected, 403.
3. **Typed confirmation** — the request body `{"confirmation": "<organization
   name>"}` must equal the workspace's organization name exactly; a mismatch
   is `422` (never partially applied — checked before anything is touched).

On success it wipes workspace domain + seeded-config data back to the
first-boot bootstrapped state and re-runs the module seeders (pipeline/stages,
consent purposes + retention, AI defaults, starter automations, the booking
page) — the same seed path `identity`'s installation bootstrap uses. It
**preserves** the identity/auth layer (`workspace`, every `app_user`, roles,
role assignments, teams, team memberships, sessions, passports, tokens — so
login keeps working) and the append-only ledgers `audit_log` / `system_log`.
The reset itself is recorded as an `audit_log` row (action `reset_data`).

The sweep runs as the app role — no superuser, no disabled triggers — so it
discovers a safe delete order at runtime (a savepoint per table per pass,
retrying whatever a still-live FK blocks) rather than relying on a hand-kept
ordering; an unbreakable FK cycle is surfaced as an error, never silently
skipped. Orphaned `cf_*` custom-field columns are dropped afterward through
the owner schema pool (`--schema-dsn`); with no schema pool configured that
step is skipped (logged, not swallowed) and the reset itself still succeeds.

`GET /v1/me`'s `non_production` field mirrors the same posture so the SPA can
show the action only where it will work: Admin settings → *data* tab → Danger
zone → *Reset data*, which prompts the operator to type the organization name
before calling the endpoint — the server is the sole validator of that string,
the client-side prompt is only UX.

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

`company_context.rollout` is the ordered server-side company-context capability:
`off` disables context reads, injection, and the new onboarding surface; `read`
enables the canonical read model and Company Context settings; `tasks` also
injects bounded context into declared AI tasks; `onboarding` additionally enables
the five-step first-run flow. The default is `onboarding`. Moving backward is a
reversible operational kill switch and never deletes confirmed company data.

### Rates

The `rates:` block configures the admin **"Refresh from sources"** jobs (worker
role). A refresh never writes a rate directly — it stages **confirm-first
proposals** into the approvals inbox, and a human approves each before it
applies. It is read only by the worker (the api enqueues the job; the worker
crawls and stages).

| field | default | effect |
|---|---|---|
| `fx_source` | `https://api.frankfurter.dev/v1/latest` | Base-relative FX JSON API (`{base,rates}`, queried `?base=&symbols=`). The default is the free, no-key ECB feed. |
| `fx_currencies` | `[USD, GBP, CHF]` | Candidate foreign currencies the FX refresh proposes to **bootstrap an empty rate sheet** — a fresh install tracks none, so without a candidate set the refresh would have nothing to fetch. Once the sheet has rows, the refresh re-prices exactly those tracked currencies and this set is unused. Each entry must be **ISO 4217-shaped** (three uppercase letters) and unique, or boot fails — the same shape check as `base_currency`; existence is not verified, so a well-formed but unsupported code (`USX`) parses and is then skipped by the source with a logged warning rather than a staged proposal. |
| `model_pricing` | *(none)* | Maps a provider name to its pricing-page URL the model-cost refresh crawls and AI-extracts (the `rate_extract` task — `make e2e-ai-report` says what any binding has been certified to). A plain `GET` must yield the price text — Google's docs page does; many JS-rendered marketing pages yield none. |

The **model-cost refresh** needs both a `model_pricing` entry **and** a bound
`rate_extract` model (in `ai-routing.yaml`); absent either, it no-ops. The **FX
refresh**, by contrast, has no such dependency — `fx_source` and `fx_currencies`
both default, so it always has something to do even on an absent `rates:` block.
Neither refresh ever auto-applies — a rate is proposed from the live source and
applied only on human approval, so a non-EUR deal with no approved rate still
fails closed (never a silent `rate=1`).

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

The `embeddings:` binding also takes `dimensions` — the vector width the
provider is asked to emit. Default `1536` (a gemini-recommended width); the
embedding column is unbounded, so any width in range is stored without a
migration. `0` or omitted means the default. An operator-set value validates
into `[1, 2000]` (`ai.ParseRouting`) — out of range is a boot error, never a
runtime one. Changing `dimensions` (or the provider/model) needs **no
migration**: the embedding column is unbounded `vector`, so a config edit +
restart (`make dev`) takes effect immediately — the next ingress and query
both use the new width. Existing rows stay stamped under the old identity
until re-embedded; see below.

### Embedding binding changes & reindex

Every embedding row is stamped with the identity (provider/model@dimensions)
it was written under. On boot, the seed step plants the deployment's
`embed_store_binding` marker at the configured identity; if the store was
already populated under a **different** one — an operator changed the
binding since the last boot — that mismatch is logged at **error** level (an
admin must see it) and boot still succeeds. Search stays available
throughout: vector ranking filters to the **current** identity (stale-identity
rows are excluded, not queried at the wrong width), and the lexical/FTS arm
and any already-current rows keep answering queries. Reindexing onto the new
identity is a deliberate ops action, never something boot forces.

The mismatch surfaces two ways: `/readyz`'s `embed:` line (`active` |
`needs_reindex` | `reembedding` | `unknown` — the last when no embed lane is
bound or the marker read fails; it never makes `/readyz` return 503) and an
admin/ops-only banner in the frontend shell. Reconciling runs through three **human-only** routes
(`x-agent-access: human-only` — a passport/agent principal never reaches
them):

- `GET /embeddings/reindex/status` — the binding marker plus a live
  per-workspace pending-entity scan; admin/ops-only (the
  `embedding_reindex` object's `read` grant — manager/rep/read_only hold no
  grant and the request 403s, matching the ops-gated banner that consumes
  it).
- `GET /embeddings/reindex/preview` — the scope before the spend:
  fleet-wide and per-workspace pending counts plus a cost estimate (always
  `heuristic` — a work-shape token figure, never priced from observed
  `ai_call` history) and each workspace's advisory budget-utilization
  impact. Admin/ops-only, the same `read` grant as the status route. The
  embed lane itself is budget-exempt (routing never queues or degrades it),
  so this is disclosure only, never a block.
- `POST /embeddings/reindex` — admin/ops-gated (the `embedding_reindex`
  object's `update` grant). Claims the binding marker (`idle` →
  `reembedding`) and enqueues one fleet-wide re-embed job, resumable by
  construction (a content-hash + identity skip-compare makes revisiting an
  already-current row free); one live reindex at a time (`409
  reindex_running`).

Correctness never depends on a reindex finishing: retrieval filters to the
current identity, so rows still under a stale identity are simply hidden
from search until re-embedded, never served as if current.

Two operator gotchas, verified against current vendor docs:

1. **`openai_compatible`'s embeddings lane 404s on OpenRouter, Groq, and
   DeepSeek** — they serve chat only. Bind `embeddings:` to a vendor that
   has the lane (OpenAI, Mistral, a Gemini-compat layer, Together) or a
   local model (ollama `bge-m3`).
2. **Vendor `-latest` model aliases drift and some are being deprecated**
   (e.g. Mistral). Pin an explicit versioned id, or resolve via the
   vendor's `/models` endpoint, rather than hardcoding an alias.
