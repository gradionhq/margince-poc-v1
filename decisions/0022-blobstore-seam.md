# 0022 — Blobstore seam for object bytes (worklist §1c platform seam)

Date: 2026-07-10. Implements the blobstore arm of the platform-parity push
ratified in the 2026-07-10 founder walkthrough
([docs/worklists/skeleton-baseline-2026-07-09.md](../docs/worklists/skeleton-baseline-2026-07-09.md)
§0b, §1c). River shipped first (decisions/0021, PR #35); keyvault is the
sibling seam (decisions/0023). This record ratifies the design; the concrete
code steps are in
[docs/worklists/blobstore-seam-2026-07-10.md](../docs/worklists/blobstore-seam-2026-07-10.md).

"One platform push" in §0b names the initiative, not one PR — River landed
as its own PR, and blobstore does too. See decisions/0023 for why blobstore
and keyvault are sequenced as two PRs rather than combined.

## Context

The database already commits to an external object store, but there is no
store behind it:

- `attachment.storage_key` (core migration 0011) is `NOT NULL` and documented
  "S3/MinIO object key". `organization.logo_object_key` is the same shape.
- The contract carries the surface as a declared fast-follow:
  `crm.yaml` — `/attachments (S3/MinIO refs — schema exists; endpoints
  fast-follow)`.
- STATUS lists "transcript/blob-storage substrate" as an open arc.

Nothing can currently write an object, read it back, or delete it — **and
nothing in the codebase writes an attachment at all.** There is no
`INSERT INTO attachment` path, no `/attachments` endpoints (only the commented
fast-follow note), and no logo upload. The only code touching `attachment` is
`privacy` (`erasure.go` `DELETE`s the rows; `sar.go` `SELECT`s their
metadata). So the substrate the schema commits to is not merely empty — it has
no producer.

This is the crux the design has to answer honestly (T3/T8: no abstraction
without a concrete caller today): a `platform/blobstore` with only the privacy
hooks as "consumers" would guard objects that nothing creates — a speculative
substrate. The seam becomes real only when it ships **with its first
production writer**. The contract already reserves that writer as a
fast-follow (`/attachments`), and the schema already exists — so this push
builds the minimal `/attachments` upload/download surface as the writer, and
the seam lands used, not ahead of use.

The GDPR hooks then become genuinely load-bearing rather than decorative:
once attachments are written, `privacy/erasure.go` must delete the **objects**
(not only the rows) or an erasure leaves the bytes orphaned forever, and a SAR
must be able to include the object. Wiring those hooks in the same push keeps
Art. 17 honest from the first stored object.

The transactional outbox and River do not touch this: they move events and
jobs, not opaque bytes. Object storage is a distinct piece of technical
plumbing.

## Decision

**Adopt `internal/platform/blobstore` — the peer of `platform/events` and
`platform/jobs` — owning object-bytes I/O behind a provider-agnostic
interface, with a MinIO/S3-backed implementation and an in-memory fake.**
Uncomment MinIO in `infra/docker-compose.dev.yml`; add the blobstore config
vars to `.env.template` and `docs/reference/configuration.md`.

### Why a `platform/` package and NOT a frozen `shared/ports/` seam

The interface (`Store`: `Put`/`Get`/`Delete`/`Stat`) lives in-package, like
`platform/jobs`' `Config` and `platform/events`' relay — not under
`shared/ports/`.

`shared/ports/` is reserved for the spec's provider-agnostic, P-principle
architectural seams (`datasource`, `model`, `connector`, `retrieval`,
`jurisdiction`): interfaces the spec freezes, that modules depend on
abstractly, that carry additive provider mechanics and a registry. Blobstore
is none of those — it is technical plumbing (bytes in and out of an
S3-compatible store) with a single axis of substitution (S3-compatible vs the
memory fake) and no cross-module provider registry. The triad DAG
(`shared → platform → modules`) already lets `capture`/`privacy`/`collections`
consume a `platform` package directly, so no port indirection is needed.

**Alternative considered and rejected:** a frozen `shared/ports/blobstore`
interface. Rejected for lack of a second provider axis and lack of a spec
mandate to freeze the shape. **Spec touchpoint (P3):** if
`contract/interfaces.md` declares a blobstore seam interface, the spec wins
and this becomes a `ports/` seam. The sibling spec tree is not reachable from
this checkout (the `../margince/specs/` path is machine-specific — worklist
§3), so this is flagged for verification when the spec is reachable, not
resolved here.

### The blobstore / DB boundary (the load-bearing rule)

| | **DB row** (`attachment`, `organization`) | **Blobstore** (`platform/blobstore`) |
|---|---|---|
| Holds | Metadata + the tenant anchor (`workspace_id`, RLS) + `storage_key` | The opaque bytes, addressed by `storage_key` |
| Isolation | RLS via `WithWorkspaceTx` GUC | Key is workspace-prefixed by construction (`<workspace_id>/<entity>/<id>`) |
| Write | storekit `Audit` + `Emit` in the one domain tx | `Put(key, bytes)` around the tx |
| Authority | System of record | Byte custodian only — never writes a DB row |

The rule: **the row owns metadata and isolation; the store owns only bytes.**
Blobstore is not RLS-aware — isolation is enforced by the key derivation in
the calling module, which already runs inside `WithWorkspaceTx` and behind the
RBAC gate. A workspace-prefixed key means a tenant physically cannot address
another tenant's object, and a caller cannot forge a cross-tenant key without
first defeating RLS on the row that names it.

**Ordering / the honest hard cases (T7):**

- **Create** is put-then-commit: write the object first, commit the row
  second. A committed row therefore always has its bytes. A rolled-back tx
  can leave an orphan object (bytes with no row) — that is the safe direction
  (no dangling row promising bytes that aren't there); orphans are swept by a
  later GC pass, not left to masquerade as data.
- **Erasure** (Art. 17) is delete-row-then-delete-object, retried until the
  object is gone: a crash between the two leaves an orphan object, never a
  live row pointing at deleted bytes. The erasure engine must delete the
  objects, not only the rows — this is non-negotiable and gated by test.
- **SAR** (Art. 15) reads the object through the store to include it (or a
  manifest of it) in the export.

### How attachments fit the existing invariants (no registry changes)

The `/attachments` surface deliberately extends **no** spec-governed registry —
it composes with what exists, which is what keeps it "minimal":

- **Authority inherits from the parent entity.** `attachment` is not added as
  an RBAC object (that vocabulary lives in `identity/internal/policy`,
  decisions/0006, and is not this push's to change). An attachment has no
  independent existence: authority derives from its parent. Upload/delete
  require `ActionUpdate` on the parent object type and delete/read require the
  parent to be row-visible (`auth.EnsureLinkTarget` for person/organization/
  deal/lead; `auth.EnsureActivityVisible` for an activity parent) — the same
  dispatch `insertActivityLinks` already uses. A caller who cannot see the
  parent gets 404 (existence-hiding), never a leak.
- **Audit-only write-shape (no outbox event).** The mutation commits the row +
  `audit_log` in one storekit transaction (`create` on upload, `archive` on
  delete — both existing verbs), but emits **no** outbox event. This follows
  the established audit-only precedent for subordinate/config objects
  (record_grant, automation, product, offer lines): a polymorphic attachment
  has no event stream defined in events.md §4.1/§5, and inventing one without
  the spec would contradict the catalog's "no type may imply an undefined
  stream" rule. When the spec defines an attachment event stream, the `Emit`
  is added then.
- **Delete is a soft archive.** `DELETE /attachments/{id}` sets `archived_at`
  and audits `archive` — identical to how `deleteAutomation`/`deleteVoiceProfile`
  already behave. The object bytes are **not** purged by the user-facing
  delete; authoritative byte-erasure is the Art. 17 path (below), matching how
  every archived record's data persists until erasure.

### Scope discipline

- **In scope, now:** the `Store` interface + memory fake + MinIO impl +
  compose wiring + `/readyz` probe; the **minimal `/attachments` surface** that
  gives the seam its first production writer and reader (`POST /attachments`
  multipart upload → object + row; `GET /attachments/{id}` → stream;
  `DELETE /attachments/{id}`; `GET /attachments?entity=…` list), additive to
  `crm.yaml`; and the Art. 17 **erasure** hook that deletes an erased
  subject's objects plus SAR inclusion of attachment objects. The erasure hook
  is not optional: the moment an attachment can be written, an erasure that
  leaves its bytes is an Art. 17 regression.
- **Explicitly out of scope this pass, natural follow-ons:** organization-logo
  upload, presigned direct-upload URLs, and transcript / voice-corpus
  large-object storage. Each migrates onto the seam when its own ticket is
  touched. The `/attachments` surface is deliberately minimal — CRUD on a blob
  behind an RBAC/row-scope gate — not the full attachment product (versioning,
  inline previews, virus scanning).

### Composition and layout

- **`internal/platform/blobstore`** (new) — the `Store` interface, `memoryStore`
  fake, `s3Store` impl (S3-compatible; MinIO in dev), `New` from config, and a
  health probe. Owns no domain.
- **`internal/compose`** — the `Store` is constructed from config in `cmd` and
  threaded into `compose.New` via an `Option` (the same shape as the model path
  and the relay), then handed to the `capture`/`privacy` consumers. The
  `/readyz` probe is registered here.
- **`cmd/api` / `cmd/worker`** — build the `Store` from operator config
  (endpoint, bucket, region, access key/secret, path-style). **Blobstore's own
  infrastructure credentials come from operator config/env, NOT from keyvault**
  — a cold boot must reach the object store before any vault exists; folding
  the store's own access key into the vault would be a bootstrapping cycle.

### Schema / migration ownership

No new table is required: `attachment.storage_key`,
`organization.logo_object_key`, `content_type`, `byte_size`, and `checksum`
already exist (migration 0011). No `workspace_id` question arises for the
store itself — isolation lives in the key. If a bucket/prefix convention needs
recording it is a small additive migration or, more likely, pure config; the
build plan confirms none is needed.

## Consequences

- **Capability win:** attachments become actually storable and servable — the
  declared `/attachments` fast-follow gains both its substrate and its minimal
  endpoints, so the seam ships with a real producer rather than ahead of use.
- **GDPR honesty win:** erasure deletes objects (not only rows) and SAR can
  include them — closing a latent Art. 17 gap that exists today, gated by an
  integration test that asserts an erased subject's objects are gone.
- **New dependency:** an S3 client (`minio-go` or `aws-sdk-go-v2/service/s3`;
  the build plan pins one). Pure Go, digest-pinned MinIO in compose; image-pin
  and SonarCloud posture unaffected.
- **Ops surface:** `/readyz` gains a blobstore dependency probe that names the
  store when it is unreachable (503), matching the existing DB/Redis probes.
- **Contract change is additive:** the `/attachments` endpoints are new paths
  in `crm.yaml` (regenerated `internal/contracts`), so the oasdiff
  breaking-change gate stays green; no existing operation changes.
- **Blast radius is contained:** `platform/blobstore` new; `compose` and the
  two `cmd` roles wire it; a small `attachments` handler set owns the endpoints
  and `privacy` gains the object hooks. No outbox, RLS, or write-shape change.
- **Integration lane proves it, zero-skip:** a real MinIO container proves
  put→get→delete round-trips, workspace-key isolation, and erasure object
  deletion; the memory fake keeps unit tests hermetic and the offline/dev path
  toolchain-free.
