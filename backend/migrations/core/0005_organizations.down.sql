DROP TABLE IF EXISTS partner;
DROP TABLE IF EXISTS organization_domain;
DROP TRIGGER IF EXISTS trg_organization_no_cycle ON organization;
DROP FUNCTION IF EXISTS organization_no_ancestor_cycle();
DROP TABLE IF EXISTS organization;
