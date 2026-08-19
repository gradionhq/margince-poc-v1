# ADR-0080 — The job-runtime metrics carry a workspace label; nothing else does

**Status:** Active
**Decided:** 2026-08-03

## The decision

A Prometheus label is admitted when the operator controls how many distinct
values it can take, and refused when tenant data volume controls that.
`queue`, `kind` and `state` pass. `workspace_id` passes on the job-runtime
gauges, because one installation serves one organization and the server
refuses to start when it finds more than one. Person, deal, activity,
approval, passport and job-row identifiers never pass — they grow with the
data, which is the case the rule exists to defend against.

The label is admitted on the four job gauges only, not across the metric set.
The request-latency histogram already multiplies route by method by status,
and adding a workspace dimension to a histogram is a different proposition
from adding one to a gauge.

The label carries the workspace id, never a name or a slug. `/metrics` has no
redaction path, and a workspace name is operator-authored text.

If the one-organization-per-installation rule is ever reversed, this admission
is reversed with it.

## Why

The original rule banned a `workspace_id` label outright, and its stated
reason was multi-tenant scale: one installation serving an unbounded, growing
set of customers, where a per-tenant label multiplies every time series by the
customer count. That is the classic Prometheus cardinality failure and the ban
was the right answer to it.

The deployment model then changed. One installation serves one organization,
so the label's cardinality is that organization's own workspace count, not a
SaaS customer list. Meanwhile the operator question the job queue raises — is
one workspace's work backing up — is dashboard-shaped, and the blanket ban
sent that question to an in-product screen instead.

Replacing the enumerated ban with a test is what makes the rule survive labels
nobody has proposed yet. A reviewer asks the question rather than checking a
name against a list.

## What it binds in this repository

- `backend/internal/compose/jobmetrics.go` renders the job section. Four
  gauges carry the label: `margince_job_queue_depth` and `margince_job_running`
  by queue and workspace, `margince_job_discarded` and
  `margince_job_cancelled` by kind and workspace.
- `workspaceLabelFor` in that file answers the label for one group. A
  dispatcher does no tenant work and gets the empty string, which is the one
  invariant every reader of these gauges stands on.
- A row whose workspace key is present but empty is malformed, and renders
  under the literal `malformed_workspace_id` rather than being counted as a
  dispatcher. It adds exactly one series.
- `backend/internal/platform/httpserver/observe.go` holds the rest of the
  exposition — process runtime, outbox backlog, relay counter, pool state.
  None of it carries a workspace label.
- `/metrics` is mounted in `backend/internal/compose/routes.go` behind
  `requireMetricsToken`.
- `backend/internal/compose/jobmetrics_test.go` renders the exposition text
  from a snapshot without a database, so the label rules are provable in the
  deterministic lane.

## History

Adopted from the retired specification, decided 2026-08-03. Rewritten in plain
language 2026-08-19. The source is marked proposed; the label ships on all four
gauges, so this record is active.

The source names the earlier metrics rule by an identifier and describes
amending its wording. The identifier is gone with the retired specification;
the bounded-cardinality rule it carried is restated in full above.

ADR-0103 records that the per-workspace label goes when the workspace fan-out
does. That collapse has not happened, so the label stands.
