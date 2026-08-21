-- `source` names WHERE a row came from, not which door it arrived through.
--
-- Two spellings meant "a first-party write by a person using this product":
-- `manual` from the web app and `ui` from the project surface, plus `mcp` from
-- the tool surface — where a person is likewise the one who asked, through an
-- assistant instead of a form. Three words for one origin made the column
-- unreadable and, worse, load-bearing in the wrong place: retrieval ranking
-- used to read it to tell a human statement from an agent write. That ladder
-- now reads `captured_by`, which records the actual writer, so this column is
-- free to say the one thing it should.
--
-- Collapsing to `manual` is safe ONLY because that ranking change landed
-- first. Reversed, every agent-written note would have been promoted to
-- human-statement trust silently.
--
-- The down migration cannot restore the distinction: once the three words are
-- one word, nothing in the row says which it was. `captured_by` still does,
-- for anything that needs to know.

-- COST, stated rather than discovered. None of these tables has an index on
-- `source`, so each UPDATE is a full scan, and all fifteen run in one
-- transaction — the row locks it takes are held until the whole thing commits.
-- lock_timeout bounds how long a statement WAITS for a lock; it does not bound
-- how long these scans run.
--
-- That is acceptable here because the predicate matches a minority of rows and
-- these tables are small at this stage. On a large installation this wants
-- batching by primary key with a commit per batch. It is written as one
-- transaction on purpose: a half-applied spelling is the one outcome worse
-- than a slow one, since nothing afterwards could tell which tables were swept.
SET LOCAL lock_timeout = '3s';

UPDATE person             SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE organization       SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE deal               SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE lead               SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE activity           SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE project            SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE relationship       SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE organization_domain SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE dedupe_candidate   SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE person_email       SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE person_phone       SET source = 'manual' WHERE source IN ('mcp', 'ui');

-- The same word on four tables outside the CRM record set. Each is written by
-- a person acting through this product — recording their own writing voice,
-- resolving a signal — so each carried 'ui' for the same reason and gets the
-- same correction. They are here rather than left alone because a spelling
-- half-collapsed is worse than either whole one: a reader would have to know
-- which tables were swept.
UPDATE voice_profile         SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE voice_corpus_source   SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE voice_profile_version SET source = 'manual' WHERE source IN ('mcp', 'ui');
UPDATE signal_resolution     SET source = 'manual' WHERE source IN ('mcp', 'ui');
