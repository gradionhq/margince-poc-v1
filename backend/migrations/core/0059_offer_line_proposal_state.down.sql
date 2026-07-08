-- Staged (never-accepted) lines cannot survive a world without the flag:
-- they were invisible to totals and must not silently start counting.
DELETE FROM offer_line_item WHERE proposal_state = 'staged';
ALTER TABLE offer_line_item DROP COLUMN proposal_state;
