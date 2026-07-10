# 0023 — Keyvault seam for secret material (worklist §1c platform seam)

Date: 2026-07-10. Implements the keyvault arm of the platform-parity push
ratified in the 2026-07-10 founder walkthrough
([docs/worklists/skeleton-baseline-2026-07-09.md](../docs/worklists/skeleton-baseline-2026-07-09.md)
§0b, §1c), the sibling of River (decisions/0021) and blobstore
(decisions/0022). This record ratifies the design; the concrete code steps
are in
[docs/worklists/keyvault-seam-2026-07-10.md](../docs/worklists/keyvault-seam-2026-07-10.md).

## Context

Secret material is persisted **inside domain and infrastructure tables**
today:

- `connector_connection.auth bytea` (core migration 0023) — the opaque
  connector credential bundle for one human's grant of one connector.
- `oauth.private_key` / `oauth.public_key bytea` (core migration 0025) — the
  OAuth signing keypair.

The capture path shows why this is the constraint that matters. The IMAP
connector deliberately **discards the password at login** (`imap.go`: "the
password is used here and discarded") because there is nowhere safe to persist
it — so today capture is effectively a one-shot pull, and durable incremental
`Sync` over a stored credential cannot work. Giving connectors a durable,
per-user credential that is **not** a bytea column in a tenant table is
exactly the queued EP05 §B reshape — "per-user + vault `credential_ref` —
structural, own PR arc" (docs/worklists/spec-drift-2026-07-08.md).

So keyvault is the seam that holds secret material **out of** the domain
tables, leaving an opaque `credential_ref` handle in the row.

**The entanglement is the whole story here** (and is why §0b says "pair this
with the EP05 §B capture-connection vault reshape — evaluate together"): a
`platform/keyvault` package with a fake and wiring but no secret actually
moved onto it is speculative dead code (T3/T8). Unlike blobstore — whose
callers already exist the day it lands — keyvault's only real consumer is the
migration of an existing secret onto it. Keyvault must therefore ship
**with** its first secret migrated, or not at all.

## Decision

**Adopt `internal/platform/keyvault` — the peer of `platform/jobs` — a
provider-agnostic secret store behind an opaque, workspace-scoped
`credential_ref`, with a config/local-backed provider and an in-memory fake;
and in the same PR migrate the first real secret (`connector_connection.auth`)
onto it, replacing the tenant-table bytea column with a `credential_ref`.**

### Why a `platform/` package and NOT a frozen `shared/ports/` seam

Same reasoning as blobstore (decisions/0022): the `Vault` interface
(`Put`/`Get`/`Delete` by ref) is technical plumbing with one substitution axis
(a real backend vs the memory fake) and no cross-module provider registry, so
it lives in the `platform` package, not `shared/ports/`. The `connector` port
in `shared/ports/connector` does **not** need to import keyvault: the
`credential_ref` is just an opaque string handle on the domain row; the
`capture` module resolves the ref through `platform/keyvault` and hands the
connector its `Auth` bytes exactly as before — the port seam is unchanged.

**Spec touchpoint (P3):** verify against `contract/interfaces.md` §1 (the
capture seam) and any declared vault/credential interface when the sibling
spec tree is reachable (it is not from this checkout — worklist §3). If the
spec freezes a vault interface, it becomes a `ports/` seam.

### Sequencing and scope — the load-bearing rule

Keyvault ships as **one reviewable PR that introduces the seam AND migrates
the smallest real secret onto it**, deferring the broader product reshape:

- **In scope, now:** the `Vault` interface + memory fake + config/local-backed
  provider + compose wiring + `/readyz` probe; a `credential_ref text` column
  added to `connector_connection` (additive migration); the `capture` write
  path stores the credential bundle in the vault and records the ref; the read
  path resolves the ref back to `Auth`; the `auth bytea` column is
  deprecated/dropped **additively** (write-to-vault first, backfill, then drop
  in a later migration — never a destructive single step).
- **Explicitly out of scope this pass, the EP05 §B product arc it unblocks:**
  multiple per-user connections, the connection-management contract surface
  and UI, and connector credential *rotation*. Those now have a vault to build
  on and are their own arc.
- `DECISION (founder)`: are the `oauth.private_key/public_key` keypairs in
  scope of this push or a follow-on? **Recommendation: connector auth only in
  this push**; the OAuth signing keys are a distinct migration (different
  lifecycle, different consumers) and fold onto the same seam next.

### The keyvault / DB boundary

| | **DB row** (`connector_connection`) | **Keyvault** (`platform/keyvault`) |
|---|---|---|
| Holds | Non-secret metadata + the tenant anchor + `credential_ref` | The secret bytes, addressed by the ref |
| Isolation | RLS via `WithWorkspaceTx` GUC | Ref is workspace-scoped; resolving it cross-tenant fails |
| Authority | System of record for the connection | Custodian of the secret only |

The rule: **the row owns the connection and its isolation; the vault owns only
the secret, addressed by an opaque ref.** A `credential_ref` leaked from one
tenant's row cannot resolve another tenant's secret — the ref is
workspace-scoped and resolution re-checks the tenant, so a stolen ref is inert
without also defeating RLS on the row. Keyvault does **not** store blobstore's
infrastructure credentials or the app's own DB DSN (both are cold-boot
prerequisites — a vault that needs the DB, and a store whose key lives in the
vault, would be a bootstrapping cycle; those stay operator config/env).

This composes cleanly with the existing egress-hygiene layer and does not
replace it: `model.SecretStripper` (`ai/stripper.go`) keeps secrets **out of
model-bound payloads at egress**; keyvault is where secrets live **at rest**.
Different jobs, both kept.

### Composition and layout

- **`internal/platform/keyvault`** (new) — the `Vault` interface, `memoryVault`
  fake, a config/local-backed provider (encrypted-at-rest via a
  config-sourced key; the build plan pins the mechanism), `New` from config,
  and a health probe. Owns no domain.
- **`internal/compose`** — the `Vault` is constructed from config in `cmd` and
  threaded into `compose.New` via an `Option`, handed to the `capture`
  consumer (the connector Authenticate/Sync path). `/readyz` probe registered
  here.
- **`cmd/api` / `cmd/worker`** — build the `Vault` from operator config (the
  root key / backend endpoint). The capture path is exercised by both roles
  (API authenticates a connection; worker syncs), so both wire the vault.

### Schema / migration ownership

Additive migration on `connector_connection`: add `credential_ref text NULL`.
The `auth bytea` column stays through the transition (write-to-vault + record
ref + backfill existing rows), and is dropped in a **later** additive
migration once no reader depends on it — never in the same destructive step,
matching the repo's additive-migration rule (DO NOT TOUCH: shipped core
migrations are additive-only). No new tenant table; RLS on
`connector_connection` is unchanged.

