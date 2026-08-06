-- The parked reference goes; the objects it named stay. Dropping a column is
-- not a licence to delete bytes a confirmed organization may already point at
-- under the very same key.
ALTER TABLE site_read DROP COLUMN logo_origin;
ALTER TABLE site_read DROP COLUMN logo_object_key;
