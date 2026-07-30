-- 0147: the erasure suppression list gains channel identities.
--
-- Suppression is what stops an erased subject being resurrected by the next
-- inbound message (0031). With only kind='email' it could only stop mail: a
-- Telegram-only subject would be erased and then recreated by their very next
-- message, with nothing erroring and nothing logged.
--
-- The hashed value is 'provider:channel_user_id', never the bot id. Telegram
-- user ids are GLOBAL rather than bot-scoped, so omitting the bot is what keeps
-- an erasure holding after the workspace rotates its bot — the same reason
-- person_channel_identity's unique key omits channel_id (0146).

ALTER TABLE erasure_suppression DROP CONSTRAINT erasure_suppression_kind_check;
ALTER TABLE erasure_suppression
  ADD CONSTRAINT erasure_suppression_kind_check CHECK (kind IN ('email', 'channel_identity'));
