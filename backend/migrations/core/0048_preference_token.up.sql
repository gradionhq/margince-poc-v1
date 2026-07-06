-- 0048: preference-center tokens (B-E11.32). One unguessable token per
-- recipient: it resolves to (workspace, person) BEFORE any session or
-- workspace header exists — the no-login preference center and the RFC 8058
-- one-click unsubscribe both address /v1/public/preferences/{token}. So,
-- like booking_page (0036) and the workspace table itself (data-model
-- §1.2), this table is DELIBERATELY not under RLS: it IS the resolver the
-- tenant GUC is derived from. The token is a capability identifier carried
-- in the List-Unsubscribe URL, not a stored credential — high-entropy and
-- plaintext because it IS the URL; it carries no CRM record data beyond the
-- person link and its revocation.
CREATE TABLE preference_token (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id     uuid NOT NULL,
  token         text NOT NULL UNIQUE,
  created_at    timestamptz NOT NULL DEFAULT now(),
  revoked_at    timestamptz NULL,
  -- Composite FK so a token can never point at a person in another
  -- workspace (schema_fitness composite-FK invariant, C4).
  CONSTRAINT preference_token_person_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE
);
-- At most one live token per recipient: the send path mints lazily on the
-- first message and reuses it on every later one.
CREATE UNIQUE INDEX uq_preference_token_person
  ON preference_token (workspace_id, person_id) WHERE revoked_at IS NULL;
