-- Consent & retention (data-model §3.4, A22/ADR-0011): per-purpose consent
-- with an append-only proof log + the retention-policy engine + legal hold.
-- requires_double_opt_in is normative in the §3.4 prose (RecordConsentRequest
-- DOI flow) though absent from its DDL block — logged as fable feedback.

CREATE TABLE consent_purpose (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  key          text NOT NULL,                       -- e.g. 'marketing_email'
  label        text NOT NULL,
  requires_double_opt_in boolean NOT NULL DEFAULT false,
  archived_at  timestamptz NULL,
  CONSTRAINT consent_purpose_key_unique UNIQUE (workspace_id, key)
);

-- Current state per person × purpose.
CREATE TABLE person_consent (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id     uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  purpose_id    uuid NOT NULL REFERENCES consent_purpose(id) ON DELETE RESTRICT,
  state         text NOT NULL DEFAULT 'unknown'
                  CHECK (state IN ('unknown','granted','withdrawn')),
  lawful_basis  text NULL,                           -- 'consent' | 'legitimate_interest' | 'contract' | …
  captured_at   timestamptz NULL,
  source        text NULL,                           -- channel: 'booking' | 'import' | 'form' | 'manual' | …
  policy_version text NULL,                          -- version of the wording shown at grant time
  CONSTRAINT person_consent_unique UNIQUE (workspace_id, person_id, purpose_id)
);

-- Proof-of-consent: append-only, with the verbatim wording shown.
CREATE TABLE consent_event (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id     uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  purpose_id    uuid NOT NULL REFERENCES consent_purpose(id) ON DELETE RESTRICT,
  new_state     text NOT NULL CHECK (new_state IN ('granted','withdrawn')),
  lawful_basis  text NULL,
  source        text NOT NULL,                        -- channel/surface that captured it
  policy_text   text NOT NULL,                        -- the verbatim wording presented
  policy_version text NOT NULL,
  double_opt_in_confirmed_at timestamptz NULL,
  captured_at   timestamptz NOT NULL,
  captured_by   text NOT NULL
);
CREATE INDEX idx_consent_event_person ON consent_event (workspace_id, person_id, captured_at DESC);

CREATE TABLE retention_policy (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_type   text NOT NULL,                        -- 'lead' | 'person' | 'activity' | 'deal' | …
  category      text NULL,                            -- optional finer scope (e.g. 'cold_lead')
  retain_days   int  NOT NULL,
  action        text NOT NULL CHECK (action IN ('archive','anonymize','erase')),
  lawful_basis  text NULL,
  enabled       boolean NOT NULL DEFAULT true,
  CONSTRAINT retention_policy_unique UNIQUE (workspace_id, object_type, category)
);

-- A row on legal hold is never auto-acted by the retention evaluator.
ALTER TABLE person       ADD COLUMN legal_hold boolean NOT NULL DEFAULT false;
ALTER TABLE organization ADD COLUMN legal_hold boolean NOT NULL DEFAULT false;
ALTER TABLE deal         ADD COLUMN legal_hold boolean NOT NULL DEFAULT false;
ALTER TABLE lead         ADD COLUMN legal_hold boolean NOT NULL DEFAULT false;
