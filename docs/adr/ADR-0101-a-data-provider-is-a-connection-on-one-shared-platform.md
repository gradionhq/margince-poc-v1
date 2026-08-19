# ADR-0101 — A licensed data provider is a connection on one shared platform, not a feature inside a domain

**Status:** Active — the platform, the port and the Settings surface are
built. Company subjects are **not built**; see below.

**Decided:** 2026-08-11

## The decision

Margince has two kinds of external connection and must stop calling them one
word. A sync connector mirrors a remote system: it holds a cursor, streams
records in, and follows the connector write shape. A data provider answers one
question at a time — we send an identifier, it returns assertions, we pay per
result — with no cursor and no mirror. Neither seam absorbs the other.

The data-provider half is a platform, not a person feature. One module owns the
connection registry, the metered run ledger, the credit reservations and the
vendor adapters. Adding a second provider is a descriptor entry, an adapter, an
allowlisted host and a fixture — not a second copy of the budget machinery.

The split follows one rule: facts about the connection belong to the platform,
facts about the subject belong to the domain. The platform decides whether a
run may happen, what it may cost and whether it succeeded. It never decides
what the answer means, so a completed run's claims are written by the domain
that owns them.

A run carries a typed foreign key to its subject, never a bare untyped id.
Dropping referential integrity would mean an erasure no longer reaches that
person's run history by construction. Each provider also declares three
behaviours rather than inheriting the first vendor's: whether it answers
synchronously, by polling, or by callback; what it charges for; and what it
costs.

## Why

The first provider connection grew a run ledger, credit reservation, a run
state machine, a settings surface, an egress allowlist entry, a kill switch and
a deterministic fake for CI — all inside the people module, because that
provider enriches people. None of that machinery is about people. A second
provider would either copy it or reach into the people module for facts that
have nothing to do with people.

Money is the invariant the platform protects. A reservation covers the whole
worst case before submitting, a crashed worker resolves to an unknown
submission rather than retrying a charge, and a duplicate trigger returns the
run already in flight instead of buying the same answer twice.

## What it binds in this repository

- `backend/internal/modules/integrations/` is the platform module. Its
  `doc.go` declares the tables it owns: `provider_connection`,
  `provider_connection_budget`, `provider_run`, `provider_run_reservation`.
- `backend/internal/shared/ports/provider/provider.go` holds the seam — the
  `Adapter` facing the vendor and the `RunService` facing the domain. Being
  stdlib-only it cannot name a transaction, so the callbacks that must run
  inside one live in `runs.go` as func types, supplied by compose from the
  people module: `WriteClaims`, `FenceSubjectFunc`, `DuplicateClusterFunc`,
  `SubjectIdentifiersFunc`.
- `person_provider_claim` is owned by `backend/internal/modules/people/`, not
  by the platform. `mergerelink.go` relinks claims to the surviving person on
  a merge rather than dropping them.
- `backend/migrations/core/0219_provider_integrations.up.sql` creates
  `provider_run` with `subject_kind` checked against `('person','scrubbed')`
  and a typed `person_id uuid NULL REFERENCES person(id) ON DELETE CASCADE`,
  bound by a shape check so a person subject implies a non-null person id.
- `Descriptor` in the port declares the transport (`synchronous`, `polled`,
  `callback`), the `BillingBasis` (`per_successful_result`, `per_request`,
  `per_record_subscription`) and one allowlisted `EgressHost`.
  `backend/internal/modules/integrations/surfe/` is the first adapter.
- `frontend/src/screens/integrations-provider.tsx` is the Settings card:
  connect a key, read the remaining credits, choose whether new contacts are
  enriched automatically, and stop the flow or destroy what was bought.

## What is not built

The platform is still closed to person subjects. `provider_run.subject_kind`
admits only `person` and `scrubbed`, so a company data provider — a business
registry, a firmographic feed — has nowhere to land.

Opening it is a real decision rather than a column addition, because a second
company-enrichment path already exists: the deep read of a company's own
website, which feeds `organization_fact`. The founder settled the precedence on
2026-08-11. For companies the website research is primary and provider data
enhances it; where both assert the same field the website value wins, and the
provider value is kept beside it with its own provenance. What is owed is the
vocabulary — `organization_fact` has no field for legal form, register number,
officers or filed accounts, and that widening is not designed yet.

## History

Adopted from the retired specification, decided 2026-08-11. Rewritten in plain
language 2026-08-19. The platform, the port, the typed subject key and the
Settings surface have all shipped.

An amendment the same day surveyed eight further providers and found three
platform rules that were really facts about the first vendor: that polling is
the default, that a failed hand-off can be recovered by re-reading a job
handle, and that billing is always per successful result. All three
corrections are in the shipped port.
