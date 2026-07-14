# Blobstore seam — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development or superpowers:executing-plans to
> implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Introduce `platform/blobstore` — object-bytes I/O behind a
provider-agnostic `Store` (MinIO/S3 impl + in-memory fake) — and wire the
smallest genuine consumers: attachment content write/read behind the existing
`attachment.storage_key`, plus the Art. 17 erasure object-deletion hook and
SAR object inclusion. Fills a schema gap that already exists (the schema
commits to object keys; nothing stores objects).

**Architecture:** A new `internal/platform/blobstore` package (peer of
`platform/events` / `platform/jobs`) owns the `Store` interface, an
`s3Store` (S3-compatible; MinIO in dev), and a `memoryStore` fake. The store
is constructed from config in `cmd` and threaded through `internal/compose`
into `capture` (attachment content) and `privacy` (erasure/SAR object hooks).
The DB row stays the system of record and the tenant anchor; the store holds
only opaque bytes at a workspace-prefixed key.

**Tech Stack:** Go 1.26.5, pgx v5.10.0, an S3 client (pin `minio-go/v7` —
first choice, since dev/CI is MinIO and it is the lighter dep; fall back to
`aws-sdk-go-v2/service/s3` only if a non-MinIO S3 target is required),
Postgres 16, MinIO (digest-pinned) in `infra/docker-compose.dev.yml`.

## Global Constraints

- Go **1.26.5**; the S3 client pinned in `go.mod`; `go mod tidy`, commit
  `go.sum`. New image (MinIO) digest-pinned in compose and gated by
  `check-image-pins` (bump tag + digest together, everywhere or nowhere).
- Every new hand-written `*.go` starts with the two-line BUSL-1.1 SPDX header
  (enforced by `backend/license_test.go`). Not on `*_gen.go`.
- Craft gate (diff-scoped, pre-push, BLOCKER): **never swallow an error**
  (an object Put/Delete failure must surface, never `_ =`); **no
  `time.Sleep`/real-clock/real-network in tests** — the memory fake carries
  unit tests, the MinIO container carries the integration lane.
- Integration tests are `//go:build integration`, gate on `MARGINCE_TEST_*`,
  and a skip fails the lane (`make test-integration`).
- Non-test, non-generated Go files stay < 500 LOC (`go-file-length`).
- Commits signed off (`git commit -s`); `PATH="$(go env GOPATH)/bin:$PATH"
  make check` green before push; `make test-integration` green (needs
  `make db-up`).
- **Write-shape untouched:** the `attachment` row + audit + outbox still
  commit in the one storekit tx. The object `Put` happens around it
  (put-then-commit); the store never writes a DB row.

## File Structure

- **Create** `backend/internal/platform/blobstore/blobstore.go` — the `Store`
  interface, `Object` metadata, sentinel errors (`ErrNotFound`), the
  workspace-key helper.
- **Create** `backend/internal/platform/blobstore/memory.go` — `memoryStore`
  fake (map-backed, concurrency-safe), the default for unit tests / offline.
- **Create** `backend/internal/platform/blobstore/s3.go` — `s3Store` over the
  pinned S3 client; `New` from `Config`; a `Ping`/health method for `/readyz`.
- **Create** `backend/internal/platform/blobstore/blobstore_test.go` — unit
  tests over the fake (round-trip, not-found, workspace-key derivation).
- **Create** `backend/internal/platform/blobstore/s3_integration_test.go`
  (`//go:build integration`) — put→get→delete against real MinIO; isolation.
- **Modify** `backend/api/crm.yaml` — add the minimal `/attachments` paths
  (additive); regenerate `internal/contracts` + `compose/stubs_gen.go` via
  `make gen`.
- **Modify** `backend/internal/compose/…` — a `WithBlobstore(store)` compose
  `Option`; hand the store to the `activities` attachment handlers and the
  privacy engines; register the module handlers so they shadow the generated
  501 stubs; register the `/readyz` probe.
