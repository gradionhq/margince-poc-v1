-- 0069: record the media type of the response an idempotency claim
-- stores, so a replay repeats the ORIGINAL response verbatim — status,
-- body, AND Content-Type — instead of restamping every replay as
-- application/json. Existing rows all recorded contract JSON responses,
-- so the backfill default is exact for them; new settlements write the
-- response's actual header.
ALTER TABLE idempotency_key
  ADD COLUMN response_content_type text NOT NULL DEFAULT 'application/json';
