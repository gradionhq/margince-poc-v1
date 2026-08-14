-- The activity-kind and channel-provider vocabularies stop being hand-maintained
-- CHECK constraints and become derived registries (DESIGN-SP4 §3-§4): a row here
-- says "this installation actually has a writer for this kind," checked by the
-- database via FK rather than remembered by three separate call sites.
--
-- Both tables are installation-global, not tenant tables (A107/ADR-0061: one
-- installation, one organization) — no workspace_id, no RLS, the same posture
-- event_outbox already has as the one other table with neither.

CREATE TABLE activity_kind (
  kind text PRIMARY KEY
);

-- Seeded with every kind that has a real writer TODAY (the five fixed kinds) or
-- is reserved in the OpenAPI contract's kind enum with no writer yet (whatsapp —
-- backend/api/crm.yaml carries it in four places; dropping the row would make a
-- contract-valid request fail an FK it has no way to know about). telegram is
-- seeded here too because it is also a channel_provider row below.
INSERT INTO activity_kind (kind) VALUES
  ('email'), ('call'), ('meeting'), ('note'), ('task'), ('whatsapp'), ('telegram');

CREATE TABLE channel_provider (
  provider  text PRIMARY KEY REFERENCES activity_kind (kind),
  -- 'core' for a connector compiled into this binary, 'unit' for an extension's
  -- declared channel. Not read by anything yet — a future unit-surface slice
  -- needs it for a boot collision refusal, and adding the column now avoids a
  -- second migration when that lands.
  transport text NOT NULL CHECK (transport IN ('core', 'unit'))
);

INSERT INTO channel_provider (provider, transport) VALUES ('telegram', 'core');

-- The FK swap. Both CHECKs were unnamed inline constraints, so Postgres named
-- them <table>_<column>_check by its own default convention.
ALTER TABLE activity DROP CONSTRAINT activity_kind_check;
ALTER TABLE activity ADD CONSTRAINT activity_kind_fkey
  FOREIGN KEY (kind) REFERENCES activity_kind (kind);

ALTER TABLE person_channel_identity DROP CONSTRAINT person_channel_identity_provider_check;
ALTER TABLE person_channel_identity ADD CONSTRAINT person_channel_identity_provider_fkey
  FOREIGN KEY (provider) REFERENCES channel_provider (provider);

-- channel_connection.provider's CHECK is DELIBERATELY untouched: with
-- per-member credentials for a unit-supplied channel, a unit never writes this
-- table, so widening it would admit a workspace-bot binding for a provider
-- whose credential model has no bot.
