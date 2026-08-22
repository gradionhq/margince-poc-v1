-- Reverses 1787413000. Views saved over projects are removed first; the
-- narrower constraint cannot be re-added over rows it refuses.
SET LOCAL lock_timeout = '5s';
DELETE FROM saved_view WHERE resource = 'projects';
ALTER TABLE saved_view DROP CONSTRAINT IF EXISTS saved_view_resource_check;
ALTER TABLE saved_view ADD CONSTRAINT saved_view_resource_check
    CHECK (resource IN ('people', 'organizations', 'deals', 'activities', 'leads', 'partners'));
