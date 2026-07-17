-- 0088: opt-in AI payload capture (Layer 3). The post-SecretStripper
-- request (system + messages) and response text for one ai_call, captured
-- only when the operator turns it on (margince.yaml ai.capture_payloads).
-- Special-category-adjacent content (GDPR Art. 9 possibility): a SEPARATE
-- table from ai_call on purpose — it ages out via the retention engine
-- (365d erase) and is scrubbed by the Art. 17 cascade, while the ai_call
-- metadata row survives. Never in audit_log. FK cascade so a purged call
-- takes its payload with it.
CREATE TABLE ai_call_payload (
  id               uuid        NOT NULL DEFAULT uuidv7(),
  workspace_id     uuid        NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  ai_call_id       uuid        NOT NULL REFERENCES ai_call(id) ON DELETE CASCADE,
  request_payload  jsonb       NOT NULL,
  response_payload jsonb       NOT NULL,
  occurred_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id)
);

CREATE INDEX ai_call_payload_ws_time ON ai_call_payload (workspace_id, occurred_at);
CREATE INDEX ai_call_payload_call    ON ai_call_payload (ai_call_id);

ALTER TABLE ai_call_payload ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_call_payload FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_call_payload_tenant_isolation ON ai_call_payload
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
