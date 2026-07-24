-- Mirror of the up: restore the green/yellow color codes and their CHECK.
-- Drop the semantic-name CHECK first so the reverse rewrite is accepted.
ALTER TABLE automation DROP CONSTRAINT IF EXISTS automation_tier_check;

ALTER TABLE automation ALTER COLUMN tier SET DEFAULT 'green';

UPDATE automation SET tier = 'green'  WHERE tier = 'auto_execute';
UPDATE automation SET tier = 'yellow' WHERE tier = 'confirmation_required';

ALTER TABLE automation
  ADD CONSTRAINT automation_tier_check CHECK (tier IN ('green', 'yellow'));
