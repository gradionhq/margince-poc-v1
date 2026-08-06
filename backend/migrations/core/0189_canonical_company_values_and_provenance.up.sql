-- 0189: one authoritative value per company fact, with its evidence beside it
-- (PO-DDL-N-2, ADR-0085 / A130).
--
-- Three stores held a company's industry and nothing said which was true: the
-- organization column, the profile-field sidecar written by enrichment with a
-- snippet and a confidence, and an overlapping fact value. The same repeated
-- for legal name and address. Nobody decided that; it accumulated — the
-- columns came first, the sidecars were added to carry evidence, and because a
-- sidecar COULD hold a value it did.
--
-- The rule this migration serves: for any fact that is a column on
-- organization, the column IS the value and the sidecar holds a proposal plus
-- its evidence, joined at read. So the sidecars gain what a receipt actually
-- needs and the core row gains the two identity values it was missing.
--
-- Deliberately NOT here:
--   * no website column. The primary organization_domain row stays canonical
--     and the readable website is derived from it — a second store for a fact
--     the domain table already owns is the duplication this closes.
--   * no version/updated_at on organization_fact. 0099 already added both and
--     the trigger with them; re-adding fails on duplicate columns.
--   * person.is_role_mailbox (ADR-0089) lands with the coverage read that
--     needs it, not here.

-- LinkedIn is a first-class validated column, not a governed custom field: it
-- bears identity semantics — matching, dedupe, enrichment — that a custom
-- field cannot express, and the person side already treats it this way.
ALTER TABLE organization
  ADD COLUMN linkedin_url text NULL;

ALTER TABLE organization
  ADD CONSTRAINT organization_linkedin_url_shape
  CHECK (
    linkedin_url IS NULL
    OR linkedin_url ~ '^https://([a-z]{2,3}\.)?linkedin\.com/company/[^/?#]+/?$'
  );

-- Unique among LIVE rows only: an archived company must not hold a URL hostage
-- for the record that replaced it.
CREATE UNIQUE INDEX organization_linkedin_url_key
  ON organization (workspace_id, lower(linkedin_url))
  WHERE linkedin_url IS NOT NULL AND archived_at IS NULL;

-- Freshness and human verification on both provenance sidecars.
--
-- captured_at answers "when did we first record this", which is not the
-- question a receipt exists to answer. "Is this still true?" needs two more
-- facts: when the source was last actually read, and when a human last looked
-- at the claim and agreed. A value scraped six months ago and confirmed by a
-- person last week is a different claim from either half alone, and the page
-- must be able to say which.
--
-- verified_by is nullable and unconstrained by FK on purpose: the verifying
-- user may later be erased under Art. 17 while the fact that verification
-- happened must survive.
ALTER TABLE organization_profile_field
  ADD COLUMN retrieved_at timestamptz NULL,
  ADD COLUMN verified_at  timestamptz NULL,
  ADD COLUMN verified_by  uuid NULL;

ALTER TABLE organization_fact
  ADD COLUMN retrieved_at timestamptz NULL,
  ADD COLUMN verified_at  timestamptz NULL,
  ADD COLUMN verified_by  uuid NULL;

-- A verification is an event with an actor: recording one without the other
-- describes a confirmation nobody made.
ALTER TABLE organization_profile_field
  ADD CONSTRAINT org_profile_field_verified_pair
  CHECK ((verified_at IS NULL) = (verified_by IS NULL));

ALTER TABLE organization_fact
  ADD CONSTRAINT org_fact_verified_pair
  CHECK ((verified_at IS NULL) = (verified_by IS NULL));

COMMENT ON COLUMN organization.linkedin_url IS
  'Canonical LinkedIn company URL (PO-DDL-N-2). Unique among live rows per workspace.';
COMMENT ON COLUMN organization_profile_field.retrieved_at IS
  'When the source was last actually read (PO-DDL-N-2); distinct from captured_at.';
COMMENT ON COLUMN organization_profile_field.verified_at IS
  'When a human last confirmed this claim (PO-DDL-N-2). Paired with verified_by.';
COMMENT ON COLUMN organization_fact.retrieved_at IS
  'When the source was last actually read (PO-DDL-N-2); distinct from captured_at.';
COMMENT ON COLUMN organization_fact.verified_at IS
  'When a human last confirmed this claim (PO-DDL-N-2). Paired with verified_by.';
