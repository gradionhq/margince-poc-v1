# ADR-0048 — Connector credentials are sealed in the application database and never read back out

**Status:** Active
**Decided:** 2026-06-26

## The decision

Every stored connector credential — an OAuth refresh token, an API key, a
bot token — is encrypted and kept in the application's own database. No
deployment needs an external secrets manager. A domain row never holds the
credential bytes; it holds an opaque reference string, and only the
connector runtime resolves that reference in-process to make its outbound
call. No HTTP endpoint, MCP tool or admin screen returns a stored secret.
An operator replaces a credential; nobody reads one.

## Why

The product has to run on a customer's own hardware and in partner-hosted
installations, where a managed secrets service cannot be assumed to exist.
Making the connectors depend on one would fork the connector code per
environment. Keeping the store in the database and leaving only the sealing
key configurable gives one binary that runs everywhere. Refusing every
read-back path also means there is no privileged debug route to audit.

## What it binds in this repository

- `backend/internal/platform/keyvault/` is the seam. `keyvault.Vault`
  declares `Put`, `Get`, `GetOn`, `Delete` and `Health`; every call carries
  the workspace id.
- `backend/internal/platform/keyvault/local.go` is the shipping provider.
  It seals with AES-256-GCM from `crypto/aes` and `crypto/cipher`, using a
  base64-encoded 32-byte root key read from the environment variable
  `MARGINCE_KEYVAULT_ROOT_KEY`. A key that is set but malformed is a boot
  error, never a silent fallback.
- `backend/migrations/core/0062_keyvault.up.sql` creates the ciphertext
  table `vault_secret` (`ref`, `ciphertext`, `key_version`, `created_at`).
  The table carries no `workspace_id`: the workspace is inside the
  reference and inside the GCM additional authenticated data, so a
  reference presented under the wrong workspace is rejected before any read.
- The reference format is in `keyvault.go`: a fixed `mgv` scheme tag, the
  root-key version, the owning workspace, and a 128-bit random token. The
  version travels in the reference so a later key rotation can select the
  right key without changing the format.
- A cross-workspace reference answers `keyvault.ErrNotFound`, the same
  answer as a reference that was never stored.
- Domain rows carry the reference in a `credential_ref` column:
  `capture_connection`, `channel_connection`
  (`backend/migrations/core/0151_channel_connection.up.sql`), the finance
  mirror (`0202_finance_mirror.up.sql`) and provider integrations
  (`0219_provider_integrations.up.sql`).
- Consumers reach the vault through the reference:
  `backend/internal/modules/capture/registry.go`,
  `backend/internal/modules/integrations/connect.go`,
  `backend/internal/modules/overlay/connection.go`.
- `backend/internal/platform/keyvault/memory.go` is the test fake;
  `detached.go` covers the process role that has no vault configured.

## History

Adopted from the retired specification, decided 2026-06-26. Rewritten in
plain language 2026-08-19.

The source names a `connector_secret` table owned by the identity module.
Neither exists. The shipped table is `vault_secret` and it lives in
`internal/platform/keyvault`, which is technical plumbing rather than a
capability module. The source also describes a pluggable key-provider
interface with a cloud KMS implementation alongside the local one; the
substitution seam that shipped is narrower — a real local provider and a
memory fake — and no KMS provider is present. The envelope-wrapping design
the source calls for is not what the code does either: the local provider
seals each secret directly under the configured root key, with the key
version stamped into the reference so a keyring can be added later.