- **Modify** `backend/internal/modules/activities/…` — the attachment store
  (INSERT/SELECT/DELETE the `attachment` row via the storekit write-shape,
  RBAC-gated) and the multipart upload/download handlers (`activities` owns the
  `attachment` table per `tableownership_test.go`). This is the seam's first
  production writer + reader.
- **Modify** `backend/internal/modules/privacy/erasure.go` — after deleting
  `attachment` rows for the subject, delete their objects from the store
  (retry-until-gone; a store miss on an already-deleted object is not an
  error).
- **Modify** `backend/internal/modules/privacy/sar.go` — include the object
  (or a manifest entry) in the SAR export.
- **Modify** `backend/cmd/api/main.go`, `backend/cmd/worker/main.go` — build
  the `Store` from config and pass `WithBlobstore`.
- **Modify** `infra/docker-compose.dev.yml` — uncomment MinIO.
- **Modify** `.env.template`, `docs/reference/configuration.md` — blobstore
  config vars (endpoint, bucket, region, access key/secret, path-style,
  enable flag).
- **Modify** `backend/internal/platform/httpserver/observe.go` (or wherever
  `/readyz` composes its probes) — add the blobstore dependency probe.

## The four checkpoint questions, answered up front

**1. What is the `Store` interface, and who calls it?**

```go
type Store interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
    Get(ctx context.Context, key string) (io.ReadCloser, Object, error) // ErrNotFound if absent
    Delete(ctx context.Context, key string) error                       // idempotent: absent == ok
    Stat(ctx context.Context, key string) (Object, error)
}
```

Callers, this pass: the `activities` attachment handlers (upload writes the
object + row; download streams it; delete removes both), `privacy/erasure`
(delete objects for an erased subject), `privacy/sar` (include objects). Keys
are derived, never client-supplied:
`WorkspaceKey(workspaceID, entity, id)` → `"<workspace_id>/attachment/<id>"`.

**2. What schema / migration does it need?**

No DB migration. `attachment` (0011) already has `storage_key NOT NULL`,
`content_type`, `byte_size`, `checksum`; `organization.logo_object_key`
exists. The store addresses bytes by the key on the row. No `workspace_id` on
the store (isolation is in the key). No new table, no RLS change. The only
contract change is **additive**: new `/attachments` paths in `crm.yaml`
(regenerated), so the oasdiff breaking-change gate stays green.

**3. How does it compose through `internal/compose`?**

`platform/blobstore` owns the lifecycle. `cmd` builds the `Store` from config
and passes `compose.WithBlobstore(store)`; `compose.New` threads it to the
`activities` attachment handlers (which shadow the generated 501 stubs) and
the privacy engines, and registers the `/readyz` probe. A nil/disabled store
(no blobstore configured) is an explicit boot decision, not a silent
nil-deref: either the memory fake (dev without MinIO) or a boot error if a
consumer needs it — decide the default in Task 5 and make it loud.

**4. How does the integration lane prove it, zero-skip?**

`s3_integration_test.go` against real MinIO: put→get→delete round-trip;
`ErrNotFound` on a missing key; **workspace-key isolation** (a key under
workspace A is not reachable via a workspace-B-derived key). A `privacy`
integration test asserts that after erasure of a subject with an attachment,
the object is **gone** from the store (the Art. 17 honesty gate). The memory
fake carries the same contract in hermetic unit tests.

---

## Task 1: `platform/blobstore` interface + memory fake

**Files:** create `blobstore.go`, `memory.go`, `blobstore_test.go`.

- [ ] **Step 1: Write the failing unit test** over the fake — round-trip
  (`Put` then `Get` returns the same bytes + `Object` metadata), `Get` on a
  missing key returns `ErrNotFound`, `Delete` is idempotent, and
  `WorkspaceKey` derives distinct keys for distinct workspaces.
