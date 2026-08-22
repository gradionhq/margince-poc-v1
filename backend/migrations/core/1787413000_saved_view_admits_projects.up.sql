-- A saved view may be over the projects list. The resource vocabulary is
-- the contract's SavedViewResource enum, which already names `projects`;
-- until now the table refused it, so the list a project view was saved
-- from answered 422 to a body the contract admits.
SET LOCAL lock_timeout = '5s';
ALTER TABLE saved_view DROP CONSTRAINT IF EXISTS saved_view_resource_check;
ALTER TABLE saved_view ADD CONSTRAINT saved_view_resource_check
    CHECK (resource IN ('people', 'organizations', 'deals', 'activities', 'leads', 'partners', 'projects'));
