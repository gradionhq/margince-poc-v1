-- 0262: `contract` — the agreements an account has signed (ADR-0109/A160).
--
-- The record is not the paper. The signed PDF is an attachment like any other
-- file; this table is what can be counted, filtered, summed and watched for a
-- renewal date. Neither substitutes for the other, which is why a contract has
-- both a row here and a document over there.
--
-- NO `workspace_id`, AND THAT IS THE POINT. ADR-0091/A136 retires the workspace
-- tenant boundary, and this is the first table authored after that decision: a
-- new capability does not pay a tax that is being removed table by table. It
-- follows that this table carries no RLS policy and appears in no tenant-
-- isolation proof, because there is no tenant column for either to bind to.
--
-- NO `owner_id`, and that is also deliberate (ADR-0109 §8). A contract belongs
-- to a company, not a person. Who may see it is DERIVED — whoever may see the
-- linked deal, falling back to the organization — so that reassigning a deal
-- moves its contracts in the same query. An owner copied at creation would
-- diverge the moment the deal changed hands and leave the agreement visible to
-- the representative who no longer works the account.
CREATE TABLE contract (
  id                        uuid PRIMARY KEY DEFAULT uuidv7(),

  -- The counterparty. An organization holds many contracts: a master agreement
  -- and its later addenda are separate agreements with their own end dates and
  -- their own renewal dates, and that one-to-many is why this is a table rather
  -- than a handful of columns on `organization`.
  organization_id           uuid NOT NULL REFERENCES organization(id) ON DELETE RESTRICT,

  -- Nullable on purpose. An imported agreement, or one signed before the deal
  -- existed, has no pipeline history to point at, and refusing those rows would
  -- make the table describe only the deals we happened to run through Margince.
  deal_id                   uuid NULL REFERENCES deal(id) ON DELETE SET NULL,
  project_id                uuid NULL REFERENCES project(id) ON DELETE SET NULL,

  -- Free text, and unconstrained on purpose: an imported agreement carries
  -- whatever number the counterparty's own system gave it, and two systems
  -- reusing a number is their business rather than a reason to refuse the row.
  contract_number           text NULL,
  title                     text NOT NULL,

  -- Total contract value, or twelve months of billing when `value_basis` says
  -- the agreement is open-ended (CONTRACT-PARAM-2).
  value_minor               bigint NULL,
  currency                  char(3) NULL,
  value_basis               text NOT NULL DEFAULT 'total'
                            CHECK (value_basis IN ('total', 'annualized_12m')),

  -- Frozen at activation, never at read time. A contract signed at last year's
  -- rate is worth what it was worth then, and re-converting it at today's rate
  -- would silently restate history every time somebody opened the page.
  fx_rate_to_base           numeric(20,10) NULL,
  fx_rate_date              date NULL,

  starts_on                 date NULL,
  -- NULL means open-ended, which is a real and common shape, not missing data.
  ends_on                   date NULL,
  renewal_on                date NULL,
  auto_renew                boolean NOT NULL DEFAULT false,
  notice_period_days        integer NULL CHECK (notice_period_days IS NULL OR notice_period_days >= 0),

  -- Asserted by a human or an approved proposal. No date moves it, here or in
  -- code: the data that would drive an inference is exactly the data most often
  -- stale, and a term that lapsed last month is very often an agreement
  -- everybody knows was extended by email.
  status                    text NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft', 'active', 'expired', 'cancelled', 'superseded')),

  -- When a human says it was signed. Never defaulted from a deal's close
  -- timestamp, which records when somebody moved a stage and is not evidence
  -- that anything was signed. In-product e-signature stays removed (A94).
  signed_on                 date NULL,

  cancellation_notice_on    date NULL,
  cancellation_effective_on date NULL,

  -- The renewal chain. A renewal creates a successor and points the predecessor
  -- at it rather than mutating the predecessor, so an agreement that has run for
  -- six years reads as a chain instead of a row somebody overwrote five times.
  superseded_by_id          uuid NULL REFERENCES contract(id) ON DELETE SET NULL,

  source                    text NOT NULL DEFAULT 'manual',
  captured_by               uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  version                   bigint NOT NULL DEFAULT 1,
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now(),
  -- Archive is the delete. Deleting a contract would silently change whether an
  -- account ever counted as a customer and destroy the evidence behind a deal
  -- that was marked won.
  archived_at               timestamptz NULL,

  -- Money travels as a pair or not at all. Half a pair cannot be converted, and
  -- the failure would surface far from the row that caused it.
  CONSTRAINT contract_value_pair CHECK (
    (value_minor IS NULL) = (currency IS NULL)),

  -- A frozen rate is meaningless without the date it was frozen on.
  CONSTRAINT contract_fx_pair CHECK (
    (fx_rate_to_base IS NULL) = (fx_rate_date IS NULL)),

  CONSTRAINT contract_term_order CHECK (
    starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on),

  -- A cancellation cannot extend a term that already ran out. Refusing the
  -- contradiction here is what lets the derived reading take the earlier of the
  -- two dates without ever meeting a row where that choice is surprising.
  CONSTRAINT contract_cancellation_within_term CHECK (
    cancellation_effective_on IS NULL OR ends_on IS NULL
    OR cancellation_effective_on <= ends_on),

  CONSTRAINT contract_cancellation_order CHECK (
    cancellation_notice_on IS NULL OR cancellation_effective_on IS NULL
    OR cancellation_effective_on >= cancellation_notice_on),

  -- A contract cannot supersede itself. Longer cycles are refused by the write
  -- path, which can walk the chain; a self-reference is cheap to catch here.
  CONSTRAINT contract_supersedes_not_self CHECK (
    superseded_by_id IS NULL OR superseded_by_id <> id),

  -- The pointer and the status say the same thing. A superseded contract names
  -- its successor, and a contract naming a successor reads as superseded.
  CONSTRAINT contract_superseded_agrees CHECK (
    (status = 'superseded') = (superseded_by_id IS NOT NULL))
);

CREATE TRIGGER contract_set_updated_at
  BEFORE UPDATE ON contract
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- The account read: every agreement on one company, newest first. The column
-- order mirrors the list query's ORDER BY and its keyset cursor exactly —
-- (created_at, id) — so the page is served from the index rather than sorted.
-- Partial because an archived contract is never in the list this index serves.
CREATE INDEX contract_account_ix
  ON contract (organization_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

-- The deal read, and the won-evidence check that asks whether this deal has an
-- agreement behind it.
CREATE INDEX contract_deal_ix
  ON contract (deal_id)
  WHERE deal_id IS NOT NULL AND archived_at IS NULL;

-- The renewal sweep's due-scan. Partial on both columns because a contract with
-- no renewal date is never due, and an archived one is never swept.
CREATE INDEX contract_renewal_due_ix
  ON contract (renewal_on)
  WHERE renewal_on IS NOT NULL AND archived_at IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON contract TO margince_app;
