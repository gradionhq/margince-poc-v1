-- Reverse of 0189: drop the settings table. The values it holds are
-- reconstructible from margince.yaml's bootstrap section plus the registered
-- defaults, so no data-preservation step is needed here — a down migration
-- that tried to write them back onto workspace columns would depend on those
-- columns still existing, which is a coupling this file should not have.
DROP TABLE IF EXISTS setting;
