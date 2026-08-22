-- "View as buyer": a seller previews the room through the real public edge,
-- as a participant the buyers never see. One per rep per room, read-only by
-- CHECK, outside the per-address uniqueness so the rep's own address never
-- collides with a buyer's seat and never answers a public link request.
SET LOCAL lock_timeout = '5s';
ALTER TABLE deal_room_participant ADD COLUMN preview boolean NOT NULL DEFAULT false;
ALTER TABLE deal_room_participant ADD CONSTRAINT deal_room_participant_preview_reads_only
    CHECK (NOT preview OR capability = 'view');
-- A preview seat is somebody's: its uniqueness is per (room, seller), and a
-- NULL seller would be distinct from every other NULL.
ALTER TABLE deal_room_participant ADD CONSTRAINT deal_room_participant_preview_named
    CHECK (NOT preview OR invited_by IS NOT NULL);
DROP INDEX uq_deal_room_participant_email;
CREATE UNIQUE INDEX uq_deal_room_participant_email ON deal_room_participant (room_id, email)
    WHERE revoked_at IS NULL AND NOT preview;
CREATE UNIQUE INDEX uq_deal_room_preview_per_rep ON deal_room_participant (room_id, invited_by)
    WHERE revoked_at IS NULL AND preview;
