-- Human sessions + Agent Seat Passports (data-model §2.6–§2.7, ADR-0043).

CREATE TABLE session (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id         uuid NOT NULL REFERENCES app_user(id)  ON DELETE CASCADE,
  token_hash      text NOT NULL UNIQUE,     -- SHA-256(raw token); the raw token never touches the DB
  idle_expires_at timestamptz NOT NULL,     -- rolls forward on activity, capped by expires_at
  expires_at      timestamptz NOT NULL,     -- absolute timeout
  last_seen_at    timestamptz NOT NULL DEFAULT now(),
  user_agent      text NULL,
  ip              inet NULL,
  revoked_at      timestamptz NULL,         -- remote revoke; enforced at lookup
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_session_user ON session (workspace_id, user_id) WHERE revoked_at IS NULL;

CREATE TABLE passport (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  on_behalf_of  uuid NOT NULL REFERENCES app_user(id)  ON DELETE CASCADE,  -- the human whose RBAC bounds this passport
  granted_by    uuid NOT NULL REFERENCES app_user(id)  ON DELETE RESTRICT,
  label         text NULL,                  -- "Claude Desktop", "Cursor", …
  scopes        text[] NOT NULL,            -- rejected at bind if not ⊆ on_behalf_of's RBAC
  token_hash    text NOT NULL UNIQUE,
  expires_at    timestamptz NOT NULL,
  revoked_at    timestamptz NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_passport_obo ON passport (workspace_id, on_behalf_of) WHERE revoked_at IS NULL;