- [ ] **Step 2: Run — expect FAIL** (`blobstore` undefined).
- [ ] **Step 3: Implement `Store`, `Object`, `ErrNotFound`, `WorkspaceKey`,
  and `memoryStore`.** The fake is map-backed with a mutex; `Get` returns an
  `io.NopCloser` over a copy so callers can't mutate stored bytes.
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** — `feat(blobstore): Store seam + in-memory fake`.

## Task 2: MinIO/S3 implementation + `/readyz` probe + compose wiring

**Files:** create `s3.go`, `s3_integration_test.go`; modify compose Option
and `/readyz`; uncomment MinIO in compose; add config vars.

- [ ] **Step 1: Uncomment MinIO** in `infra/docker-compose.dev.yml`
  (digest-pin the image; add the console/API ports and a named volume as the
  commented block already sketches). `make db-up` brings it up.
- [ ] **Step 2: Add the S3 dep** — `cd backend && go get github.com/minio/minio-go/v7@latest && go mod tidy`.
- [ ] **Step 3: Write the failing integration test** (`//go:build integration`)
  — put→get→delete round-trip, `ErrNotFound`, and workspace-key isolation,
  against MinIO via `MARGINCE_TEST_*` (add `MARGINCE_TEST_BLOBSTORE_*` to the
  env contract).
- [ ] **Step 4: Run — expect FAIL** (`s3Store` undefined).
- [ ] **Step 5: Implement `s3Store`** — `New(Config)` builds the client
  (endpoint, bucket, region, creds, path-style), ensures the bucket exists at
  boot, and maps a not-found provider error to `ErrNotFound`. Add a
  `Ping`/health method (bucket-exists check) for `/readyz`.
- [ ] **Step 6: Add the compose `Option` + `/readyz` probe.**
  `WithBlobstore(store)`; the probe names "blobstore" when unreachable (503),
  matching the DB/Redis probes.
- [ ] **Step 7: Run the integration test — expect PASS.**
- [ ] **Step 8: Commit** — `feat(blobstore): MinIO-backed Store, readyz probe, compose Option`.

## Task 3: `/attachments` contract + `activities` handlers (the first real writer)

**Files:** modify `backend/api/crm.yaml` (additive paths) + `make gen`;
create the attachment store + handlers in `backend/internal/modules/activities/`;
register them in `internal/compose`.

- [ ] **Step 1: Add the minimal `/attachments` paths to `crm.yaml`** —
  `POST /attachments` (multipart: `entity_type`, `entity_id`, `file`),
  `GET /attachments/{id}` (streams the object; `Content-Disposition` +
  `Content-Type` from the row), `DELETE /attachments/{id}`,
  `GET /attachments?entity_type=…&entity_id=…` (list metadata). Reuse the
  existing entity-type enum. Run `make gen`; commit the regenerated
  `internal/contracts` + `stubs_gen.go` (drift gate).
- [ ] **Step 2: Confirm additive** — `make check-contract-breaking` (oasdiff)
  green: new paths only, no existing operation changed.
- [ ] **Step 3: Write the failing tests** — (a) unit/integration: upload
  writes the object then commits the `attachment` row + audit + outbox in the
  one storekit tx (put-then-commit); download returns the bytes; delete
  removes both; (b) RBAC/row-scope: a caller who cannot see the parent entity
  gets 404 (existence-hiding), not the object; (c) a rolled-back upload leaves
  **no row** (orphan object is acceptable, GC-swept — assert no row).
- [ ] **Step 4: Run — expect FAIL** (handlers are the generated 501 stubs).
- [ ] **Step 5: Implement** — the `activities` attachment store derives the
  key (`WorkspaceKey`), `Put`s the object, then writes the `attachment` row via
  `storekit` (`Audit` + `Emit`) inside `WithWorkspaceTx`, RBAC-gated at entry
  (`auth.Require` + `auth.EnsureVisible` on the parent entity). The download
  handler resolves the row (row-scope gate first), then `Get`s by its
  `storage_key` and streams. Delete removes the row (write-shape) then the
  object. Handlers shadow the 501 stubs in compose. `captured_by` is stamped
  from the principal, never the body.
