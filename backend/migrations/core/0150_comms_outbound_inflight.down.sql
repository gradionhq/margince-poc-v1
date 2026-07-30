-- 0150 rollback: the column and its shape constraint go together. Dropping it
-- forgets which channel deliveries had a transmission outstanding, so a
-- re-applied 0150 starts with no marker set — the conservative direction only
-- because every such row is already parked with its reason recorded.
ALTER TABLE comms_outbound DROP CONSTRAINT comms_outbound_inflight_is_channel;

ALTER TABLE comms_outbound DROP COLUMN inflight_at;
