-- A project can be placed under legal hold, exactly like a person,
-- organization, deal or lead.
--
-- Correspondence filed under a project is business correspondence
-- (1787400000_project_linked_correspondence), and a project is where a
-- multi-year engagement's evidence collects. Until now there was no way to
-- freeze it: the privacy selectors read legal_hold through an activity's
-- organization and deal links only, so an activity linked ONLY to a project
-- in litigation could still be erased by an Art. 17 cascade or swept by the
-- retention evaluator. The column is set by an operator in the database, as
-- on its siblings; no API writes it.

SET LOCAL lock_timeout = '3s';

ALTER TABLE project ADD COLUMN legal_hold boolean DEFAULT false NOT NULL;
