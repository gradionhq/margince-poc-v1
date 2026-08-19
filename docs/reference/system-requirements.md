# System requirements

What an installation needs to run. Two deployment shapes are supported:
**single node** (every service on one host) and **separate nodes** (api /
worker / web / database on hosts of their own). Both run the same images and
the same configuration.

The sizing below assumes the AI lanes are bound to a cloud provider. Running a
model on the installation's own hardware needs substantially more memory and
possibly a GPU, and is not sized here.

## Software

| Requirement | Notes |
|---|---|
| **Postgres 16** with **pgvector** ≥ 0.5.0 | The `vector` extension is not trusted, so a superuser installs it once (`scripts/deploy/db-bootstrap.sql`). |
| **Redis 7.0 - 7.2** | Streams with consumer groups. **Valkey** is compatible and should work but is not officially tested. |
| **A TLS-terminating reverse proxy** | api and web must be served under one hostname. |
| **Correct system clock** | Retention, automation triggers and job scheduling are time-driven. |
| S3-compatible object store | Optional. Absent, attachments and company logos are unavailable; nothing else changes. |
| Outbound HTTPS | Optional, per feature. Core CRM, authentication and license validation work fully offline. |

The api, worker and web containers hold no persistent local state — all durable
state is in Postgres, Redis and the object store.

## Volume tiers

| Tier | Persons | Organizations | Activities |
|---|---|---|---|
| **Small** | 10,000 | 1,000 | 20,000 |
| **Mid-market** | 250,000 | 10,000 | 500,000 |

Pick the tier by contact count. The second question is whether AI-powered
search and retrieval is enabled: the embedding store is roughly twenty times
the size of the CRM data itself, so it moves the numbers more than the tier
does. That ratio assumes the default 1536-dimension embedding width; the store
scales linearly with the configured `dimensions`.

## Single node

Everything on one host. Containerized or not makes no difference.

| Tier | Retrieval | vCPU | RAM | Disk |
|---|---|---|---|---|
| Small | off | 2 | 4 GB | 20 GB |
| Small | on | 2 | 8 GB | 40 GB |
| Mid-market | off | 4 | 8 GB | 50 GB |
| Mid-market | on | 8 | 32 GB | 200 GB |

- **Set `shared_buffers` explicitly.** Postgres defaults assume it owns the
  machine; here it shares memory with the application processes. Left at a
  value sized for a dedicated host, the node swaps, and it presents as slow
  page loads rather than as a misconfiguration.
- **Restart is downtime.** The api applies migrations at boot, so this shape
  has no rolling upgrade, and every service shares one failure domain.

## Separate nodes

| Node | vCPU | RAM |
|---|---|---|
| **Postgres** | 2 (small) – 8 (mid-market) | 4 GB (small) – 32 GB (mid-market, retrieval on) |
| **api** | 2 | 2 GB per replica |
| **worker** | 2 | 4 GB per replica |
| **web** | 1 | 512 MB |
| **Redis / Valkey** | 1 | 2 GB |

Database disk follows the single-node table above; the other nodes need none.

- **Put the api in the same availability zone as the database.** Round-trip
  latency is on the critical path of every page load. Cross-region placement
  degrades the product and more CPU does not compensate.
- Redis and the object store must be reachable from both the api and the
  worker.

## Database settings

- **`max_connections`**: allow **40** for one api and one worker, plus **19**
  per additional api replica and **16** per additional worker replica, plus
  headroom for maintenance and monitoring sessions.
- **Connection poolers must run in transaction mode.** The tenant boundary is
  bound per transaction; statement pooling breaks it, and session pooling
  wastes the pool.
- **Provision four times the steady-state data size**, to cover write-ahead
  logging, bloat and index rebuilds. Size backups on top of that.
- **Growth is bounded by the retention posture.** The default policy
  anonymizes and erases aging records on a yearly ladder. An installation
  configured with `retain_only` never deletes anything and must be sized on
  its own retention horizon.

## Network

api and web sit behind one reverse proxy under **one hostname** — the web
application, the MCP client handshake and the OAuth consent flow all depend on
it.

Outbound access is needed only per feature: the bound AI provider, the FX and
model-price sources, the Gmail / Microsoft capture connectors, outbound webhook
subscribers, and the SMTP relay. An air-gapped installation is supported; a
missing AI key disables the AI lanes and leaves the rest of the product
working.

## Availability

The api and the worker both scale to multiple replicas without configuration:
concurrent api instances divide the event backlog rather than duplicating it,
simultaneous starts cannot race the schema migration, and scheduled jobs run
once cluster-wide regardless of how many workers are up.

## Browsers

Current versions of Chrome, Edge, Firefox and Safari.
