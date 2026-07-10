# Keyvault seam — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development or superpowers:executing-plans to
> implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.
>
> **Sequenced AFTER the blobstore seam** (docs/worklists/blobstore-seam-2026-07-10.md)
> — see decisions/0023 for why the two are separate PRs.

**Goal:** Introduce `platform/keyvault` — secret storage behind an opaque,
workspace-scoped `credential_ref` (config/local-backed provider + in-memory
fake) — and in the SAME PR migrate the first real secret,
`connector_connection.auth`, off its tenant-table bytea column onto the vault,
leaving a `credential_ref` on the row. A seam with no secret migrated would be
dead code (T3/T8); the migration is what makes it real.

**Architecture:** A new `internal/platform/keyvault` package (peer of
`platform/jobs`) owns the `Vault` interface, a config/local-backed provider,
and a `memoryVault` fake. The vault is constructed from config in `cmd` and
threaded through `internal/compose` into the `capture` connector path. The DB
row stays the system of record and tenant anchor; the vault owns only the
secret bytes, addressed by an opaque ref. The `connector` port
(`shared/ports/connector`) is **unchanged** — `capture` resolves the ref to
`Auth` before handing the connector its credentials.

**Decision of record:** [decisions/0023-keyvault-seam.md](../../decisions/0023-keyvault-seam.md).

**Tech Stack:** Go 1.26.5, pgx v5.10.0, Postgres 16. Crypto for the local
provider: prefer the standard library (`crypto/aes` + `crypto/cipher` GCM)
with a config-sourced 32-byte root key; adopt a single vetted lib only if the
build surfaces a concrete need. No new infrastructure container.

## Global Constraints

- Go **1.26.5**; any new dep pinned in `go.mod`; commit `go.sum`.
- Every new hand-written `*.go` starts with the two-line BUSL-1.1 SPDX header
  (`backend/license_test.go`). Not on `*_gen.go`.
- Craft gate (diff-scoped, pre-push, BLOCKER): **never swallow an error** (a
  vault Put/Get/Delete failure must surface — and must never log the secret
  or the plaintext); **no `time.Sleep`/real-clock/real-network in tests**.
- **Secret hygiene:** the plaintext secret and the root key never reach a log
  line, an error message, or a model-bound payload. Error messages name the
  ref, never the secret. This composes with — does not replace —
  `model.SecretStripper`.
- Integration tests `//go:build integration`, gate on `MARGINCE_TEST_*`, and a
  skip fails the lane.
- **Additive migrations only** (DO NOT TOUCH: shipped core migrations are
  additive). The `auth bytea` column is dropped in a *later* migration, never
  in the same step that adds `credential_ref`.
- Non-test, non-generated Go files < 500 LOC; commits signed off;
  `PATH="$(go env GOPATH)/bin:$PATH" make check` + `make test-integration`
  green before push.
- **Scope is the seam + the `connector_connection.auth` migration only.** Do
  **not** build the EP05 §B per-user-connections reshape, the
  connection-management contract/UI, credential rotation, or the OAuth-key
  migration in this PR.

## File Structure

- **Create** `backend/internal/platform/keyvault/keyvault.go` — the `Vault`
  interface, `Ref` type, sentinel errors (`ErrNotFound`), the
  workspace-ref helper.
- **Create** `backend/internal/platform/keyvault/memory.go` — `memoryVault`
  fake (map-backed, concurrency-safe), default for unit tests / offline.
- **Create** `backend/internal/platform/keyvault/local.go` — the
  config/local-backed provider (AES-GCM at rest under a config root key),
  `New` from `Config`, a health method for `/readyz`.
- **Create** `backend/internal/platform/keyvault/keyvault_test.go` — unit
  tests over the fake and the local provider (round-trip, not-found,
  workspace-ref isolation, wrong-key decrypt failure).
- **Create** the additive migration
  `backend/migrations/core/00NN_connector_credential_ref.up.sql` /
  `.down.sql` — `ALTER TABLE connector_connection ADD COLUMN credential_ref text NULL;`
- **Modify** `backend/internal/compose/…` — a `WithKeyvault(vault)` Option;
  hand the vault to the capture connector path; register the `/readyz` probe.
- **Modify** `backend/internal/modules/capture/…` (the connection store +
  the connect/sync compose adapter, e.g. `compose/imapconnect.go`) — on
  Authenticate, store the credential bundle in the vault and record the
  `credential_ref`; on Sync, resolve the ref back to `Auth`. Backfill existing
  `auth bytea` rows into the vault.
