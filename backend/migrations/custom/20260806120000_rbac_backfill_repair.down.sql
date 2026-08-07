-- Deliberately a no-op, for the same reason as core 0190's down: this migration
-- declares no grant of its own, so reverting it must leave the overlay_connection
-- and import_run grants standing. Each backfill's own down removes its object.
SELECT 1;
