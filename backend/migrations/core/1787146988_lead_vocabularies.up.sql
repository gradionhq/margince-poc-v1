-- Lead vocabularies: where a lead came from, and why it was closed, become
-- installation-owned lists instead of literals spread over the scorer, the
-- create form and the filter chip.
--
-- lead_source carries the intent the scorer reads (high adds the
-- high-intent points, low subtracts the penalty, neutral does neither), so
-- an administrator renaming or adding a source also decides what it is
-- worth. The six seeded keys are the values the scorer and the UI carried as
-- literals before this table existed; `system` marks them so they can be
-- renamed and deactivated but never deleted, because the scorer's parity test
-- and the capture paths still spell them.
--
-- lead_disqualify_reason is the closing vocabulary. The seed is the stock
-- set sales teams already use. A disqualified lead points at its reason and
-- may carry a note; both stay on the row after the lead is archived, which
-- is where the "why did we drop this" question is later answered from.
--
-- Both tables are installation-scoped: one installation serves one
-- organization, so there is no tenant column to carry.

SET LOCAL lock_timeout = '3s';

CREATE TABLE lead_source (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  key         text NOT NULL,
  label       text NOT NULL,
  intent      text NOT NULL DEFAULT 'neutral' CHECK (intent IN ('high','neutral','low')),
  sort_order  integer NOT NULL DEFAULT 0,
  active      boolean NOT NULL DEFAULT true,
  system      boolean NOT NULL DEFAULT false,
  version     bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT lead_source_key_shape CHECK (key = lower(key) AND length(btrim(key)) > 0),
  CONSTRAINT lead_source_label_present CHECK (length(btrim(label)) > 0)
);
CREATE UNIQUE INDEX uq_lead_source_key ON lead_source (key);
CREATE TRIGGER trg_lead_source_updated BEFORE UPDATE ON lead_source
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE lead_disqualify_reason (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  label       text NOT NULL,
  sort_order  integer NOT NULL DEFAULT 0,
  active      boolean NOT NULL DEFAULT true,
  system      boolean NOT NULL DEFAULT false,
  version     bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT lead_disqualify_reason_label_present CHECK (length(btrim(label)) > 0)
);
CREATE TRIGGER trg_lead_disqualify_reason_updated BEFORE UPDATE ON lead_disqualify_reason
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- The reason outlives nothing: a reason row in use cannot be deleted (the
-- store refuses it with 409 and offers deactivation instead), and the FK
-- RESTRICT is the database's copy of that rule.
ALTER TABLE lead
  ADD COLUMN disqualify_reason_id uuid NULL REFERENCES lead_disqualify_reason(id) ON DELETE RESTRICT,
  ADD COLUMN disqualify_note text NULL;
CREATE INDEX idx_lead_disqualify_reason ON lead (disqualify_reason_id) WHERE disqualify_reason_id IS NOT NULL;

INSERT INTO lead_source (key, label, intent, sort_order, system) VALUES
  ('manual',   'Created manually', 'neutral', 10, true),
  ('inbound',  'Inbound',          'high',    20, true),
  ('webform',  'Web form',         'high',    30, true),
  ('referral', 'Referral',         'high',    40, true),
  ('import',   'Import',           'low',     50, true),
  ('crawl',    'Crawl',            'low',     60, true);

INSERT INTO lead_disqualify_reason (label, sort_order, system) VALUES
  ('Not a good fit',     10, true),
  ('Bad timing',         20, true),
  ('No budget',          30, true),
  ('No decision power',  40, true),
  ('Chose a competitor', 50, true),
  ('No interest',        60, true),
  ('Not reachable',      70, true),
  ('Duplicate or spam',  80, true);