- **Modify** `backend/cmd/api/main.go`, `backend/cmd/worker/main.go` — build
  the `Vault` from config and pass `WithKeyvault` (both roles: API
  authenticates, worker syncs).
- **Modify** `.env.template`, `docs/reference/configuration.md` — keyvault
  config vars (backend selector, root-key source).
- **Modify** the `/readyz` probe composition — add the keyvault probe.

## The four checkpoint questions, answered up front

**1. What is the `Vault` interface, and who calls it?**

```go
type Vault interface {
    Put(ctx context.Context, workspaceID uuid.UUID, secret []byte) (Ref, error)
    Get(ctx context.Context, workspaceID uuid.UUID, ref Ref) ([]byte, error) // ErrNotFound if absent
    Delete(ctx context.Context, workspaceID uuid.UUID, ref Ref) error        // idempotent
}
```

The `workspaceID` is part of every call so the provider scopes the ref to the
tenant and a ref cannot be resolved under the wrong workspace. Caller this
pass: the `capture` connector path (Authenticate stores, Sync resolves). The
`connector` port is untouched — it still receives `Auth` bytes.

**2. What schema / migration does it need?**

One additive migration: `connector_connection ADD COLUMN credential_ref text
NULL`. The `auth bytea` column **stays** through this PR (write-to-vault +
record ref + backfill existing rows), and is dropped in a **later** additive
migration once no reader depends on it. No new table; RLS on
`connector_connection` unchanged.

**3. How does it compose through `internal/compose`?**

`platform/keyvault` owns the lifecycle. `cmd` builds the `Vault` from config
and passes `compose.WithKeyvault(vault)`; `compose.New` threads it to the
capture connector adapter and registers the `/readyz` probe. Both `cmd/api`
and `cmd/worker` wire it (Authenticate runs in the API, Sync in the worker).

**4. How does the integration lane prove it, zero-skip?**

A round-trip test against the local provider on real Postgres: Authenticate
stores the credential → `credential_ref` recorded on the row → Sync resolves
the ref to the same `Auth` bytes → the connector authenticates. Plus:
**workspace-ref isolation** (a ref minted under workspace A returns
`ErrNotFound` when resolved under workspace B), and the **backfill** (an
existing `auth bytea` row is migrated into the vault and gains a ref). The
memory fake carries the contract in hermetic unit tests; the local provider's
AES-GCM round-trip and wrong-key failure are unit-tested.

---

## Task 1: `platform/keyvault` interface + memory fake

**Files:** create `keyvault.go`, `memory.go`, `keyvault_test.go`.

- [ ] **Step 1: Write the failing unit test** over the fake — `Put` returns a
  ref; `Get` with that ref + the same workspace returns the secret; `Get` with
  a different workspace returns `ErrNotFound`; `Delete` is idempotent.
- [ ] **Step 2: Run — expect FAIL** (`keyvault` undefined).
- [ ] **Step 3: Implement `Vault`, `Ref`, `ErrNotFound`, and `memoryVault`.**
  The ref embeds/incorporates the workspace so cross-workspace resolution
  fails by construction; the fake stores `(workspace, ref) → secret`.
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** — `feat(keyvault): Vault seam + in-memory fake`.

## Task 2: local (config-backed) provider + `/readyz` + compose wiring

**Files:** create `local.go`; modify compose Option and `/readyz`; add config.

- [ ] **Step 1: Write the failing unit test** for the local provider —
  AES-GCM round-trip; decrypt fails cleanly (surfaced error, no plaintext
  leak) under a wrong root key; workspace-ref isolation.
- [ ] **Step 2: Run — expect FAIL** (`local` undefined).
- [ ] **Step 3: Implement the local provider** — `New(Config)` reads the
  32-byte root key from config (base64 env / file path — the build decides,
  loud on a missing/short key at boot, never a silent zero key); encrypts
  secrets with AES-GCM; a health method for `/readyz`. Where the ciphertext
  lives (a `vault_secret` operational table vs a file) is decided here; if a
  table, it is operational infra (no `workspace_id` GUC on the vault's own
  storage — the workspace is inside the ref/AAD), added to the tenant-table
  fitness allowlist exactly as River's tables were (decisions/0021).
- [ ] **Step 4: Add the compose `Option` + `/readyz` probe** — `WithKeyvault`;
  the probe names "keyvault" when unhealthy (503).
- [ ] **Step 5: Run — expect PASS.**
- [ ] **Step 6: Commit** — `feat(keyvault): config-backed local provider, readyz, compose Option`.

## Task 3: the additive migration

