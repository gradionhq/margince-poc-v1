-- Reversing drops three published facts and nothing else: every provider row
-- survives, and the boot reconcile rewrites all three on the next boot from the
-- composed connector set, which is where they come from in the first place.
-- Nothing here is authored by a human, so nothing here is lost.

ALTER TABLE channel_provider DROP COLUMN supplies_transport;
ALTER TABLE channel_provider DROP COLUMN credential_model;
ALTER TABLE channel_provider DROP COLUMN label;
