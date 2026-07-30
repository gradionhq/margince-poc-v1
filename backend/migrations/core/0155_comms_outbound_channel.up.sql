-- 0149: comms_outbound becomes shape-neutral, so ONE delivery row, ONE retry
-- ladder, ONE seat check and ONE consent gate serve mail and a messaging
-- channel alike (telegram-oa design §8.3).
--
-- A channel message has no RFC822 identity, no subject and no address list, so
-- the five columns that assumed mail become nullable and a channel recipient
-- column joins them.
--
-- The CHECK is what keeps "nullable" from meaning "optional". A row is
-- mail-shaped or channel-shaped and never half of each: without it a mail
-- delivery could commit with no addressees and a channel delivery with no
-- recipient, and both would be discovered by the dispatcher at transmit time —
-- against a live provider, minutes after the writer that could have been told
-- has gone.
--
-- channel_user_id holds the provider's own account id for the recipient, the
-- same key person_channel_identity resolves on (0146), and deliberately NOT the
-- bot id: Telegram user ids are global, so a workspace that rotates its bot
-- keeps every staged delivery deliverable.
--
-- It is also the row's SHAPE DISCRIMINATOR, which is why the channel arm demands
-- it be non-null rather than merely present. A privacy scrub that must remove
-- the recipient therefore EMPTIES it, exactly as the mail arm's address lists
-- are emptied rather than nulled (privacy/deliveries.go) — nulling it would
-- re-declare the row as mail-shaped with every mail column missing, and the
-- constraint below would refuse the erasure.
--
-- The DEFAULTs on cc and references_chain deliberately STAY. They belong to the
-- mail shape, where an omitted list means an empty one, and a channel writer
-- names both as NULL explicitly (comms.Store.StageChannelTx) rather than
-- inheriting a mail default the CHECK would then refuse.
ALTER TABLE comms_outbound
  ALTER COLUMN message_id DROP NOT NULL,
  ALTER COLUMN recipients DROP NOT NULL,
  ALTER COLUMN cc DROP NOT NULL,
  ALTER COLUMN subject DROP NOT NULL,
  ALTER COLUMN references_chain DROP NOT NULL,
  ADD COLUMN channel_user_id text NULL;

ALTER TABLE comms_outbound
  ADD CONSTRAINT comms_outbound_shape CHECK (
    (channel_user_id IS NULL
       AND message_id IS NOT NULL
       AND recipients IS NOT NULL
       AND cc IS NOT NULL
       AND subject IS NOT NULL
       AND references_chain IS NOT NULL)
    OR
    (channel_user_id IS NOT NULL
       AND message_id IS NULL
       AND recipients IS NULL
       AND cc IS NULL
       AND subject IS NULL
       AND references_chain IS NULL
       -- Both are mail headers with no channel meaning: thread_key is the
       -- RFC822 conversation identity, and list_unsubscribe is an RFC 8058
       -- target. A channel row carrying either would be mail bookkeeping filed
       -- under a message that never had any.
       AND thread_key IS NULL
       AND list_unsubscribe IS NULL)
  );
