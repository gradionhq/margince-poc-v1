-- 0281: the brief and dossier caches drop the tenant column
-- (ADR-0091 §8 phase D).
--
-- Six tables, six indexes, one redundant unique. Every one of these is a
-- per-USER cache of a generated read — a morning brief, an account dossier, a
-- growth-fit read — so the key that matters is the reader and the record, and
-- three of them already say so: org_dossier, org_growth_fit and person_brief
-- are keyed (user_id, <record>) with the tenant nowhere in the key.
--
-- uq_brief_run_ws_id is phase B's leftover: a second copy of brief_run's own
-- primary key, created by 0019 as a composite foreign-key target that phase C
-- has since rewritten away.

DROP INDEX idx_brief_item_deal;
CREATE INDEX idx_brief_item_deal ON brief_item (deal_id) WHERE state <> 'new';

DROP INDEX idx_brief_run_user;
CREATE INDEX idx_brief_run_user ON brief_run (user_id, generated_at DESC);

DROP INDEX org_brief_organization_ix;
CREATE INDEX org_brief_organization_ix ON org_brief (organization_id);

DROP INDEX org_dossier_organization_ix;
CREATE INDEX org_dossier_organization_ix ON org_dossier (organization_id);

DROP INDEX org_growth_fit_organization_ix;
CREATE INDEX org_growth_fit_organization_ix ON org_growth_fit (organization_id);

DROP INDEX person_brief_person_ix;
CREATE INDEX person_brief_person_ix ON person_brief (person_id);

ALTER TABLE brief_run DROP CONSTRAINT uq_brief_run_ws_id;

ALTER TABLE brief_run DROP COLUMN workspace_id;
ALTER TABLE brief_item DROP COLUMN workspace_id;
ALTER TABLE org_brief DROP COLUMN workspace_id;
ALTER TABLE org_dossier DROP COLUMN workspace_id;
ALTER TABLE org_growth_fit DROP COLUMN workspace_id;
ALTER TABLE person_brief DROP COLUMN workspace_id;
