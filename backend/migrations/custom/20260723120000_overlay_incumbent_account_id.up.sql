-- 20260723120000_overlay_incumbent_account_id (OVA-DDL-3): record the
-- incumbent's own account/portal id on the connection so an inbound webhook's
-- portalId binds to THIS workspace (the webhook-as-signal tenant binding). The
-- signature proves the sender (one app secret across all portals); the portal
-- binding proves the tenant. Nullable because connections made before this
-- column exist until they reconnect (or a backfill fills it); a webhook whose
-- portal matches no active connection carrying it is rejected fail-closed, so a
-- null here simply means "not bindable yet", never a cross-tenant leak.
ALTER TABLE incumbent_connection ADD COLUMN IF NOT EXISTS incumbent_account_id text;

-- The receiver resolves the workspace from an inbound portalId; a partial index
-- over active connections that carry one keeps that per-workspace lookup a
-- point read. NOT unique: enforcing global portal uniqueness would let one
-- workspace's connect quietly fail because another workspace already recorded
-- the same portal — the fail-closed binding handles a shared/duplicate portal
-- by matching the active connection, and a genuine collision is an operator
-- concern surfaced elsewhere, not a connect-blocking constraint here.
CREATE INDEX IF NOT EXISTS idx_incumbent_connection_account
  ON incumbent_connection (incumbent, incumbent_account_id)
  WHERE status = 'active' AND incumbent_account_id IS NOT NULL;
