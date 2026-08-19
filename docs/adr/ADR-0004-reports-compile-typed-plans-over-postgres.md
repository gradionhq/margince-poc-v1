# ADR-0004 — Reports compile a typed plan to Postgres SQL; no second query engine

**Status:** Active
**Decided:** 2026-06-04

## The decision

A report is a typed, declarative plan that the server compiles into
parameterized Postgres SQL. Nobody — not a user, not a natural-language layer,
not an agent — submits SQL. The plan names an entity, a set of fields, filters
and grouping, all drawn from a closed vocabulary the server owns; anything
outside that vocabulary is refused rather than interpreted. We do not run a
second analytical engine alongside Postgres, and we do not split reporting into
its own service.

## Why

Two query engines mean two places a number can be computed and two chances for
them to disagree, which is exactly the failure this product exists to remove.
One engine also means one place to enforce permissions, so a report cannot
become a way around the access rules that guard the same rows elsewhere.
Compiling a plan instead of accepting SQL removes injection and invented joins
as a class: there is nowhere in the plan to put a table name or an expression.
The whole thing still runs inside one Postgres instance, which is what keeps a
self-hosted or air-gapped install possible.

## What it binds in this repository

- `backend/internal/compose/report.go` compiles the prebuilt report specs. Field
  vocabulary is closed per report, every identifier reaching the query text
  comes from those tables, and every value travels as a bind parameter.
- `backend/internal/compose/reportcatalog.go` holds the catalog of reports;
  `backend/internal/compose/reporthandlers.go` serves them.
- `ReportPlan` in `backend/internal/shared/ports/datasource/datasource.go` is
  the plan type — `Entity`, `Select`, `Filter`, `GroupBy`, and nothing
  free-form.
- `/reports/{report}` and `/reports/{report}/derivation` in
  `backend/api/crm.yaml` are the HTTP surface; the derivation route returns the
  source rows behind one aggregate.
- `backend/internal/modules/search/queryplan.go` is the peer grammar for the
  search and natural-language side, with `queryvalidate.go` deciding membership
  and `querysql.go` building the statement. Its comments state the same rule:
  the grammar has no free-text member anywhere a name is expected.
- `Server.RunReport` and `Server.ExplainReport` in
  `backend/internal/compose/nativeonlytools.go` refuse both routes when the
  installation runs in overlay mode, so the guard is server-side rather than a
  hidden screen.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19. The source pre-decided an escalation path: if a specific
heavy aggregate could not meet its latency budget on Postgres, it could move to
an embedded columnar engine behind the same plan type, under its own decision
record. No such escalation has been taken; the tree has one engine.
