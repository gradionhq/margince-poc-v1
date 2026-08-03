-- Reverses 0171. The relationship-typed ROWS go first, then the vocabularies
-- narrow back.
--
-- That order is forced: ADD CONSTRAINT ... CHECK validates the rows already in
-- the table, so narrowing a vocabulary while a 'relationship' row still holds it
-- aborts the whole rollback. On a fresh schema there is nothing to find and
-- either order appears to work, which is exactly why it is stated here rather
-- than discovered on the one database that has data.
--
-- Deleting them is the honest reverse: without the vocabulary these rows cannot
-- be written, and none of them is the edge itself — each is something hung OFF
-- an edge, so the relationship table is untouched.
DELETE FROM custom_field     WHERE object      = 'relationship';
DELETE FROM field_provenance WHERE object_type = 'relationship';
DELETE FROM embedding        WHERE entity_type = 'relationship';
DELETE FROM attachment       WHERE entity_type = 'relationship';

ALTER TABLE custom_field DROP CONSTRAINT custom_field_object_check;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_object_check
  CHECK (object IN ('person','organization','deal','lead','activity','project'));

ALTER TABLE field_provenance DROP CONSTRAINT field_provenance_object_type_check;
ALTER TABLE field_provenance ADD CONSTRAINT field_provenance_object_type_check
  CHECK (object_type IN ('person','organization','deal','lead','activity','project'));

ALTER TABLE embedding DROP CONSTRAINT embedding_entity_type_check;
ALTER TABLE embedding ADD CONSTRAINT embedding_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity','project'));

ALTER TABLE attachment DROP CONSTRAINT attachment_entity_type_check;
ALTER TABLE attachment ADD CONSTRAINT attachment_entity_type_check
  CHECK (entity_type IN ('person','organization','deal','lead','activity','project'));
