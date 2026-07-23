-- One-way caveat: this rollback re-canonicalizes every stored body (jsonb
-- normalizes key order, whitespace, and numeric formatting on write), so a
-- delivery parked under 0116 loses the verbatim bytes it was signed with and
-- would re-sign a canonicalized body on the next retry/replay. The conversion
-- is NOT byte-reversible and cannot be made so — jsonb cannot preserve the
-- original text. Roll back only when webhook_delivery holds no parked/retrying
-- rows whose signature integrity matters (or accept that those rows re-sign).
ALTER TABLE webhook_delivery ALTER COLUMN payload TYPE jsonb USING payload::jsonb;
