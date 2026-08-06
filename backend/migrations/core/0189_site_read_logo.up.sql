-- 0184: the mark a website read resolved, parked on the dossier until the
-- company it belongs to exists.
--
-- An onboarding read runs BEFORE its organization does: it reads the
-- installation's own website to propose the anchor a human then confirms into
-- being. The logo lane (A55) has nowhere to put what it can plainly see —
-- organization.logo_* needs a row — and the seed page's declarations live only
-- in the crawl's memory, so by the time the confirmation creates that row the
-- mark is gone. The anchor is also the ONE company no later lane revisits (the
-- auto-enrich sweep takes rows named from a mail domain with no finished read,
-- and the anchor is neither), which makes that loss permanent: the company
-- every user sees first is the only one that can never have a face.
--
-- Same pair as organization.logo_*: a reference to the normalized bytes in
-- object storage plus the asset URL they were resolved from, never the bytes.
ALTER TABLE site_read ADD COLUMN logo_object_key text NULL;
ALTER TABLE site_read ADD COLUMN logo_origin     text NULL;
