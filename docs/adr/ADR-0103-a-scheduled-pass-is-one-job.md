# ADR-0103 — A scheduled pass is one job; a fan-out enumerates real work, never tenants

**Status:** Active — the role vocabulary and the two response schemas are
built. The collapse of the tenant fan-outs is **not built**; see below.

**Decided:** 2026-08-14

## The decision

A scheduled pass over the installation is one job declaration, not two. The
dispatchers that existed only to enumerate workspaces and enqueue one child
each collapse into the pass itself.

The collapse takes the dispatcher's kind and cadence, because the kind is the
name an operator already alerts on and the cadence is the operator-facing
interval. It takes the child's queue, timeout, retry policy and registration
condition, because those describe the work rather than the enumeration. Getting
that split backwards is the one way this breaks production quietly: the
long-running passes were deliberately kept off the default queue so a slow
external receiver could not starve short maintenance jobs, and that reason is
about duration, not tenancy.

A fan-out over something an installation genuinely has many of stays a fan-out.
A connector connection and a voice build are real plural work, their children
keep their own retry and failure isolation, and one connector's failed sync
must not stop the next connector's.

The role vocabulary is `dispatcher` and `worker`. A worker is named by the
subject in its arguments, not by a tenant.

The tenant leaves every argument list. Where it was the only argument, the
block goes empty; where it sat beside a subject, the subject becomes the whole
identity of the job.

## Why

The job declarations encoded the tenant boundary in their vocabulary rather
than in a column, so removing that boundary was not one mechanical edit
repeated 27 times. It changes the vocabulary the manifest is written in, and
therefore the generator, the census gates and roughly eighty-five call sites of
the dispatch helpers. Left undecided, each module would answer it locally and
the manifest would end up with two dialects.

Under one installation a workspace dispatcher enumerates a single row and
enqueues a single child: two job rows, two kinds, two timeouts and a fan-out
declaration to express one pass.

## What it binds in this repository

- `backend/api/jobs.yaml` declares every job kind. `role: workspace` no longer
  appears anywhere; the two surviving values are `dispatcher` (24 entries) and
  `worker` (41).
- `backend/tools/gen-jobs` compiles the manifest into the closed kind set
  compose registers through. A kind not declared there cannot be registered, and
  a kind with no chosen timeout fails generation rather than running on River's
  silent one-minute default.
- `backend/internal/compose/jobcensusargs.go` is the census arm that compares
  each registered kind's argument struct against its declaration, so a
  declaration and its Go type cannot drift apart.
- `backend/api/crm.yaml` no longer contains `per_workspace` anywhere.
  `EmbedReindexStatus` and `EmbedReindexPreview` lost their per-tenant
  breakdown arrays.
- `utilization_impact` survives that removal, hoisted to the root of
  `EmbedReindexPreview` where it names the installation's budget band. It is an
  operator disclosure before a spend, and deleting the array around it would
  have taken it silently.
- `backend/internal/compose/jobmetrics.go` renders a job's workspace as a
  metric label, admitted by ADR-0080. That label goes when the fan-out does.

## What is not built

`backend/api/jobs.yaml` still carries 20 declarations with
`fan_out_unit: workspace`, each pairing a dispatcher with a per-workspace
worker. `finance_sync_sweep` fanning out to `finance_sync` and
`capture_auto_enrich_sweep` fanning out to `capture_auto_enrich_workspace` are
two of them. Only four fan-outs are over real plural work — three over a
connector connection, one over a voice build.

`Workspace: id` still appears in 35 argument blocks.

So the vocabulary changed but the shape did not: the pairs that were called
`role: workspace` are now called `role: worker` and still run behind a
dispatcher that enumerates one row.

What is owed is the collapse itself, and it has a visible cost. An operator
loses roughly 23 job kinds. Anything bookmarked or alerted on by one of those
names breaks at that release, and a release note has to say so. A failed pass
will then report once rather than twice, losing the distinction between "the
enumeration broke" and "the work broke" — a distinction that no longer has two
sides once the enumeration reads a single row.

The integration suites in four modules — outbound webhooks, the agent
scheduler, privacy retention, search re-embedding — still prove isolation
between two tenants. Those suites become theatre once the tenant column goes,
so the columns and the fan-out have to move together.

## History

Adopted from the retired specification, decided 2026-08-14. Rewritten in plain
language 2026-08-19. The source is marked proposed and awaiting ratification.

The source records that the build reached the two-schema shape first, before
the specification did, and says so plainly rather than quietly. The
specification adopting a shape whose merit is independent of who wrote it
first is the direction of travel; ratifying whatever the build happens to do
is not.

The source rejects three alternatives. Keeping the dispatchers as no-op
passthroughs leaves every pass costing two rows and a vocabulary naming a
boundary that does not exist. Collapsing the connection fan-outs too would
serialize every connector sync into one pass, so one hung mailbox delays every
other. One role value for everything cannot express that one job enqueues the
other, which is exactly what the census gate checks.
