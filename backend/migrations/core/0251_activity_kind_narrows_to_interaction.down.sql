-- Reversing the narrowing is possible for exactly the two names that existed
-- before it, and impossible for anything registered since. That asymmetry is the
-- point of the guard below rather than something to discover mid-rollback.

DROP INDEX idx_activity_channel_thread;

ALTER TABLE activity DROP CONSTRAINT activity_message_has_provider;

-- REFUSE rather than corrupt. A message carried by a provider that was never an
-- activity kind — any transport registered after the narrowing, including every
-- extension unit's — has no pre-narrowing spelling. Mapping it to 'note' would
-- destroy the fact that it was a message; mapping it to its provider name would
-- re-create a kind the pre-narrowing schema never had. Neither is a rollback, so
-- this one stops and says which providers are in the way.
DO $$
DECLARE
  stranded text;
BEGIN
  SELECT string_agg(DISTINCT channel_provider, ', ' ORDER BY channel_provider)
    INTO stranded
    FROM activity
   WHERE kind = 'message'
     -- coalesce, because the CHECK was dropped two statements above: a
     -- provider-less message row is possible again here, and NOT IN would
     -- answer NULL (not true) and wave it through — the UPDATE below would
     -- then write NULL into kind and fail on a NOT NULL violation instead of
     -- on the specific refusal this guard exists to give.
     AND coalesce(channel_provider, '') NOT IN ('telegram', 'whatsapp');

  IF stranded IS NOT NULL THEN
    RAISE EXCEPTION
      'cannot reverse the kind narrowing: % carried messages that have no pre-narrowing activity kind. Reversing would have to discard which transport carried them. Archive or re-file those rows first.',
      stranded;
  END IF;
END $$;

-- The kinds come back before the rows that reference them, since activity.kind
-- FKs into this table.
INSERT INTO activity_kind (kind) VALUES ('telegram'), ('whatsapp')
  ON CONFLICT (kind) DO NOTHING;

-- The provider column stays populated: 0247 owns it, and at THIS point the two
-- spellings agree again for every row, which is the state 0247's own down
-- migration documents as the last one where the transport is still recoverable.
UPDATE activity SET kind = channel_provider WHERE kind = 'message';

DELETE FROM activity_kind WHERE kind = 'message';

-- whatsapp goes back to being a kind and not a transport, which is what it was
-- before the up migration registered it. Guarded, because person_channel_identity
-- may reference it if a WhatsApp connector landed in the meantime — in which case
-- it is a real transport now and deleting the row is not this migration's call.
--
-- RESIDUE, stated rather than glossed: on an installation that actually HAS
-- whatsapp rows — precisely the one the up migration's registration was for —
-- both guards fail, so the registry row survives and those rows keep
-- channel_provider = 'whatsapp' where 0247's post-state left it NULL. The kind
-- is restored correctly either way; what does not fully reverse is the provider
-- column on rows the up migration was the first to populate.
DELETE FROM channel_provider
 WHERE provider = 'whatsapp'
   AND NOT EXISTS (SELECT 1 FROM person_channel_identity WHERE provider = 'whatsapp')
   AND NOT EXISTS (SELECT 1 FROM activity WHERE channel_provider = 'whatsapp');
