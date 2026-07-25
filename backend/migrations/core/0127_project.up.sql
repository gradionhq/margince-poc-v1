-- 0127: project as a first-class record (ADR-0073/A119, E21) — the noun
-- for a body of work at a client. A deal is wrong in both directions: too
-- small, because a programme spawns several contracts over years, and too
-- short, because it ends at closed-won and the relationship does not.
--
-- Three tables here, and then the sweep: `project` joins the canonical
-- record vocabulary (PROJ-DDL-N-1), which is what admits it to the
-- timeline, lists, tags, attachments, grants and custom fields without a
-- new mechanism. Every one of those CHECKs is pinned to the Go vocabulary
-- by TestEveryDomainEnumMatchesItsSchemaCheck, so widening some and not
-- others fails the build rather than one insert nobody ran.

CREATE TABLE project (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name             text NOT NULL,
  -- The short handle a human writes in a subject line ("[ERP-27]"), and
  -- the PROJ-FORM-1 match target. Letter-led and bounded so a key can
  -- never be a bare number, which would match dates, amounts and order
  -- numbers (PROJ-PARAM-1).
  key              text NULL,
  -- The anchor company, singular by decision (ADR-0073). RESTRICT: a
  -- company with live work on it is not deletable out from under it.
  organization_id  uuid NOT NULL,
  owner_id         uuid NULL,
  phase            text NOT NULL DEFAULT 'initiative'
                     CHECK (phase IN ('initiative','pursuing','delivering','closed')),  -- PROJ-PARAM-2
  closed_reason    text NULL,
  description      text NULL,
  started_at       date NULL,
  target_end_date  date NULL,
  ended_at         date NULL,
  -- Denormalized accelerator maintained on link write (PROJ-FORM-6); a
  -- rebuild from activity_link must reproduce it exactly (PROJ-AC-12).
  last_activity_at timestamptz NULL,
  visibility       text NOT NULL DEFAULT 'workspace' CHECK (visibility IN ('workspace','owner')),
  source           text NOT NULL,
  captured_by      text NOT NULL,
  raw              jsonb NULL,
  -- Proper-noun fields stay 'simple' (0052's linguistic rule): a project
  -- name and key are identifiers, not prose to be stemmed.
  search_tsv       tsvector GENERATED ALWAYS AS (
                     setweight(to_tsvector('simple', f_unaccent(coalesce(name,''))), 'A') ||
                     setweight(to_tsvector('simple', f_unaccent(coalesce(key,''))),  'A')
                   ) STORED,
  version          bigint NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  archived_at      timestamptz NULL,

  CONSTRAINT project_key_shape     CHECK (key IS NULL OR key ~ '^[A-Za-z][A-Za-z0-9_-]{1,23}$'),
  CONSTRAINT project_closed_reason CHECK (phase <> 'closed' OR closed_reason IS NOT NULL),
  CONSTRAINT project_dates         CHECK (ended_at IS NULL OR started_at IS NULL OR ended_at >= started_at),

  -- The composite unique key every child and pointer FK matches against.
  CONSTRAINT uq_project_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT project_organization_id_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE RESTRICT,
  CONSTRAINT project_owner_id_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id)
);

-- The key is unique among LIVE rows only: archiving a project frees its
-- key for reuse (PROJ-DDL-N-2).
CREATE UNIQUE INDEX uq_project_key ON project (workspace_id, lower(key))
  WHERE key IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_project_org       ON project (workspace_id, organization_id) WHERE archived_at IS NULL;
-- The PROJ-FORM-4 sole-candidate probe must be one indexed count, not a scan.
CREATE INDEX idx_project_org_open  ON project (workspace_id, organization_id)
  WHERE phase <> 'closed' AND archived_at IS NULL;
CREATE INDEX idx_project_owner     ON project (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_project_name_trgm ON project USING gin (f_unaccent(lower(name)) gin_trgm_ops);
CREATE INDEX idx_project_search    ON project USING gin (search_tsv);

CREATE TRIGGER trg_project_updated BEFORE UPDATE ON project
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE project ENABLE ROW LEVEL SECURITY;
ALTER TABLE project FORCE ROW LEVEL SECURITY;
CREATE POLICY project_tenant_isolation ON project
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);


