-- 0239: the webhook tables drop the tenant column (ADR-0091 §8 phase D).
--
-- They were held back from every earlier slice because the retry sweep fanned
-- out one job per live tenant, and the suite proving that fan-out asserted
-- isolation between two of them. The sweep is one pass now (ADR-0103 §1), so
-- the columns have nothing left to hold.

DROP INDEX idx_webhook_delivery_by_subscription;
CREATE INDEX idx_webhook_delivery_by_subscription ON webhook_delivery (subscription_id, created_at DESC);

DROP INDEX idx_webhook_subscription_live;
CREATE INDEX idx_webhook_subscription_live ON webhook_subscription (state) WHERE archived_at IS NULL;

ALTER TABLE webhook_subscription DROP CONSTRAINT webhook_subscription_ws_id_key;

ALTER TABLE webhook_subscription DROP COLUMN workspace_id;
ALTER TABLE webhook_delivery DROP COLUMN workspace_id;