**Files:** create `migrations/core/00NN_connector_credential_ref.{up,down}.sql`.

- [ ] **Step 1:** `ADD COLUMN credential_ref text NULL` on
  `connector_connection` (up); drop it (down). `auth bytea` untouched.
- [ ] **Step 2:** `make migrate` applies it; the schema fitness test
  (`backend/migrations/schema_integration_test.go`) stays green.
- [ ] **Step 3: Commit** — `feat(migrate): connector_connection.credential_ref column`.

## Task 4: capture stores/resolves via the vault + backfill

**Files:** modify the capture connection store and the connect/sync compose
adapter (e.g. `compose/imapconnect.go`).

- [ ] **Step 1: Write the failing integration test** — Authenticate stores the
  credential bundle in the vault and persists the `credential_ref` (not the
  bytes) on the row; Sync resolves the ref and authenticates; an existing
  `auth bytea` row is backfilled into the vault and gains a ref.
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** — on Authenticate, `Put` the credential bundle,
  record `credential_ref` (inside the connection's storekit write tx: row +
  audit + outbox; the vault Put is around it, put-then-commit like blobstore).
  On read/Sync, resolve `credential_ref` via the vault to `Auth`. A one-shot
  backfill (idempotent: skip rows that already carry a ref) migrates existing
  `auth bytea` rows. Reads prefer `credential_ref`; the `auth` column is read
  only as the backfill source, and is dropped in a later migration.
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** — `feat(capture): resolve connector credentials through the keyvault`.

## Task 5: cmd wiring + docs

**Files:** modify `cmd/api/main.go`, `cmd/worker/main.go`, `.env.template`,
`docs/reference/configuration.md`.

- [ ] **Step 1:** Build the `Vault` from config in both roles and pass
  `WithKeyvault`. A capture-capable role with no keyvault configured is a
  **boot error**, not a nil-deref at Authenticate time.
- [ ] **Step 2:** Add the keyvault vars to `.env.template` and the flag/env
  table in `configuration.md`.
- [ ] **Step 3:** `PATH="$(go env GOPATH)/bin:$PATH" make check` +
  `make test-integration` green.
- [ ] **Step 4: Commit** — `feat(cmd): wire keyvault from config; document its env`.

---

## Self-review checklist (run before opening the PR)

- **Not speculative:** the seam ships with a real secret migrated onto it
  (`connector_connection.auth` → `credential_ref`). ✅
- **Scope:** seam + connector-auth migration only; EP05 §B reshape, rotation,
  and OAuth-key migration deliberately out. ✅ (grep the diff for `oauth`,
  connection-list endpoints — no changes.)
- **Secret hygiene:** plaintext and root key never logged, never in an error
  message, never in a model-bound payload; composes with `SecretStripper`. ✅
- **Tenant isolation:** every call carries `workspaceID`; a ref cannot resolve
  under the wrong workspace (integration-proven); the row's RLS is unchanged. ✅
- **Additive migration:** `credential_ref` added; `auth bytea` dropped only in
  a later migration; backfill idempotent. ✅
- **Write-shape intact:** connection row + audit + outbox one storekit tx;
  vault Put around it, put-then-commit. ✅
- **`connector` port unchanged:** capture resolves the ref; the port still
  receives `Auth`. ✅
- **Ports-vs-platform:** interface in `platform/keyvault`, not `shared/ports/`
  (decisions/0023 rationale); spec touchpoint flagged. ✅
- **Gates:** license headers; vault storage table (if any) on the
  tenant-table fitness allowlist; no `time.Sleep`; file-length < 500;
  `make check` + `make test-integration` green, zero `--- SKIP`. ✅

## Open questions to confirm during implementation (not blockers)

1. **OAuth signing keys** (`oauth.private_key/public_key`) — this PR or a
   follow-on? `DECISION (founder)` — recommendation: follow-on (different
   lifecycle/consumers), fold onto the same seam next.
2. **Ciphertext storage** — a `vault_secret` operational table vs a file/dir.
   Recommendation: an operational table (rides the existing pool/migrator,
   allowlisted like River's tables), so the vault has no filesystem
   dependency and honors the same backup/restore as the DB.
3. **Root-key source and rotation** — base64 env var vs file path; rotation is
   out of scope here but the ref/AAD scheme should not foreclose it. Confirm
   the config surface with the founder.
4. **Spec touchpoint** — verify `contract/interfaces.md` §1 does not freeze a
   different vault/credential interface once the sibling spec tree is
   reachable (not reachable from this checkout — worklist §3).
