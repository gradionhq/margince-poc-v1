-- comms_outbound is the durable record of one governed outbound message: what
-- was staged, whether it left, and why it has not. The activity row is the
-- user-visible fact; this table is the delivery machinery behind it.
--
-- message_id is the RFC822 message identity, stored UNBRACKETED because that is
-- the form mail parsing yields (capture keys activities on the same form). It is
-- also the activity's source_id, which is how the provider's own copy of this
-- message collapses onto the same activity when capture re-ingests it.
--
-- There is deliberately no 'sending' status: a crash between transmit and
-- record would strand a row in it forever, and a guard keyed on that status
-- would then turn River's redelivery into a silent skip — disabling the
-- connector's retransmission guard in exactly the crash it exists for. The
-- status set is pending | sent | parked and nothing else.
CREATE TABLE comms_outbound (
  id                  uuid PRIMARY KEY,
  workspace_id        uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  activity_id         uuid NOT NULL,
  -- CASCADE: an outbound delivery is meaningless without the activity it
  -- reports on — there is no honest state for this row to hold once its
  -- one user-visible fact is gone, so it goes with it (the
  -- activity_link_activity_id_fkey precedent, 0019). Activity rows are
  -- erasure-SCRUBBED rather than hard-deleted in the ordinary path, so this
  -- fires only for a direct DDL-level delete, never for GDPR erasure.
  user_id             uuid NOT NULL,
  -- RESTRICT: a user row with deliveries still pointing at it must not
  -- vanish silently — the durable delivery history would then attribute a
  -- send to nobody. A user leaving the workspace is deactivated, not deleted.
  provider            text NOT NULL,
  message_id          text NOT NULL,
  recipients          jsonb NOT NULL,
  cc                  jsonb NOT NULL DEFAULT '[]'::jsonb,
  subject             text NOT NULL,
  body                text NOT NULL,
  consent_purpose     text NOT NULL,
  in_reply_to         text NULL,
  references_chain    jsonb NOT NULL DEFAULT '[]'::jsonb,
  -- thread_key is the RFC822 conversation identity (the References root),
  -- carried here so the send log holds its own record of which conversation
  -- the message joined, independently of the activity this row reports on.
  -- Nothing reads it today: threading rides the message's own
  -- In-Reply-To/References headers, so the dispatcher needs none of it.
  thread_key          text NULL,
  -- list_unsubscribe alone: RFC 8058 fixes List-Unsubscribe-Post at the literal
  -- "List-Unsubscribe=One-Click", so the connector derives it whenever this is
  -- non-empty. Two columns could drift; one cannot.
  list_unsubscribe    text NULL,
  status              text NOT NULL DEFAULT 'pending',
  attempts            int  NOT NULL DEFAULT 0,
  reason              text NULL,
  provider_message_id text NULL,
  sent_at             timestamptz NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT comms_outbound_status CHECK (status IN ('pending','sent','parked')),
  -- The three jsonb columns are LISTS, and the type check says so. Without it a
  -- nil Go slice encodes as JSON null, which is a legal jsonb value: the row
  -- loads, the dispatcher decodes null into a nil slice, and a message is
  -- transmitted with no addressees rather than refused. A shape the loader
  -- cannot distinguish from an empty list belongs to the schema, not to every
  -- reader.
  CONSTRAINT comms_outbound_recipients_array CHECK (jsonb_typeof(recipients) = 'array'),
  CONSTRAINT comms_outbound_cc_array CHECK (jsonb_typeof(cc) = 'array'),
  CONSTRAINT comms_outbound_references_array CHECK (jsonb_typeof(references_chain) = 'array'),
  CONSTRAINT comms_outbound_message_unique UNIQUE (workspace_id, message_id),
  CONSTRAINT comms_outbound_activity_id_fkey FOREIGN KEY (workspace_id, activity_id)
    REFERENCES activity (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT comms_outbound_user_id_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT
);

ALTER TABLE comms_outbound ENABLE ROW LEVEL SECURITY;
ALTER TABLE comms_outbound FORCE ROW LEVEL SECURITY;

CREATE POLICY comms_outbound_tenant_isolation ON comms_outbound
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- There is deliberately no due-index: River's job row owns the schedule, and
-- an index describing a sweep that does not exist is decoration.
