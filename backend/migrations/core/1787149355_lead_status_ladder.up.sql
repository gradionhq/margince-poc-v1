-- The lead status becomes an activity-driven ladder.
--
-- `working` said that somebody had the lead and nothing about whether we had
-- reached out or whether they had answered, which is the one thing a rep
-- scanning the list wants to know. The open states are now
-- new → contacted → engaged; the terminal pair (promoted, disqualified)
-- is unchanged. Every `working` lead becomes `contacted` — the nearest
-- truthful reading of "an SDR has it": somebody acted on it.
--
-- status_set_by records whether a human placed the lead on its step or the
-- system did from captured activity; it is what the page renders under the
-- stepper ("set automatically from your email of 12 Aug"). qualified_deal_id
-- is the deal a qualify call opened alongside the promotion, when one was
-- asked for; SET NULL on deal delete because the promotion stands without it.

SET LOCAL lock_timeout = '3s';

ALTER TABLE lead DROP CONSTRAINT lead_status_check;
UPDATE lead SET status = 'contacted' WHERE status = 'working';
ALTER TABLE lead ADD CONSTRAINT lead_status_check
  CHECK (status IN ('new','contacted','engaged','promoted','disqualified'));

ALTER TABLE lead
  ADD COLUMN status_set_by text NULL CHECK (status_set_by IN ('human','system')),
  ADD COLUMN qualified_deal_id uuid NULL REFERENCES deal(id) ON DELETE SET NULL;
CREATE INDEX idx_lead_qualified_deal ON lead (qualified_deal_id) WHERE qualified_deal_id IS NOT NULL;

-- The score index names the open set; it follows the ladder.
DROP INDEX IF EXISTS idx_lead_score;
CREATE INDEX idx_lead_score ON lead (score DESC)
  WHERE archived_at IS NULL AND status IN ('new','contacted','engaged');
