-- The transport axis leaves activity.kind (DESIGN-SP5 §1). Which provider carried
-- a message becomes its own column — a reference into the channel_provider
-- registry — instead of being recovered by reinterpreting the interaction kind as
-- a provider name, which is what activities/channelsend.go's
-- `provider := string(anchor.Kind)` does today.
--
-- kind is UNCHANGED here: a channel message still carries 'telegram', and every
-- reader of kind keeps working. This migration adds the column and populates it so
-- the writers and readers can move onto it in the same change. Narrowing kind to
-- its six semantic members is the NEXT slice, and it depends on this column
-- already being correct for every row.

ALTER TABLE activity ADD COLUMN channel_provider text
  REFERENCES channel_provider (provider);

-- Populated FROM THE REGISTRY rather than from a literal provider list, and it is
-- exactly correct today precisely BECAUSE the old model stored the provider in
-- kind. That equivalence is the whole reason this backfill needs no judgement
-- about which rows were channel messages.
UPDATE activity SET channel_provider = kind
  WHERE kind IN (SELECT provider FROM channel_provider);

-- whatsapp is deliberately NOT touched, and it is NOT asserted absent. It is a
-- kind the contract admits on POST /v1/activities and that the log_activity agent
-- tool advertises, so a rep or an agent may already have hand-logged one — an
-- earlier draft of this migration asserted zero such rows and would have bricked
-- the deploy of any installation that had. It also buys nothing here: whatsapp has
-- never been a channel_provider row, so a whatsapp activity was not a channel
-- conversation before this migration and is not one after. Its NULL
-- channel_provider is the accurate answer, not a gap.
--
-- The kind-narrowing slice is where whatsapp has to be decided, and it has to be
-- decided by closing or mapping the WRITERS, not by asserting an instant: rows can
-- keep arriving until they are closed.

-- The registry's own grammar. channel_provider.provider was a bare text primary
-- key, so the published ProviderRef pattern described a grammar the column did not
-- enforce. A boot reconcile inserting on behalf of a composed unit is the wrong
-- place to discover that a provider name is unusable, and the wrong place to
-- decide it: the column that owns the name owns the rule. 32 characters matches
-- the cap the extension surface already applies to IngressSource.System.
ALTER TABLE channel_provider ADD CONSTRAINT channel_provider_provider_grammar
  CHECK (provider ~ '^[a-z][a-z0-9_]*$' AND char_length(provider) <= 32);

-- channel_provider.provider REFERENCES activity_kind (kind) is the conflation in
-- schema form: it asserts that every transport is also an interaction kind, which
-- holds only while provider names live in activity.kind. An extension unit's
-- provider is a transport and names no interaction kind at all, so the FK would
-- refuse a legitimate registration. Dropping it here — one slice before the rows
-- it constrains move — is also what lets the boot reconcile stop inserting an
-- activity_kind row per provider.
ALTER TABLE channel_provider DROP CONSTRAINT channel_provider_provider_fkey;

-- No index on the column, deliberately. The send path resolves an anchor's
-- provider by primary key, and capture's reply-match still selects on
-- (thread_key, kind) — so nothing queries BY provider yet, and an index with no
-- reader is weight on every write to the busiest table in the schema. The slice
-- that moves the reply-match onto this column is the one that should add it,
-- together with the query that uses it.
