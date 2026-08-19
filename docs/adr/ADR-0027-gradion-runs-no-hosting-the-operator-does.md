# ADR-0027 — Gradion runs no hosting; whoever deploys an installation operates it

**Status:** Active
**Decided:** 2026-06-17

## The decision

Gradion is a software vendor and never an operator. It runs no production
hosting, holds no customer database, runs no always-on service on behalf of a
customer, and supplies no inference. Every running installation is operated by
either a hosting partner or by the customer who self-hosts it. Data residency is
whatever region the operator names; the European default is delivered by
choosing a European partner, not by Gradion running a region. Gradion publishes
signed releases and the operator decides when to apply them.

## Why

A small software company that also runs production infrastructure takes on 24/7
operations, capital cost, and the legal role of data processor at scale. It also
competes with the hosting partners the license and the go-to-market model depend
on. Without this rule the deployment documentation drifts back toward "a
Gradion-hosted region", which is a promise nobody at Gradion can keep. Keeping
the vendor out of the data path is also what makes an air-gapped installation an
ordinary case rather than a special build.

## What it binds in this repository

- `docs/deployment.md` is written for a self-hoster. It ships
  deployment-target-agnostic container material and says outright that a
  concrete deployment — its domain, secrets, and platform manifests — belongs to
  the operator's own infrastructure repository, not to this one.
- The root `Dockerfile` builds three targets: `api`, `worker`, and `web`. There
  is no fourth image for a control plane, a tenant router, or a signup service.
- `scripts/deploy/db-bootstrap.sql`, `scripts/deploy/api-entrypoint.sh` and
  `scripts/deploy/worker-entrypoint.sh` hand the operator the database roles and
  the migrate-then-serve sequence. Nothing calls home to run them.
- Bootstrap is config-driven and local. The installation reads `margince.yaml`
  for its organization identity and its first admin, documented in
  `docs/reference/configuration.md`. No remote service issues a tenant.
- `backend/internal/platform/licensecheck` verifies the entitlement token
  in-process, running a WebAssembly module bundled in the binary. There is no
  license server for an installation to reach.
- `backend/internal/platform/blobstore` reads its endpoint from
  `MARGINCE_BLOBSTORE_ENDPOINT`. There is no Gradion-operated bucket and no
  default endpoint pointing at one.
- `LICENSE` (Business Source License 1.1) carries the Authorized Hosting Partner
  term: hosting the product for third parties requires a partner agreement,
  which is the commercial shape of this decision.
- `README.md` states the same split — free internal use up to ten seats,
  self-host or partner-hosted alike.

## History

Adopted from the retired specification, decided 2026-06-17. Rewritten in plain
language 2026-08-19.

The source canonicalized an earlier ratification from 2026-06-13 that until then
lived only as notes in the economics, go-to-market, and license documents.

The source also singles out the always-on connector for cloud assistants as the
one component someone must run publicly, and rules that the operator runs it.
That still holds, though the spelling changed: the governed tool surface is now
served by `cmd/api` at `/mcp` on the same origin as the rest of the API, so
there is no separate connector process for anyone to host.

The source ties the European inference tier to a partner and deliberately names
no provider. Nothing in this repository pins one either — the model seam takes
whatever endpoint and credentials the operator configures.