- [ ] **Step 6: Run — expect PASS**; `make check` green.
- [ ] **Step 7: Commit** — `feat(activities): /attachments upload/download over blobstore`.

## Task 4: privacy object hooks (erasure + SAR) — the GDPR-honesty gate

**Files:** modify `privacy/erasure.go`, `privacy/sar.go`; integration test.

- [ ] **Step 1: Write the failing integration test** — seed a subject with an
  attachment (row + object); run Art. 17 erasure; assert the `attachment` row
  is gone **and** the object is gone from the store. A second erasure (or a
  crash-retry) is idempotent (a missing object is not an error).
- [ ] **Step 2: Run — expect FAIL** (object still present).
- [ ] **Step 3: Implement** — erasure, after deleting the subject's
  `attachment` rows, deletes their objects from the store (collect the
  `storage_key`s in the same query that selects/deletes the rows, delete
  after commit, retry-until-gone; `Delete` is idempotent so a re-run is safe).
  SAR includes the object (or a manifest entry with `storage_key`, size,
  checksum) in the export.
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** — `feat(privacy): erase and export attachment objects via blobstore`.

## Task 5: cmd wiring + docs

**Files:** modify `cmd/api/main.go`, `cmd/worker/main.go`, `.env.template`,
`docs/reference/configuration.md`.

- [ ] **Step 1:** Build the `Store` from config in both roles and pass
  `WithBlobstore`. Decide the no-blobstore default and make it **loud** (dev
  without MinIO → memory fake with a startup log line; a consumer that needs
  the store with none configured → boot error, never a nil-deref at request
  time).
- [ ] **Step 2:** Add the blobstore vars to `.env.template` and the flag/env
  table in `configuration.md` (this was deferred in worklist §1a PR A pending
  this ADR).
- [ ] **Step 3:** `PATH="$(go env GOPATH)/bin:$PATH" make check` +
  `make test-integration` green.
- [ ] **Step 4: Commit** — `feat(cmd): wire blobstore from config; document its env`.

---

## Self-review checklist (run before opening the PR)

- **Not speculative:** the seam ships with a real production writer + reader
  this PR (the `/attachments` upload/download in `activities`) plus the privacy
  erasure/SAR hooks — used, not ahead of use. ✅
- **Contract additive:** new `/attachments` paths only; oasdiff green; regen
  committed (drift gate). ✅
- **Write-shape intact:** row + audit + outbox still one storekit tx; object
  Put is around it, put-then-commit; store writes no DB row. ✅
- **Tenant isolation:** keys are workspace-prefixed and derived, never
  client-supplied; the object is only reachable through an RLS-visible row;
  integration test proves cross-workspace keys don't collide. ✅
- **GDPR honesty:** erasure deletes objects (gated by test), SAR includes
  them; `Delete` idempotent for crash-retry. ✅
- **Errors never swallowed:** every `Put`/`Get`/`Delete` failure surfaces;
  messages actionable, no bucket/endpoint leaked to a client. ✅
- **Ports-vs-platform:** interface in `platform/blobstore`, not
  `shared/ports/`; spec touchpoint flagged. ✅
- **Gates:** license headers; image-pin on MinIO; no `time.Sleep`; file-length
  < 500; `make check` + `make test-integration` green, zero `--- SKIP`. ✅

## Open questions to confirm during implementation (not blockers)

1. **S3 client choice** — `minio-go/v7` (chosen: lighter, MinIO-native for
   dev/CI) vs `aws-sdk-go-v2/service/s3`. Confirm at `go get` time.
2. **No-blobstore default** — memory fake vs boot error when a consumer needs
   it. Chosen: memory fake in dev (loud log), boot error in a role that must
   serve attachments with none configured.
3. **Attachment size cap / streaming** — enforce a max upload size and stream
   rather than buffering the whole body; pick the cap during implementation
   (mirror any existing request-body limit in the chassis).
