-- Reverses 1787227316.
--
-- The grant leaves with the table: a role document naming an object no route
-- serves is a grant nobody can spend, and it would linger in every existing
-- workspace with nothing to remove it later.
SET LOCAL lock_timeout = '3s';

DROP TABLE IF EXISTS commission_entry;

UPDATE role
   SET permissions = permissions #- '{objects,commission}'
 WHERE is_system AND permissions->'objects' ? 'commission';
