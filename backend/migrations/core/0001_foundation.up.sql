-- Foundation objects every later migration relies on (data-model §1).

-- uuidv7() shim for Postgres 16 (data-model §1.1, OQ-1): the ONE canonical
-- generator every non-app insert path must call. The app supplies v7 ids on
-- its own inserts; this shim serves DEFAULTs, backfills, and seeds. On
-- Postgres 18 this migration is replaced by the native function (the column
-- definitions are identical either way).
CREATE OR REPLACE FUNCTION uuidv7() RETURNS uuid
LANGUAGE sql VOLATILE AS $$
  SELECT encode(
    set_byte(
      set_byte(
        overlay(r PLACING substring(int8send((extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3) FROM 1 FOR 6),
        6, (get_byte(r, 6) & 15) | 112),
      8, (get_byte(r, 8) & 63) | 128),
    'hex')::uuid
  FROM (SELECT uuid_send(gen_random_uuid()) AS r) AS gen
$$;

-- Shared updated_at trigger (data-model §1.2), attached per table.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

-- Variant for mutable domain tables carrying the optimistic-concurrency
-- version column (data-model §1.3a): the same trigger bumps both.
CREATE OR REPLACE FUNCTION set_updated_at_bump_version() RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  NEW.version = OLD.version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
