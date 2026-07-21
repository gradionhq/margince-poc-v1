-- 0091: site_read live progress — the deep read now runs a fast crawl
-- plus one-to-few large model calls, and the SPA's poll should show WHERE
-- the read is instead of a silent 'running'. phase names the current
-- stage (crawling | extracting); pages_read counts committed pages as
-- they land. Both are worker-written hints, cleared irrelevant once the
-- terminal status is set — the finish report stays the authority.
ALTER TABLE site_read ADD COLUMN phase text CHECK (phase IN ('crawling','extracting')),
                      ADD COLUMN pages_read integer NOT NULL DEFAULT 0;
