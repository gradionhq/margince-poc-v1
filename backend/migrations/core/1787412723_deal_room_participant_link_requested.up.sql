-- When a buyer last asked the public page for a new link. Stamped whether or
-- not a link is then mailed: the seat that still holds a live credential gets
-- nothing, and the seller is the one who can hand that buyer a link by hand.
SET LOCAL lock_timeout = '5s';
ALTER TABLE deal_room_participant ADD COLUMN link_requested_at timestamptz;
