-- Reverse of 0190: drop the settings table. The values it holds are
-- reconstructible from margince.yaml's bootstrap section plus the registered
-- defaults, so no data-preservation step is needed here — a down migration
-- that tried to write them back onto workspace columns would depend on those
-- columns still existing, which is a coupling this file should not have.
-- Carry the CURRENT value back onto the column first. After 0190 the setting
-- row is the only copy a writer updates, so dropping the table without this
-- would roll back to whatever the column held before the move — silently
-- discarding every change made in between.
UPDATE workspace w SET capture_auto_enrich = (s.value)::boolean
  FROM setting s
 WHERE s.key = 'capture.auto_enrich'
   AND w.archived_at IS NULL;

DROP TABLE IF EXISTS setting;
