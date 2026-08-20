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
