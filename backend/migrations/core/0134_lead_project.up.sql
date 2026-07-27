-- 0130: a lead may belong to a project (A119 Amendment 1.A, PROJ-DDL-4).
--
-- The original decision gave the project conversations, people and deals,
-- which quietly assumed it is only populated once there is commercial
-- intent. That is wrong at the head of its own ladder: a project is born in
-- `initiative`, BEFORE any deal exists, and the object carrying interest at
-- that stage is the lead. An enquiry about a named programme had nowhere to
-- go but the person.
--
-- There is deliberately NO same-company guard here, and there cannot be
-- one: a lead holds candidate_org_key, not organization_id (ADR-0008), so
-- it has no company to compare and deal_project_same_org has no lead twin.
-- Promotion is where a lead acquires a company, and where a mismatch
-- becomes visible (Note PROJ-DDL-N-4).
--
-- SET NULL rather than CASCADE on delete: losing the grouping must never
-- take the prospect with it.
ALTER TABLE lead ADD COLUMN project_id uuid NULL;
ALTER TABLE lead ADD CONSTRAINT lead_project_id_fkey
  FOREIGN KEY (workspace_id, project_id) REFERENCES project (workspace_id, id) ON DELETE SET NULL (project_id);
CREATE INDEX idx_lead_project ON lead (workspace_id, project_id)
  WHERE project_id IS NOT NULL AND archived_at IS NULL;
