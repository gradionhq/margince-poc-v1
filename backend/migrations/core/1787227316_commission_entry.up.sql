-- What a partner earned on a won deal, as a ledger rather than a calculation.
--
-- The partner extension has carried margin_tier (15/20/25%) since 0005 and
-- nothing has ever multiplied it by anything, so "what do we owe this partner"
-- had no answer in the product. commission_entry is that answer, and it is a
-- LEDGER on purpose: money that was owed and then was not is a second row, never
-- an edited first one. Recomputing an entry when a deal changes would silently
-- rewrite what we already told a partner they had earned.
--
-- Every rate-bearing value is SNAPSHOT at accrual. margin_tier is config a human
-- can change next quarter; the deal amount can be corrected after the close. An
-- entry says what the arrangement WAS the day the deal was won, which is the
-- only reading that stays true.
--
-- trigger_event_id is the replay guard, and it is a stored column rather than a
-- Redis key because platform/events dedupe is a 96-hour cache that marks AFTER
-- the effect runs — a crash in that window replays the accrual. The unique
-- constraint makes the second attempt fail instead of paying twice. A genuine
-- re-win carries a different event id, so it accrues again, which is correct:
-- the deal really was won twice.
SET LOCAL lock_timeout = '3s';

CREATE TABLE commission_entry (
  id uuid PRIMARY KEY,
  deal_id uuid NOT NULL REFERENCES deal(id) ON DELETE CASCADE,
  partner_org_id uuid NOT NULL REFERENCES organization(id) ON DELETE CASCADE,

  status text NOT NULL DEFAULT 'accrued'
    CHECK (status IN ('accrued','approved','paid','void')),

  -- The transition that produced this row. NULL for an entry a human created
  -- by hand, which has no event behind it; the unique index below is partial
  -- for that reason.
  trigger_event_id uuid NULL,

  -- What the arrangement was at accrual, frozen. rate_bps is basis points, so
  -- 15% is 1500 and no fractional-percent rounding enters the row.
  attribution_at_accrual text NOT NULL
    CHECK (attribution_at_accrual IN ('sourced','influenced')),
  margin_tier_at_accrual text NULL,
  rate_bps integer NOT NULL CHECK (rate_bps >= 0 AND rate_bps <= 10000),

  -- The money. basis_amount_minor is the deal amount the rate applied to;
  -- amount_minor is what that produced. Both in `currency`, with the won-time
  -- rate to base carried alongside so a base-currency roll-up reproduces
  -- without re-reading a deal whose frozen rate may since have been cleared.
  basis_amount_minor bigint NOT NULL CHECK (basis_amount_minor >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  fx_rate_to_base numeric(20,10) NULL,
  amount_minor bigint NOT NULL CHECK (amount_minor >= 0),

  -- A reversal points at the entry it undoes. The pair is what a clawback
  -- looks like here: the original goes 'void' and this row records why.
  reversal_of uuid NULL REFERENCES commission_entry(id) ON DELETE RESTRICT,
  void_reason text NULL,

  captured_by text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  -- A reversal is born void: it exists to cancel, never to be approved or paid.
  CONSTRAINT commission_reversal_is_void
    CHECK (reversal_of IS NULL OR status = 'void'),
  -- A voided entry says why. Silence about a reversal is the thing a partner
  -- dispute cannot be answered from.
  CONSTRAINT commission_void_has_reason
    CHECK (status <> 'void' OR void_reason IS NOT NULL)
);

-- One LIVE accrual per deal. A voided entry and its reversal are excluded, so a
-- deal reopened and won again accrues cleanly, while a replayed event cannot
-- produce a second live row.
CREATE UNIQUE INDEX uq_commission_live_per_deal
  ON commission_entry (deal_id)
  WHERE status <> 'void' AND reversal_of IS NULL;

-- The replay guard proper: one entry per triggering event, ever, including
-- across void. Partial because hand-created entries carry no event.
CREATE UNIQUE INDEX uq_commission_trigger_event
  ON commission_entry (trigger_event_id)
  WHERE trigger_event_id IS NOT NULL;

CREATE INDEX idx_commission_partner ON commission_entry (partner_org_id, status);
CREATE INDEX idx_commission_deal ON commission_entry (deal_id);

CREATE TRIGGER commission_entry_touch
  BEFORE UPDATE ON commission_entry
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

COMMENT ON TABLE commission_entry IS
  'What one partner earned on one won deal. Append-forward: a clawback is a reversal row plus a void, never an edit.';

-- The RBAC object, into the code-side seed AND into the role documents of
-- workspaces bootstrapped before this release. Without the second half a
-- workspace that already exists holds no grant and 403s on every commission
-- route — the defect 0182 had to repair for `partner`.
--
-- Posture: admin/ops/manager/management work the ledger; a rep READS it (they
-- need to see what a deal earned its partner) but never approves or pays;
-- read_only reads.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,commission}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE (is_system AND key IN ('admin','ops','manager','management')
  AND NOT permissions->'objects' ? 'commission');

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,commission}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE (is_system AND key IN ('rep','read_only')
  AND NOT permissions->'objects' ? 'commission');
