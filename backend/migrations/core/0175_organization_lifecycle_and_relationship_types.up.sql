-- 0175: where an account STANDS and what it IS to us become two fields
-- (PO-DDL-4 / PO-DDL-4b, ADR-0079 / A124).
--
-- organization.classification held one value and answered two questions. Two
-- of its nine values are stages of a sales motion, six are kinds of company,
-- one is a shrug — so setting 'partner' erased whether they buy from us, and a
-- company that is an agency AND a client AND a co-sell partner got one slot.
-- There was no churn state at all: an account that ended its contract could
-- only be relabelled by overwriting 'customer', destroying the fact it ever
-- was one.
--
-- The column also never had a writer. ADR-0032 said enrichment would set it;
-- that writer was never built, so the only writers are partner promotion and
-- merge survivorship and every other row sits on DEFAULT 'prospect' forever.
-- The label a reader saw was the default rendered as a finding. That is why
-- the backfill below can be this simple — there is almost no stored meaning to
-- carry across.

-- WHERE THE ACCOUNT STANDS. Single-valued: an account is at one point in a
-- sales motion at a time. It is a COLUMN and not a child row because a staged
-- approval re-validates its target's version at execute time (ADR-0036 §2) and
-- a version-less target is undecidable and fails closed — the 🟡 "their
-- contract ended, so this is a former customer" proposal needs a versioned row
-- to bind to.
ALTER TABLE organization
  ADD COLUMN lifecycle text NOT NULL DEFAULT 'unknown'
    CHECK (lifecycle IN ('unknown','target','prospect','opportunity','customer','former_customer','disqualified'));

CREATE INDEX idx_org_lifecycle ON organization (workspace_id, lifecycle) WHERE archived_at IS NULL;

-- WHAT THE COMPANY IS TO US. Multi-valued, because a company is legitimately
-- several things at once — the partner program is built on companies that are
-- simultaneously partners and customers, and an agency is often a reseller and
-- a client. A table rather than an array so each row carries its own
-- provenance: "they are a competitor" can be a human judgment or an inference,
-- and the two must stay distinguishable.
CREATE TABLE organization_relationship_type (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id      uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  organization_id   uuid NOT NULL,
  relationship_type text NOT NULL
                      CHECK (relationship_type IN ('customer','partner','supplier','investor','portfolio_company','competitor','other')),
  source            text NOT NULL,
  captured_by       text NOT NULL,
  version           bigint NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  archived_at       timestamptz NULL,
  -- Composite, so the database itself rejects a type row pointed at an
  -- organization in another workspace — RLS keeps a query inside one tenant,
  -- but only the FK keeps a WRITE from naming a row outside it.
  FOREIGN KEY (workspace_id, organization_id) REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

-- One live row per type per account; archiving frees the slot so a type can be
-- asserted again later without colliding with the record of the last time.
CREATE UNIQUE INDEX uq_org_rel_type ON organization_relationship_type (organization_id, relationship_type)
  WHERE archived_at IS NULL;
CREATE INDEX idx_org_rel_type_org ON organization_relationship_type (workspace_id, organization_id)
  WHERE archived_at IS NULL;

CREATE TRIGGER trg_organization_relationship_type_updated BEFORE UPDATE ON organization_relationship_type
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE organization_relationship_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_relationship_type FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_relationship_type_tenant_isolation ON organization_relationship_type
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON organization_relationship_type TO margince_app;

-- BACKFILL. lifecycle takes 'unknown' for everything that was not explicitly a
-- customer: carrying 'prospect' forward would re-assert in the new vocabulary
-- the same unearned claim the old default made, and waste the split.
UPDATE organization SET lifecycle = 'customer' WHERE classification = 'customer';

-- The type rows the old enum can be read as. tech_vendor/platform are things
-- we buy from. 'prospect' and 'other' name no relationship and produce no row.
--
-- 'partner' is NOT assigned here, and that is the whole subtlety: an org IS a
-- partner iff it carries this type AND a `partner` extension row, and the
-- store now refuses a patch that breaks either direction. Handing the type to
-- every old `agency` and `reseller` — which have no extension row, because
-- nothing ever created one for them — would land those rows in a state the
-- running system rejects, on the first edit anyone made. They map to no type
-- and a human retags them, which is the honest outcome for a column that
-- never had a writer.
INSERT INTO organization_relationship_type (workspace_id, organization_id, relationship_type, source, captured_by)
SELECT o.workspace_id, o.id,
       CASE o.classification
         WHEN 'customer'    THEN 'customer'
         WHEN 'competitor'  THEN 'competitor'
         WHEN 'tech_vendor' THEN 'supplier'
         WHEN 'platform'    THEN 'supplier'
       END,
       'migration', 'migration'
FROM organization o
WHERE o.classification IN ('customer','competitor','tech_vendor','platform')
  AND o.archived_at IS NULL
ON CONFLICT DO NOTHING;

-- The partner type comes from the EXTENSION ROW and only from it, which is
-- what makes the moved invariant true of every migrated row (ADR-0079 amending
-- ADR-0032 §Decision 2). A classification of 'partner' without an extension
-- row was already a broken state under ADR-0032; carrying it forward would
-- import that breakage into the vocabulary that now enforces it.
INSERT INTO organization_relationship_type (workspace_id, organization_id, relationship_type, source, captured_by)
SELECT p.workspace_id, p.organization_id, 'partner', 'migration', 'migration'
FROM partner p
JOIN organization o ON o.id = p.organization_id
-- LIVE partner rows only. An archived extension is not a partnership the
-- partner API will admit to (it filters archived_at IS NULL), so importing one
-- would mint a type row the invariant then refuses to let anyone remove.
WHERE o.archived_at IS NULL AND p.archived_at IS NULL
ON CONFLICT DO NOTHING;

-- classification is RETIRED, not dropped: kept one release, written by
-- nothing, `deprecated: true` on the wire, so the migration stays reversible
-- and the two vocabularies can be compared in production. A follow-up
-- migration drops it once no reader remains.
COMMENT ON COLUMN organization.classification IS
  'RETIRED (ADR-0079/A124) — superseded by organization.lifecycle + organization_relationship_type. Written by nothing; dropped in a follow-up migration.';
