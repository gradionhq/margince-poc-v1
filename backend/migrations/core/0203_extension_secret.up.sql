-- 0201: an extension's own secret namespace (ADR-0069).
--
-- extension_secret maps an extension's own key names onto keyvault refs.
-- keyvault mints opaque refs and scopes them to a workspace in the ref's
-- AAD; it has no key/value namespace and no user scope, so the namespace
-- and the per-user scope live here.
--
-- extension_name is the namespace wall: the store closes over the invoking
-- unit's name and every statement carries it, so two units addressing the
-- same bare key name never resolve each other's secret. The name is NOT a
-- foreign key — the composed extension set lives in source (ADR-0069 §5),
-- not in a table — so a removed unit leaves its rows behind rather than
-- having them silently cascade away with a secret an operator may still
-- need to revoke at the provider.
--
-- vault_ref is a keyvault handle, never secret material: the ciphertext
-- lives in vault_secret, sealed under a key this table has no access to.
CREATE TABLE extension_secret (
    id             uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    extension_name text        NOT NULL,
    workspace_id   uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    user_id        uuid            NULL,
    key            text        NOT NULL,
    vault_ref      text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    -- Composite, not a bare user_id REFERENCES app_user(id): two independent
    -- FKs would each be satisfiable on their own and would let a row attach a
    -- user from another tenant to this workspace's secret. Under the default
    -- MATCH SIMPLE a row with user_id IS NULL satisfies this constraint
    -- vacuously, which is exactly what the workspace scope needs.
    FOREIGN KEY (workspace_id, user_id) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

-- user_id CANNOT be part of the primary key: PK columns are implicitly
-- NOT NULL in Postgres, so a workspace-scoped row (user_id IS NULL) could
-- never exist. Two partial unique indexes carry the two scopes instead.
CREATE UNIQUE INDEX extension_secret_workspace_key
    ON extension_secret (extension_name, workspace_id, key)
    WHERE user_id IS NULL;
CREATE UNIQUE INDEX extension_secret_user_key
    ON extension_secret (extension_name, workspace_id, user_id, key)
    WHERE user_id IS NOT NULL;
ALTER TABLE extension_secret ENABLE ROW LEVEL SECURITY;
ALTER TABLE extension_secret FORCE ROW LEVEL SECURITY;
CREATE POLICY extension_secret_tenant_isolation ON extension_secret
    USING       (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK  (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON extension_secret TO margince_app;

COMMENT ON COLUMN extension_secret.vault_ref IS
  'An opaque keyvault handle (ADR-0069): safe to log, never the secret itself.';
