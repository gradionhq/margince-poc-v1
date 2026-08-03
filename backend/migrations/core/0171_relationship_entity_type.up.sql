-- Relationship joins the EntityType vocabulary.
--
-- An edge becomes a first-class resource on the record verbs (create, read,
-- update, archive through the datasource seam), which means the Go constant
-- datasource.EntityRelationship now exists — and
-- TestEveryDomainEnumMatchesItsSchemaCheck derives the EntityType set from
-- every constant of that type DECLARED in the package, not from EntityTypes().
-- So all four EntityType-bound CHECKs widen together or none of them may: there
-- is no additive option that widens three.
--
-- What widening each CHECK actually opens is a separate question, answered one
-- level up at each facility's own acceptance gate, and for three of the four the
-- answer is "nothing":
--
--   attachment.entity_type      the contract's own enum Valid() stays closed, so
--                               an upload for an edge still 422s honestly
--   embedding.entity_type       the embed lanes (searchBranches/embedText) stay
--                               closed, and edges emit no relationship.* events,
--                               so no writer can exist
--   field_provenance.object_type  the writers pass hardcoded literals; the
--                               relationship row's own source/captured_by
--                               already carry its provenance
--   custom_field.object         customfields.FieldObjects is the gate, and it is
--                               now an EXPLICIT list that excludes relationship
--                               (and activity) for want of contract carriage and
--                               store wiring — see the engine's own comment
--
-- The CHECK therefore ends up WIDER than the custom-field engine's allowlist,
-- which is the same posture the other three already have and is safe because the
-- engine is that table's only writer. Custom fields on edges is deliberately its
-- own future change, and it starts with a privacy question (does a cf_* column
-- on an edge make `relationship` a piiTables member?), not with an ALTER.
--
-- Additive only (ADR-0017): drop and re-add each CHECK wider, exactly as
-- 0131_project did for the same four columns. RecordType is untouched —
-- an edge is not a record you can tag, list, or link an activity to.

ALTER TABLE attachment DROP CONSTRAINT attachment_entity_type_check;
ALTER TABLE attachment ADD CONSTRAINT attachment_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity','project','relationship'));

ALTER TABLE embedding DROP CONSTRAINT embedding_entity_type_check;
ALTER TABLE embedding ADD CONSTRAINT embedding_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity','project','relationship'));

ALTER TABLE field_provenance DROP CONSTRAINT field_provenance_object_type_check;
ALTER TABLE field_provenance ADD CONSTRAINT field_provenance_object_type_check
  CHECK (object_type IN ('person','organization','deal','lead','activity','project','relationship'));

ALTER TABLE custom_field DROP CONSTRAINT custom_field_object_check;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_object_check
  CHECK (object IN ('person','organization','deal','lead','activity','project','relationship'));
