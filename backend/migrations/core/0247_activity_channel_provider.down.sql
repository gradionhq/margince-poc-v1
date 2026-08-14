-- Reversing this DISCARDS which provider carried each message, and that is worth
-- stating rather than discovering. At THIS migration the fact is still recoverable,
-- because activity.kind holds the provider name too — the column and the kind agree
-- for every channel row. It stops being recoverable at the migration that narrows
-- kind to its semantic members: after that one, dropping this column erases the
-- only place the transport is recorded, and no down migration can reconstruct it.

ALTER TABLE activity DROP COLUMN channel_provider;

ALTER TABLE channel_provider DROP CONSTRAINT channel_provider_provider_grammar;

-- Restoring the provider->kind FK is the half that can legitimately FAIL, and it
-- is worth being explicit about rather than letting Postgres report it as a raw
-- constraint violation on a rollback somebody is already unhappy about.
--
-- After the up migration, boot registers a provider WITHOUT minting an
-- activity_kind row for it — that is the whole point of dropping this FK. So any
-- installation whose composed set grew a provider beyond the seeded ones now holds
-- a channel_provider row with no matching kind, and re-adding the FK validates
-- against exactly that row and refuses.
--
-- The kind rows are re-created for precisely those providers first, which is the
-- state 0240 would have left behind, so the FK can validate. It is additive and
-- idempotent, and it does not invent a kind for anything the registry does not
-- already carry.
INSERT INTO activity_kind (kind)
SELECT provider FROM channel_provider
ON CONFLICT (kind) DO NOTHING;

ALTER TABLE channel_provider ADD CONSTRAINT channel_provider_provider_fkey
  FOREIGN KEY (provider) REFERENCES activity_kind (kind);
