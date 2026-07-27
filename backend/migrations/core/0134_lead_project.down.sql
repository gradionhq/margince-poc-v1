DROP INDEX idx_lead_project;
ALTER TABLE lead DROP CONSTRAINT lead_project_id_fkey;
ALTER TABLE lead DROP COLUMN project_id;
