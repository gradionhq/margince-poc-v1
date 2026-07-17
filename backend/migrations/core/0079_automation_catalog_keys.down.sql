-- 0079 down: reverse the re-key. Reversibility here is the standard
-- immediate-rollback guarantee (up then down restores the pre-migration
-- state) — NOT a live rollback safe at an arbitrary later point: a row
-- created under 'assign_lead_owner' by a human author AFTER the up
-- migration ran (the API's own POST /automations, now a legitimate,
-- separate catalog entry) would be mis-labeled back to 'route_lead' by
-- this statement too, indistinguishably from a re-keyed row, because
-- the key alone no longer carries when it was created. Every other
-- migration in this namespace carries the identical assumption for a
-- data backfill (e.g. 0076's down); this one states it explicitly
-- because the two keys stay live, authorable catalog entries afterward,
-- unlike a column drop.
UPDATE automation
   SET key  = 'route_lead',
       name = CASE WHEN name = 'Assign new leads an owner' THEN 'Route new leads' ELSE name END
 WHERE key = 'assign_lead_owner';
