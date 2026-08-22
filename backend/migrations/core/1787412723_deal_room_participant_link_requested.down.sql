SET LOCAL lock_timeout = '5s';
ALTER TABLE deal_room_participant DROP COLUMN IF EXISTS link_requested_at;
