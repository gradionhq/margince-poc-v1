-- The provider registry gains the two facts `GET /v1/channel-providers` publishes
-- about a transport (ADR-0107/A158): what to CALL it, and how a connection to it
-- is credentialed.
--
-- Both are properties of the composed connector, so the boot reconcile refreshes
-- them — the same place the provider row itself is written. They are STORED
-- rather than held only in memory because the directory must answer on a role
-- that never built a capture registry: the api only constructs one when a
-- keyvault root key is configured, so an in-memory-only directory is empty on a
-- vault-less install and answers 200 with nothing. The rows seeded here are what
-- makes that answer correct without a reconcile.

-- label is what a human reads where a raw provider id would otherwise appear.
-- Defaulted to the provider name so an existing row is never label-less: an
-- unlabelled transport in a timeline is worse than an ugly one, and the reconcile
-- overwrites it with the connector's own display name on the next boot.
ALTER TABLE channel_provider ADD COLUMN label text NOT NULL DEFAULT '';
-- `_` is a word break, matching providerLabel's own rule in Go: bare initcap
-- renders `deal_room` as "Deal_Room" where the Go side gives "Deal Room", and
-- the test that holds the two spellings together would then pass only while no
-- registered provider happens to contain an underscore.
UPDATE channel_provider SET label = initcap(replace(provider, '_', ' ')) WHERE label = '';
-- initcap gets this one wrong, and the boot reconcile would correct it a moment
-- later — but a migration that knowingly writes "Whatsapp" and relies on a later
-- process to fix it is two sources of truth disagreeing on purpose. The Go side
-- (compose/channelproviderfacts.go) holds the same exception for the same
-- reason; a fitness test holds the two spellings against each other.
UPDATE channel_provider SET label = 'WhatsApp' WHERE provider = 'whatsapp';

-- credential_model says how a connection to this transport is credentialed:
-- 'workspace_bot' for one shared bot the installation binds, 'per_member' for a
-- secret each member deposits with a unit. The CHECK is the vocabulary, closed
-- because this one IS installation-independent — it describes the SHAPE of a
-- credential, not which providers exist.
ALTER TABLE channel_provider ADD COLUMN credential_model text NOT NULL DEFAULT 'workspace_bot'
  CHECK (credential_model IN ('workspace_bot', 'per_member'));

-- supplies_transport says whether this provider can carry an outbound message at
-- all. It is deliberately NOT "can this workspace send": that reads
-- channel_connection, a tenant table, and publishing the two side by side would
-- re-create the exact conflation this whole arc removes. A capture-only
-- transport reports false and stays honest about it.
--
-- whatsapp is the case in hand: 0251 registered it so a hand-logged WhatsApp
-- message can name what carried it, and no connector composes it, so it supplies
-- no transport until A103's connector lands.
ALTER TABLE channel_provider ADD COLUMN supplies_transport boolean NOT NULL DEFAULT true;
UPDATE channel_provider SET supplies_transport = false WHERE provider = 'whatsapp';
