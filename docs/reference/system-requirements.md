# System requirements

This document tells you what an installation needs. Two deployment shapes
are possible: **single node** and **separate nodes**. On a single node, all
services run on one host. With separate nodes, the api, the worker, the web
server and the database each run on their own node. The two shapes use the
same installation mechanism and the same configuration.

The sizes below apply when the AI functions use a cloud provider. A model on
the installation's own hardware needs more memory and possibly a GPU — see
*Self-hosted AI models*. The AI functions are optional: without a model
binding, the rest of the product operates normally.

## Software

| Requirement | Notes |
|---|---|
| **Postgres 16** with **pgvector** ≥ 0.5.0 | The `vector` extension is not trusted. A superuser must install it one time (`scripts/deploy/db-bootstrap.sql`). |
| **Redis 7.0 to 7.2** | Streams with consumer groups are necessary. **Valkey** is compatible, but we do not test it. |
| **A reverse proxy that terminates TLS** | Serve the api and the web under one hostname. |
| **A correct system clock** | Retention, automation triggers and job schedules use the system time. |
| **An S3-compatible object store** | Attachments, company logos, offer PDFs, file custom fields and CSV import keep their data in it. |
| Outbound HTTPS | Optional, for some features. The core CRM, authentication and license validation operate fully offline. |
| A model endpoint | Optional, only for the AI functions: a cloud provider with your own key, or your own Ollama / vLLM host. See below. |

The api, worker and web containers keep no permanent local state. Postgres,
Redis and the object store keep all permanent state.

## Volume tiers

| Tier | Persons | Organizations | Activities |
|---|---|---|---|
| **Small** | 10,000 | 1,000 | 20,000 |
| **Mid-market** | 250,000 | 10,000 | 500,000 |

Select the tier by the number of contacts. Then decide if AI search and
retrieval is on. The embedding store is approximately 20 times the size of
the CRM data. Thus this option changes the numbers more than the tier does.
The ratio applies to the default embedding width of 1536 dimensions. The
store size increases linearly with the configured `dimensions`.

## Single node

All services run on one host. Containers are optional. The sizes do not
change.

| Tier | Retrieval | vCPU | RAM | Disk |
|---|---|---|---|---|
| Small | off | 2 | 4 GB | 20 GB |
| Small | on | 2 | 8 GB | 40 GB |
| Mid-market | off | 4 | 8 GB | 50 GB |
| Mid-market | on | 8 | 32 GB | 200 GB |

- **Set `shared_buffers` manually.** The Postgres defaults expect a dedicated
  machine. Here, Postgres shares the memory with the application processes.
  If the value stays at a dedicated-host size, the node swaps. The symptom is
  slow page loads, not an error message.
- **A restart causes downtime.** The api applies the migrations at boot.
  Thus this shape has no rolling upgrade. All services share one failure
  domain.

## Separate nodes

| Node | vCPU | RAM |
|---|---|---|
| **Postgres** | 2 (small) to 8 (mid-market) | 4 GB (small) to 32 GB (mid-market, retrieval on) |
| **api** | 2 | 2 GB for each replica |
| **worker** | 2 | 4 GB for each replica |
| **web** | 1 | 512 MB |
| **Redis / Valkey** | 1 | 2 GB |

For the database disk, use the single-node table above. The other nodes do
not need disk space.

- **Put the api in the same availability zone as the database.** Each page
  load waits for the round-trip latency. A placement across regions makes the
  product slow. More CPU does not correct this.
- Make sure that the api and the worker can connect to Redis and to the
  object store.

## Database settings

- **`max_connections`**: permit **40** connections for one api and one
  worker. Add **19** for each additional api replica. Add **16** for each
  additional worker replica. Add headroom for maintenance and monitoring
  sessions.
- **Operate connection poolers in transaction mode.** The tenant boundary
  binds to one transaction. Statement pooling breaks the boundary. Session
  pooling wastes the pool.
- **Provision four times the steady-state data size.** This covers
  write-ahead logs, bloat and index rebuilds. Add space for backups.
- **The retention posture limits growth.** The default policy makes aging
  records anonymous and erases them on a yearly ladder. An installation with
  `retain_only` never deletes data. Size such an installation on its own
  retention horizon.

## Network

The api and the web sit behind one reverse proxy with **one hostname**. The
web application, the MCP client handshake and the OAuth consent flow need
this.

Only some features need outbound access: the AI provider, the FX and
model-price sources, the Gmail / Microsoft capture connectors, outbound
webhook subscribers, and the SMTP relay. An air-gapped installation is
possible. Without an AI key, the AI lanes stop, and the other functions
continue.

## AI providers

The product runs no inference of its own. You connect a provider with your
own key. Supported: **Anthropic** (Claude), **OpenAI** (GPT), **Google**
(Gemini), and any **OpenAI-compatible** vendor (Mistral, DeepSeek, Groq,
OpenRouter, a gateway, …).

- The api and the worker both call the provider. Give both of them outbound
  HTTPS to the endpoint.
- The embeddings function needs a provider with an embeddings endpoint. A
  chat-only vendor cannot serve it. A local embedding model is an
  alternative.

## Self-hosted AI models

**Ollama** and **vLLM** serve a model on your own hardware. No key is
necessary, and these are the only options in the zero-egress profile.

The model host comes in addition to the node tables above. The sizes below
assume the default model class (Gemma 3), a 32,768-token context window, and
an embedding model (for example `bge-m3`) on the same host. Plan **50 GB
disk** for model files.

| Host | Hardware | Serves |
|---|---|---|
| **Minimum** | GPU with 8 GB memory, 16 GB RAM | A quantized 4B model plus the embedding model. Good enough to try the AI functions; expect thin extraction results. |
| **Recommended** | GPU with 24 GB memory (RTX 4090, L4, A10), 32 GB RAM | A quantized 12B–27B model with the full context window, the embedding model beside it, and interactive latency. |
| **Concurrent / unquantized** | 48 GB GPU memory or more (L40S, RTX 6000 Ada, 2 × 24 GB), 64 GB RAM | A 12B model unquantized on vLLM, or several requests at the same time without queueing. |

- A CPU-only host works, but only for background tasks: answers take minutes,
  and interactive functions become unusable.
- The model server must support JSON-schema output. Most tasks constrain the
  answer with a schema.
- A task can escalate from a local model to a cloud tier. For a fully local
  installation, bind every tier to a local model, or use the zero-egress
  profile — it refuses a cloud provider at start.

## Availability

The api and the worker scale to multiple replicas without configuration.
Concurrent api instances divide the event backlog. They do not duplicate it.
Simultaneous starts cannot cause a race on the schema migration. Scheduled
jobs run one time in the cluster, independent of the number of workers.

## Browsers

Use a current version of Chrome, Edge, Firefox or Safari.
