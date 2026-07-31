-- 0148: federated sign-in storage (A107/ADR-0061 §6, §11) — the permanent
-- provider binding and the per-attempt flow state.
--
-- external_identity keys a human to an OIDC provider by (issuer, subject),
-- never by email: a provider may change an address, whereas the subject is
-- designed to be stable within an issuer. email_at_link_time records what
-- the claim said when the binding was written, as evidence — it is never a
-- lookup key. One binding per user per issuer, one user per subject.
--
-- oidc_login_state is the in-flight authorization attempt: it exists for a
-- few minutes between the redirect out and the provider's return, and it is
-- the only place the nonce and the PKCE verifier live. It carries no user_id
-- — the attempt is pre-authentication, and which human it becomes is decided
-- from the validated ID token, not from anything the browser asserted.

CREATE TABLE external_identity (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id          uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id               uuid NOT NULL,
  issuer                text NOT NULL,              -- the provider's `iss`, exactly as issued
  subject               text NOT NULL,              -- the provider's `sub`; the stable identifier
  email_at_link_time    text NOT NULL,              -- evidence of the one-time claim match, never a key
  last_authenticated_at timestamptz NULL,           -- stamped on each successful federated sign-in
  created_at            timestamptz NOT NULL DEFAULT now(),

  -- Same-workspace guarantee (C4, the 0019 composite-FK pattern): the
  -- binding's human must live in this row's workspace.
  CONSTRAINT external_identity_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
-- One human per provider subject: a second local user can never claim an
-- identity the provider already bound.
CREATE UNIQUE INDEX idx_external_identity_subject ON external_identity (issuer, subject);
-- One binding per human per issuer: replacing a provider account is an
-- explicit audited operation, not a second row that silently shadows the first.
CREATE UNIQUE INDEX idx_external_identity_user_issuer ON external_identity (user_id, issuer);

ALTER TABLE external_identity ENABLE ROW LEVEL SECURITY;
ALTER TABLE external_identity FORCE ROW LEVEL SECURITY;
CREATE POLICY external_identity_tenant_isolation ON external_identity
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

CREATE TABLE oidc_login_state (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  provider      text NOT NULL,              -- the capability key the attempt was started for
  state_hash    text NOT NULL,              -- SHA-256(state); the raw value lives only in the browser cookie and the provider round-trip
  nonce         text NOT NULL,              -- compared against the ID token's nonce claim
  code_verifier text NOT NULL,              -- PKCE S256 verifier; useless without the matching code, and consumed with the row
  expires_at    timestamptz NOT NULL,       -- minutes, not hours: an authorization round-trip is short
  consumed_at   timestamptz NULL,           -- single-use: stamped when the callback claims it
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_oidc_login_state_hash ON oidc_login_state (state_hash);
-- The sweep index: expired unconsumed attempts are deleted, not kept as
-- forensic debris — the row holds a live PKCE verifier.
CREATE INDEX idx_oidc_login_state_expiry ON oidc_login_state (expires_at) WHERE consumed_at IS NULL;

ALTER TABLE oidc_login_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE oidc_login_state FORCE ROW LEVEL SECURITY;
CREATE POLICY oidc_login_state_tenant_isolation ON oidc_login_state
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