## Consequences

- **Capability unblocked:** durable per-user connector credentials become
  possible, which is what lets capture move from one-shot pulls to incremental
  `Sync` — the EP05 §B arc can proceed on top of this seam.
- **Security posture win:** tenant-supplied connector secrets leave the domain
  table for a vault addressed by an opaque ref; a stolen `connector_connection`
  row no longer carries the live credential bytes.
- **New dependency:** the config/local vault backend's crypto (the build plan
  pins it — standard library / a single vetted lib). Pure Go; image-pin and
  SonarCloud posture unaffected.
- **Ops surface:** `/readyz` gains a keyvault probe.
- **Blast radius contained by design:** `platform/keyvault` new; `compose` and
  the two `cmd` roles wire it; `capture` gains ref-based credential I/O; one
  additive migration. The `connector` port is unchanged; the broader EP05 §B
  contract/UI reshape is deliberately out of scope.
- **Integration lane proves it, zero-skip:** a round-trip test (Authenticate
  stores → ref recorded → Sync resolves the same `Auth`), workspace-ref
  isolation (a ref from workspace A does not resolve under workspace B), and
  the additive-migration backfill; the memory fake keeps unit tests hermetic.

## Why blobstore and keyvault are two sequential PRs, not one combined push

Recommended sequence: **blobstore first (decisions/0022), then keyvault** —
two PRs, mirroring how River shipped as its own PR under the same "one
platform push" umbrella.

1. **Independence and risk asymmetry.** Blobstore is self-contained and its
   callers already exist (the schema commits to `storage_key`; privacy needs
   the object hooks). Keyvault is entangled with EP05 §B — a structural,
   contract-adjacent arc — and cannot ship without migrating a real secret.
   Combining them yokes a low-risk gap-fill to a higher-risk secret migration
   in one unreviewable diff.
2. **The shared plumbing is shallow.** The only overlap is "both handle
   secrets," and it dissolves under inspection: blobstore's own access key is
   operator config on the cold-boot path and is explicitly **not** a keyvault
   client (§ boundary rules in both ADRs). They share no code; combining buys
   nothing.
3. **Reviewability.** Two focused PRs each mirror the River template (a
   `platform/<seam>` package + fake + real impl + compose wiring + a
   zero-skip integration proof), each independently revertible.
