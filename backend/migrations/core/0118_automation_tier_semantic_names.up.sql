-- 0118: rename the automation autonomy tier values from the color codes
-- (green/yellow) to the semantic names (auto_execute/confirmation_required),
-- matching the crm.yaml contract enum and the mcp.RiskTier constants. The
-- `tier` column is the sole persisted spelling — automation runs read it back
-- verbatim (automations_runs.go), so the wire value and the stored value are
-- the same string and must move together. Drop the old CHECK first: the
-- rewrite below writes values the green/yellow CHECK would reject.
--
-- The default also moves from the old 'green' to the fail-safe
-- 'confirmation_required': a row inserted without an explicit tier is
-- labelled as needing human confirmation rather than auto-execution. This is
-- defense in depth on the metadata only — production always stamps tier
-- explicitly from the catalog (automations.go), and the runtime stage-vs-execute
-- decision is made per action at fire time, never from this column.
ALTER TABLE automation DROP CONSTRAINT IF EXISTS automation_tier_check;

ALTER TABLE automation ALTER COLUMN tier SET DEFAULT 'confirmation_required';

UPDATE automation SET tier = 'auto_execute'         WHERE tier = 'green';
UPDATE automation SET tier = 'confirmation_required' WHERE tier = 'yellow';

ALTER TABLE automation
  ADD CONSTRAINT automation_tier_check CHECK (tier IN ('auto_execute', 'confirmation_required'));
