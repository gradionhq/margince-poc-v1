# ADR-0051 — Object storage is self-hosted by default and a public cloud is opt-in

**Status:** Active — one pluggable seam, an S3-protocol client, and a
self-hosted store in every lane. No public-cloud endpoint ships as a default.
**Decided:** 2026-06-29

## The decision

All object bytes — attachments today, exports and transcripts later — go through
one seam, and no caller names a storage backend. The wire protocol is the S3
API, which self-hosted stores speak as well as public clouds do, so switching
backends is configuration and never a code change. Every lane this repository
ships points at a self-hosted store running on the operator's own hardware.
Reaching a bucket at a public cloud provider is something an operator configures
deliberately by setting an endpoint and credentials for it. Nothing in the
product picks such a provider on the operator's behalf.

## Why

Customer data must stay inside the residency boundary the operator promised — on
the customer's own hardware for a sovereign installation, or in the named region
for a hosted one. The word "S3" hides the distinction that matters: it means
both a specific US-operated service and a wire protocol that self-hosted stores
implement. Writing the protocol into the seam and leaving the endpoint to the
operator gets the flexibility without the default. A default is a promise, and
the promise has to be safe for the most constrained customer rather than
convenient for the easiest deployment.

## What it binds in this repository

- `backend/internal/platform/blobstore/blobstore.go` declares the seam. `Store`
  has `Put`, `Get`, `Delete` and `DeletePrefix`, and keys are opaque to it. No
  caller names a backend.
- `backend/internal/platform/blobstore/s3.go` is the shipping provider, built on
  the `minio-go` client. The endpoint, credentials, bucket, region and whether
  to use TLS are all configuration. Nothing in the file names a provider.
- `backend/internal/platform/blobstore/env.go` is the configuration surface:
  `MARGINCE_BLOBSTORE_ENDPOINT`, `_ACCESS_KEY`, `_SECRET_KEY`, `_BUCKET`,
  `_REGION` and `_USE_SSL`. Credentials come from the environment and never from
  a command-line flag, which would leak them into the process table.
- No endpoint is compiled in. `FromEnv` returns `configured=false` when the
  endpoint is unset, and the installation boots with the attachment endpoints
  answering 501. An operator who wants object storage names their own endpoint,
  which is how the product avoids picking one for them.
- `infra/docker-compose.dev.yml` runs a self-hosted MinIO for development, and
  the root `Makefile` points the development and test lanes at it on
  `localhost:29000`. Development and production exercise the same S3 code path.
- Tenant isolation is a property of the key, not of the store.
  `blobstore.WorkspaceKey` builds `<workspace>/<kind>/<id>`, and `DeletePrefix`
  refuses a prefix that is empty or does not end at the `/` separator with
  `ErrInvalidPrefix`, so a sweep cannot reach into a sibling workspace whose id
  happens to extend the prefix.
- `backend/internal/platform/blobstore/memory.go` is the in-process fake for
  tests; `s3_integration_test.go` exercises the real protocol path.

## Not built yet

The source calls for the endpoint and region to be an audited setting on a
governed configuration surface, so that "where do the blobs live" is visible and
change is recorded. It is a process environment variable today, listed in
`docs/reference/configuration.md`, and a change to it is not audited. Moving it
into the installation settings table of ADR-0090 would discharge this, but the
migration is not designed.

## History

Adopted from the retired specification, decided 2026-06-29. Rewritten in plain
language 2026-08-19.

The source originally claimed this seam had already shipped, and a correction in
July 2026 recorded that it had not. It has since been built, and the seam lives
at `backend/internal/platform/blobstore` rather than at the path the source
named.

The source anticipated transcripts as the first object kind. Attachments and the
organization logo arrived first, which is what the shipped keys carry.
