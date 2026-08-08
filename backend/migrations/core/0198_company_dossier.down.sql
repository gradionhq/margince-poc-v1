-- Both are read-model caches of facts owned elsewhere, so dropping them loses
-- no record: the next read reassembles from the profile fields, facts and
-- source inventory they were built from.

DROP POLICY IF EXISTS org_growth_fit_ws ON org_growth_fit;
DROP POLICY IF EXISTS org_dossier_ws ON org_dossier;

DROP TABLE IF EXISTS org_growth_fit;
DROP TABLE IF EXISTS org_dossier;
