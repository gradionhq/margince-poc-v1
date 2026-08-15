-- activity.kind stops naming a transport (ADR-0107/A158). The two provider-named
-- members collapse into one semantic member, `message`, and which transport
-- carried a row is read from channel_provider — the column 0247 added, populated
-- for every channel row before this migration was allowed to run.
--
-- This is the half that does not reverse cleanly. See the down migration: it can
-- reconstruct telegram and whatsapp because those two names still exist as
-- providers, but a message on any provider registered AFTER this point has no
-- pre-narrowing spelling at all, and the down refuses rather than inventing one.

-- The new member first: activity.kind FKs into this table, so the rows below
-- cannot be updated to a kind that is not yet registered.
INSERT INTO activity_kind (kind) VALUES ('message');

-- whatsapp becomes a registered TRANSPORT rather than an interaction kind.
--
-- It has to exist before the backfill, because activity.channel_provider FKs
-- here. The alternative — mapping hand-logged whatsapp rows to a non-channel kind
-- — would silently rewrite a rep's statement that they messaged someone on
-- WhatsApp into something else, and there is no need: A103 ratifies that a real
-- WhatsApp connector is coming, so registering it now is the accurate description
-- of a transport with no sender yet, not a placeholder.
--
-- transport='core' says the name belongs to the core vocabulary rather than to an
-- extension unit. It does NOT claim a connector is compiled in — nothing is
-- composed for whatsapp, so the boot reconcile leaves it out of the in-memory
-- sendable set and a reply on it is refused by the send pre-flight, which is the
-- correct answer until the connector lands. Registration and sendability are
-- separate questions; conflating them is what this whole decision removes.
INSERT INTO channel_provider (provider, transport) VALUES ('whatsapp', 'core')
  ON CONFLICT (provider) DO NOTHING;

-- The narrowing itself. COALESCE, not a bare assignment: 0247 already populated
-- channel_provider for every telegram row, and re-deriving it from kind here
-- would overwrite a correct value with the same value for telegram while being
-- the ONLY source for whatsapp. Preserving what 0247 wrote keeps this migration
-- honest about which one is the source of truth.
UPDATE activity
   SET channel_provider = COALESCE(channel_provider, kind),
       kind             = 'message'
 WHERE kind IN ('telegram', 'whatsapp');

-- Now the old members can go. Nothing references activity_kind any more except
-- activity.kind itself: 0247 dropped channel_provider's FK into it, which is what
-- lets telegram survive as a provider while ceasing to be a kind.
DELETE FROM activity_kind WHERE kind IN ('telegram', 'whatsapp');

-- The two axes stay in step, in BOTH directions: a message always names the
-- transport that carried it, and nothing else ever does. The forward direction is
-- what makes the send path's read total; the reverse is what stops an email or a
-- note from quietly acquiring a transport it never had.
ALTER TABLE activity ADD CONSTRAINT activity_message_has_provider
  CHECK ((kind = 'message') = (channel_provider IS NOT NULL));

-- The index 0247 deliberately did not add, added here with the reader that earns
-- it: the reply-match now discriminates a channel conversation by provider rather
-- than by kind, so capture resolves (workspace_id, channel_provider, thread_key)
-- on every inbound channel message. Partial, because only message rows have a
-- provider and they are a minority of the busiest table in the schema.
CREATE INDEX idx_activity_channel_thread
  ON activity (workspace_id, channel_provider, thread_key)
  WHERE channel_provider IS NOT NULL;
