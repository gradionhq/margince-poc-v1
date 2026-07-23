-- 0116: webhook_delivery.payload jsonb -> text (byte-preserving), so a
-- parked delivery replays VERBATIM. jsonb canonicalizes on write/read
-- (key order, whitespace, numeric formatting), which silently rewrote the
-- signed body across a reload and across retry attempts — defeating the
-- "kept verbatim" guarantee 0113 documents for replay after the source bus
-- event has been trimmed (events.md §4.4). No jsonb operator is ever used
-- against this column (it is only read/written as whole bytes), so text
-- loses nothing the code depends on.
ALTER TABLE webhook_delivery ALTER COLUMN payload TYPE text USING payload::text;
