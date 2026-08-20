-- A deal that names a partner now says WHAT that partner did for it.
--
-- partner_org_id has carried the partner link since 0006, but a link alone
-- cannot answer the question the partner program is built on: did this partner
-- BRING us the deal, or did they help one we already had? Those two pay
-- differently everywhere partner programs exist, and without the distinction
-- stored, commission has to guess. partner_attribution stores the answer.
--
-- The two columns are one fact and travel together: a deal with a partner has
-- an attribution, and a deal with an attribution names the partner it belongs
-- to. deal_partner_attribution_pairing is what keeps a half-set pair from
-- existing, in either direction.
--
-- ADDITIVE. Existing partner deals are backfilled to 'sourced' — the historic
-- field was created for "deal registration / referral attribution", which is
-- the sourced motion, and every one of these rows predates the ability to say
-- anything else. Backfilling before the CHECK is added is what lets the CHECK
-- be added in the immediate (already-valid) form.
--
-- ON DELETE SET NULL on partner_org_id would break the pairing CHECK by
-- clearing one half of the pair, so the FK is re-declared to null BOTH columns
-- together. Postgres has no multi-column SET NULL on a single-column FK, so the
-- delete path is closed with a trigger on organization instead: the reference
-- and the attribution leave in the same statement.
SET LOCAL lock_timeout = '3s';

ALTER TABLE deal
  ADD COLUMN partner_attribution text NULL;

UPDATE deal
   SET partner_attribution = 'sourced'
 WHERE partner_org_id IS NOT NULL;

ALTER TABLE deal
  ADD CONSTRAINT deal_partner_attribution_check
  CHECK (partner_attribution IS NULL OR partner_attribution IN ('sourced','influenced'));

ALTER TABLE deal
  ADD CONSTRAINT deal_partner_attribution_pairing
  CHECK ((partner_org_id IS NULL) = (partner_attribution IS NULL));

-- The FK's SET NULL clears partner_org_id only, which the pairing CHECK then
-- rejects — deleting a partner organization would fail with a constraint error
-- instead of detaching its deals. Clearing both halves first, in the same
-- statement that deletes the organization, keeps the delete working and the
-- pair honest.
CREATE OR REPLACE FUNCTION deal_clear_partner_attribution_on_org_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE deal
     SET partner_org_id = NULL, partner_attribution = NULL
   WHERE partner_org_id = OLD.id;
  RETURN OLD;
END;
$$;

CREATE TRIGGER organization_delete_clears_deal_partner
  BEFORE DELETE ON organization
  FOR EACH ROW
  EXECUTE FUNCTION deal_clear_partner_attribution_on_org_delete();

CREATE INDEX idx_deal_partner_attribution
  ON deal (workspace_id, partner_attribution)
  WHERE partner_attribution IS NOT NULL AND archived_at IS NULL;

COMMENT ON COLUMN deal.partner_attribution IS
  'What the partner named by partner_org_id did for this deal: sourced (they brought it) or influenced (they helped one we already had). Commission accrues on sourced only.';
