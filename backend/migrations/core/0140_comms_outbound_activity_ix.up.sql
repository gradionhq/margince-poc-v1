-- The GDPR engines reach a delivery through the activity it reports on: Art. 17
-- erasure scrubs the deliveries of every activity its cascade redacted, and the
-- retention evaluator scrubs one per aged-out activity. Both filter on
-- activity_id, and 0136 ships no index covering it — so each call was a
-- sequential scan of the whole send log, up to the retention batch size per
-- policy per workspace per night.
--
-- This is not decoration in the sense 0136's "no due-index" note rejects: that
-- note is about a due-scan that does not exist, whereas these two sweeps do,
-- and a nightly evaluator slow enough to stall looks exactly like a satisfied
-- retention obligation.
--
-- workspace_id leads the index because every read is RLS-scoped to one tenant
-- and the composite FK it backs is (workspace_id, activity_id).
CREATE INDEX comms_outbound_workspace_activity_ix
  ON comms_outbound (workspace_id, activity_id);
