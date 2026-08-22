SET LOCAL lock_timeout = '5s';
DROP INDEX IF EXISTS uq_deal_room_preview_per_rep;
DROP INDEX IF EXISTS uq_deal_room_participant_email;
DELETE FROM deal_room_participant WHERE preview;
CREATE UNIQUE INDEX uq_deal_room_participant_email ON deal_room_participant (room_id, email)
    WHERE revoked_at IS NULL;
ALTER TABLE deal_room_participant DROP CONSTRAINT IF EXISTS deal_room_participant_preview_named;
ALTER TABLE deal_room_participant DROP CONSTRAINT IF EXISTS deal_room_participant_preview_reads_only;
ALTER TABLE deal_room_participant DROP COLUMN IF EXISTS preview;