-- PROJ-DDL-2: append-only phase history, the deal_stage_history pattern.
-- A phase is a claim about where the work stands; the history is what
-- makes the claim answerable later.
CREATE TABLE project_phase_history (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  project_id   uuid NOT NULL,
  from_phase   text NULL,                -- NULL on the creation row
  to_phase     text NOT NULL,
  reason       text NULL,                -- carries closed_reason on a transition to 'closed'
  changed_by   text NOT NULL,            -- typed principal
  occurred_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT project_phase_history_project_id_fkey FOREIGN KEY (workspace_id, project_id)
    REFERENCES project (workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_pph_project ON project_phase_history (workspace_id, project_id, occurred_at DESC);

ALTER TABLE project_phase_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_phase_history FORCE ROW LEVEL SECURITY;
CREATE POLICY project_phase_history_tenant_isolation ON project_phase_history
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);


-- PROJ-DDL-3 (`project_link_candidate`) is deliberately NOT here. Its
-- `evidence` column holds a verbatim snippet quoted out of a message body,
-- which makes it a second home for a subject's personal data — and erasure
-- SCRUBS activities rather than deleting them, so an ON DELETE CASCADE
-- would never fire to clean it. The table therefore lands with its writer,
-- its erasure coverage and its SAR section in one change, not ahead of them.


-- ---------------------------------------------------------------------
-- The timeline arm. Same shape as the other four (0038 is the precedent):
-- composite tenant FK, exactly-one-target CHECK, dedupe index.
ALTER TABLE activity_link ADD COLUMN project_id uuid NULL;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_project_id_fkey
  FOREIGN KEY (workspace_id, project_id) REFERENCES project (workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_entity_type_check;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','project'));

ALTER TABLE activity_link DROP CONSTRAINT activity_link_shape;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_shape CHECK (
  (entity_type='person'       AND person_id IS NOT NULL AND organization_id IS NULL AND deal_id IS NULL AND lead_id IS NULL AND project_id IS NULL) OR
  (entity_type='organization' AND organization_id IS NOT NULL AND person_id IS NULL AND deal_id IS NULL AND lead_id IS NULL AND project_id IS NULL) OR
  (entity_type='deal'         AND deal_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND lead_id IS NULL AND project_id IS NULL) OR
  (entity_type='lead'         AND lead_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND deal_id IS NULL AND project_id IS NULL) OR
  (entity_type='project'      AND project_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL AND deal_id IS NULL AND lead_id IS NULL)
);

DROP INDEX uq_activity_link;
CREATE UNIQUE INDEX uq_activity_link
  ON activity_link (activity_id, entity_type, coalesce(person_id, organization_id, deal_id, lead_id, project_id));
CREATE INDEX idx_alink_project ON activity_link (project_id) WHERE project_id IS NOT NULL;
-- At most ONE project link per activity (PROJ-AC-15): the ladder decides
-- once and never overwrites; replacement is relink's job alone, and this
-- index is what makes a second link impossible rather than merely unwritten.
CREATE UNIQUE INDEX uq_activity_link_project ON activity_link (activity_id) WHERE entity_type = 'project';


-- ---------------------------------------------------------------------
-- The deal rollup: a deal belongs to at most one project; a project
-- carries several deals over time.
ALTER TABLE deal ADD COLUMN project_id uuid NULL;
ALTER TABLE deal ADD CONSTRAINT deal_project_id_fkey
  FOREIGN KEY (workspace_id, project_id) REFERENCES project (workspace_id, id) ON DELETE SET NULL (project_id);
CREATE INDEX idx_deal_project ON deal (workspace_id, project_id) WHERE project_id IS NOT NULL AND archived_at IS NULL;

-- A deal and its project must name the same company (PROJ-AC-17). This
-- cannot be a CHECK — it spans two rows — so it is a constraint trigger,
-- raising check_violation so the API maps it to a 4xx rather than a 500.
CREATE FUNCTION assert_deal_project_same_org() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.project_id IS NULL THEN
    RETURN NULL;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM project p
    WHERE p.id = NEW.project_id
      AND p.organization_id IS NOT DISTINCT FROM NEW.organization_id
  ) THEN
    RAISE EXCEPTION 'deal and project belong to different companies'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'deal_project_same_org';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER trg_deal_project_same_org
  AFTER INSERT OR UPDATE OF organization_id, project_id ON deal
  DEFERRABLE INITIALLY IMMEDIATE
  FOR EACH ROW EXECUTE FUNCTION assert_deal_project_same_org();


-- ---------------------------------------------------------------------
-- People on a project, as a typed edge — the deal_stakeholder shape, so
-- "one person is accountable for three projects" is a query, not a note.
ALTER TABLE relationship ADD COLUMN project_id uuid NULL;
ALTER TABLE relationship ADD CONSTRAINT relationship_project_id_fkey
  FOREIGN KEY (workspace_id, project_id) REFERENCES project (workspace_id, id) ON DELETE CASCADE;

ALTER TABLE relationship DROP CONSTRAINT relationship_kind_check;
ALTER TABLE relationship ADD CONSTRAINT relationship_kind_check
  CHECK (kind IN ('employment','deal_stakeholder','partner_of','referred_by','co_sell_with','project_stakeholder'));

ALTER TABLE relationship ADD CONSTRAINT rel_project_stakeholder_shape CHECK (
  kind <> 'project_stakeholder'
  OR (project_id IS NOT NULL AND person_id IS NOT NULL
      AND organization_id IS NULL AND counterparty_org_id IS NULL AND deal_id IS NULL)
);
-- The existing arms gain the mirror clause: a project_id on an employment
-- or stakeholder edge is a mis-shaped row, not an ignorable extra.
ALTER TABLE relationship DROP CONSTRAINT rel_employment_shape;
ALTER TABLE relationship ADD CONSTRAINT rel_employment_shape CHECK (
  kind <> 'employment' OR (person_id IS NOT NULL AND organization_id IS NOT NULL AND deal_id IS NULL AND project_id IS NULL)
);
ALTER TABLE relationship DROP CONSTRAINT rel_stakeholder_shape;
ALTER TABLE relationship ADD CONSTRAINT rel_stakeholder_shape CHECK (
  kind <> 'deal_stakeholder' OR (deal_id IS NOT NULL AND person_id IS NOT NULL AND organization_id IS NULL AND project_id IS NULL)
);
ALTER TABLE relationship DROP CONSTRAINT rel_partner_shape;
ALTER TABLE relationship ADD CONSTRAINT rel_partner_shape CHECK (
  kind NOT IN ('partner_of','referred_by','co_sell_with')
  OR (organization_id IS NOT NULL AND counterparty_org_id IS NOT NULL
      AND organization_id <> counterparty_org_id AND person_id IS NULL AND deal_id IS NULL AND project_id IS NULL)
);

CREATE INDEX idx_rel_project_stakeholders ON relationship (workspace_id, project_id)
  WHERE kind = 'project_stakeholder' AND archived_at IS NULL;
CREATE INDEX idx_rel_person_projects ON relationship (person_id)
  WHERE kind = 'project_stakeholder' AND archived_at IS NULL;
CREATE UNIQUE INDEX uq_rel_project_stakeholder ON relationship (project_id, person_id)
  WHERE kind = 'project_stakeholder' AND archived_at IS NULL;


-- ---------------------------------------------------------------------
-- The vocabulary sweep (PROJ-DDL-N-1). Two sets, because they are two
-- vocabularies: a reference TO a record never names an activity, a thing
-- hung OFF an object can.
ALTER TABLE list DROP CONSTRAINT list_entity_type_check;
ALTER TABLE list ADD CONSTRAINT list_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','project'));

ALTER TABLE list_member DROP CONSTRAINT list_member_entity_type_check;
ALTER TABLE list_member ADD CONSTRAINT list_member_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','project'));

ALTER TABLE taggable DROP CONSTRAINT taggable_entity_type_check;
ALTER TABLE taggable ADD CONSTRAINT taggable_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','project'));

ALTER TABLE record_grant DROP CONSTRAINT record_grant_record_type_check;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_record_type_check
  CHECK (record_type IN ('person','organization','deal','lead','project'));

ALTER TABLE attachment DROP CONSTRAINT attachment_entity_type_check;
ALTER TABLE attachment ADD CONSTRAINT attachment_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity','project'));

ALTER TABLE embedding DROP CONSTRAINT embedding_entity_type_check;
ALTER TABLE embedding ADD CONSTRAINT embedding_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity','project'));

ALTER TABLE field_provenance DROP CONSTRAINT field_provenance_object_type_check;
ALTER TABLE field_provenance ADD CONSTRAINT field_provenance_object_type_check
  CHECK (object_type IN ('person','organization','deal','lead','activity','project'));

ALTER TABLE custom_field DROP CONSTRAINT custom_field_object_check;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_object_check
  CHECK (object IN ('person','organization','deal','lead','activity','project'));
