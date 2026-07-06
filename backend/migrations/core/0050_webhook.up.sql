-- 0050: outbound webhooks (B-E10.13a, data-model §12.5 "Outbound
-- webhooks" / S-E10.6, A51) — the governed first-party integration
-- surface: a tenant registers a target URL + an event-type subset from
-- the published catalog (events.md §5) + a per-subscription signing
-- secret, and the delivery worker (B-E10.13b) fans matching domain
-- events to it as signed HTTP POSTs, retried with backoff and parked in
-- a dead-letter store (B-E10.13c).
--
-- The signing secret is NEVER stored plaintext (data-model note): the
-- column holds an AES-256-GCM ciphertext of a server-generated secret,
-- sealed with the deployment key — the PoC stand-in for the vault ref
-- the spec names. The plaintext is returned exactly once, at create /
-- rotate, and used to compute the X-Margince-Signature HMAC at delivery.
CREATE TABLE webhook_subscription (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  owner_id           uuid NOT NULL,
  -- HTTPS-only; http:// is rejected at create (matches the design's
  -- validation and the anti-SSRF posture — a cleartext callback is never
  -- a safe fan-out target).
  target_url         text NOT NULL CHECK (target_url ~ '^https://'),
  -- A subset of the published event catalog (events.md §5). The catalog
  -- lives in code, so the DB CHECK only enforces non-empty; the store
  -- validates membership against events.Types().
  event_types        text[] NOT NULL CHECK (cardinality(event_types) > 0),
  -- Sealed signing secret (AES-256-GCM ciphertext, base64). Never the
  -- plaintext; never logged; never returned after create/rotate.
  signing_secret_ref text NOT NULL,
  state              text NOT NULL DEFAULT 'active' CHECK (state IN ('active','paused')),
  version            bigint NOT NULL DEFAULT 1,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  archived_at        timestamptz NULL,
  -- The composite unique key the webhook_delivery FK matches against, so
  -- a delivery can never point at a subscription in another workspace.
  CONSTRAINT webhook_subscription_ws_id_key UNIQUE (workspace_id, id),
  -- Composite tenant FK so a subscription can never be owned by a user in
  -- another workspace (schema_fitness composite-FK invariant).
  CONSTRAINT webhook_subscription_owner_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
-- The fan-out lookup: live subscriptions in a workspace. event_types is
-- filtered in the same scan (small per-tenant cardinality).
CREATE INDEX idx_webhook_subscription_live
  ON webhook_subscription (workspace_id, state) WHERE archived_at IS NULL;

CREATE TRIGGER trg_webhook_subscription_updated BEFORE UPDATE ON webhook_subscription
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE webhook_subscription ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscription FORCE ROW LEVEL SECURITY;
CREATE POLICY webhook_subscription_tenant_isolation ON webhook_subscription
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Per-attempt delivery log + state machine (B-E10.13c). At-least-once
-- with idempotency: exactly one delivery row per (subscription, event),
-- so a redelivered bus event never double-POSTs. The signed body is kept
-- verbatim (payload) so a parked delivery can be replayed after the bus
-- stream has trimmed the source event (events.md §4.4).
CREATE TABLE webhook_delivery (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  subscription_id  uuid NOT NULL,
  event_id         uuid NOT NULL,
  event_type       text NOT NULL,
  payload          jsonb NOT NULL,
  status           text NOT NULL CHECK (status IN ('pending','delivered','retrying','dead_lettered')),
  attempts         int NOT NULL DEFAULT 0,
  last_status_code int NULL,
  last_error       text NULL,
  next_retry_at    timestamptz NULL,
  delivered_at     timestamptz NULL,
  dead_lettered_at timestamptz NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  -- Idempotency key: one delivery per subscription per event.
  CONSTRAINT webhook_delivery_dedupe_key UNIQUE (workspace_id, subscription_id, event_id),
  CONSTRAINT webhook_delivery_subscription_fkey FOREIGN KEY (workspace_id, subscription_id)
    REFERENCES webhook_subscription (workspace_id, id) ON DELETE CASCADE
);
-- The retry sweeper's due-work scan: parked-for-retry rows whose backoff
-- has elapsed. Partial so it stays small (delivered rows drop out).
CREATE INDEX idx_webhook_delivery_due
  ON webhook_delivery (next_retry_at) WHERE status = 'retrying';
-- The subscription's delivery history / dead-letter inspection surface.
CREATE INDEX idx_webhook_delivery_by_subscription
  ON webhook_delivery (workspace_id, subscription_id, created_at DESC);

CREATE TRIGGER trg_webhook_delivery_updated BEFORE UPDATE ON webhook_delivery
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE webhook_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_delivery FORCE ROW LEVEL SECURITY;
CREATE POLICY webhook_delivery_tenant_isolation ON webhook_delivery
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
