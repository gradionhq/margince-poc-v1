-- 1787048271: the person cluster drops the tenant column (ADR-0091 §8 phase D).
--
-- The contact record and the seven tables that describe one:
--
--   person                         the contact itself
--   person_email, person_phone     how to reach them
--   person_social                  where else they are
--   person_profile_field           a claimed field with its evidence
--   person_channel_identity        who they are on a chat provider
--   person_signature_enrich_state  how far the signature reader has got
--   person_moment_dismissal        a suggestion this reader has waved away
--
-- person is the most-referenced table in the schema, which is why it takes a
-- change of its own. Its keys already name something narrower than the tenant,
-- and phase B put them there: an address is unique installation-wide, a channel
-- identity is unique by (provider, channel_user_id).
--
-- uq_person_ws_id goes with the column — phase B's leftover, a second copy of
-- person's own primary key, referenced by no foreign key.

ALTER TABLE person_moment_dismissal DROP CONSTRAINT person_moment_dismissal_workspace_id_fkey;
ALTER TABLE person_moment_dismissal DROP COLUMN workspace_id;

ALTER TABLE person_signature_enrich_state DROP CONSTRAINT person_signature_enrich_state_workspace_id_fkey;
ALTER TABLE person_signature_enrich_state DROP COLUMN workspace_id;

ALTER TABLE person_channel_identity DROP CONSTRAINT person_channel_identity_workspace_id_fkey;
ALTER TABLE person_channel_identity DROP COLUMN workspace_id;

ALTER TABLE person_profile_field DROP CONSTRAINT person_profile_field_workspace_id_fkey;
ALTER TABLE person_profile_field DROP COLUMN workspace_id;

ALTER TABLE person_social DROP CONSTRAINT person_social_workspace_id_fkey;
ALTER TABLE person_social DROP COLUMN workspace_id;

ALTER TABLE person_phone DROP CONSTRAINT person_phone_workspace_id_fkey;
ALTER TABLE person_phone DROP COLUMN workspace_id;

ALTER TABLE person_email DROP CONSTRAINT person_email_workspace_id_fkey;
ALTER TABLE person_email DROP COLUMN workspace_id;

ALTER TABLE person DROP CONSTRAINT uq_person_ws_id;
ALTER TABLE person DROP CONSTRAINT person_workspace_id_fkey;
ALTER TABLE person DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects
-- rows: an owner, a person's fields, their channel identities, their dismissals.
--
-- idx_person_ws_live is NOT recreated: it was (workspace_id) WHERE archived_at
-- IS NULL, and an index on the tenant alone has no narrowed form.
CREATE INDEX idx_person_owner ON person (owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_person_profile_field ON person_profile_field (person_id);
CREATE INDEX idx_person_channel_identity_person ON person_channel_identity (person_id);
CREATE INDEX person_moment_dismissal_person_ix ON person_moment_dismissal (person_id);
