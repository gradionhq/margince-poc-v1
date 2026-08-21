-- Drops in dependency order: everything pointing at deal_room before the room
-- itself. The FKs cascade, but naming the order keeps this readable rather than
-- relying on the reader to work out which drop takes which table with it.
--
-- The lock_timeout is here because these tables carry foreign keys OUT to deal
-- and app_user: dropping one takes a lock on those too, and an unbounded wait
-- would queue behind any open transaction and stall every write to the deal
-- spine for as long as this is willing to wait. Three seconds, then fail and
-- let the operator retry, rather than take the estate down to undo a feature.
SET LOCAL lock_timeout = '3s';

DROP TABLE IF EXISTS deal_room_task;
DROP TABLE IF EXISTS deal_room_session;
DROP TABLE IF EXISTS deal_room_invitation;
DROP TABLE IF EXISTS deal_room_participant;
DROP TABLE IF EXISTS deal_room_release;
DROP TABLE IF EXISTS deal_room;

DROP FUNCTION IF EXISTS deal_room_release_is_frozen();

-- The grant goes with the object it names: leaving it behind would have every
-- role document claim authority over a table that no longer exists.
UPDATE role SET permissions = permissions #- '{objects,deal_room}'
    WHERE permissions ? 'objects' AND (permissions -> 'objects') ? 'deal_room';
