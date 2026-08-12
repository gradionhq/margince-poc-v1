-- Shared provider-integration platform (ADR-0101/A152, provider-integrations.md
-- PI-DDL-1/PI-DDL-2): one installation-owned connection per licensed data
-- provider, a metered run ledger, and the per-pool credit reservations that
-- keep a paid call from ever exceeding what the customer authorized.
--
-- These four tables carry NO workspace_id and NO row-level security, and that
-- is deliberate (ADR-0091): one installation serves one organization (A107),
-- the connection is a property OF the installation rather than of a tenant
-- inside it, and there is no second workspace for a policy to isolate it from.
-- What protects them is the application authority gate on the `integrations`
-- RBAC object, plus the owning domain's own gate on any subject they name.
--
-- person_provider_claim is the exception in ownership, not in posture: it is
-- owned by modules/people (the domain decides what a claim MEANS), and it
-- likewise carries no workspace_id, matching the spec DDL.

-- PI-DDL-1: the connection. One row per provider, credential sealed elsewhere.
CREATE TABLE provider_connection (
  id                              uuid PRIMARY KEY DEFAULT uuidv7(),
  provider                        text NOT NULL UNIQUE CHECK (provider IN ('surfe')),
  status                          text NOT NULL CHECK (status IN
                                    ('disconnected','validating','connected','invalid_credentials',
                                     'insufficient_credits','rate_limited','provider_error')),
  mode                            text NOT NULL CHECK (mode IN ('automatic_on_create','on_demand')),
  preset                          text NOT NULL,
  automatic_individual_create     boolean NOT NULL DEFAULT true,
  automatic_import                boolean NOT NULL DEFAULT false,
  categories                      text[] NOT NULL CHECK (cardinality(categories) > 0),
  refresh_after_days              integer NULL CHECK (refresh_after_days > 0),
  daily_run_limit                 integer NULL CHECK (daily_run_limit > 0),
  -- An opaque handle into the keyvault, never secret material. Not a foreign
  -- key: vault_secret is keyed per workspace and this row has no workspace.
  credential_ref                  text NULL UNIQUE,
  -- The revocation epoch. Disconnect and key rotation bump it; a run carries
  -- the epoch it was admitted under and abandons if the two ever differ. This
  -- is what makes "disconnect stops new egress" (PI-AC-5) true across a
  -- process boundary, where a transaction-scoped lock cannot reach: the
  -- provider call happens outside any transaction by design.
  execution_epoch                 bigint NOT NULL DEFAULT 1,
  connected_by                    uuid NULL REFERENCES app_user(id),
  connected_at                    timestamptz NULL,
  last_verified_at                timestamptz NULL,
  last_used_at                    timestamptz NULL,
  -- A closed product reason, never a provider body or credential fragment.
  last_safe_status_code           text NULL,
  version                         bigint NOT NULL DEFAULT 1,
  created_at                      timestamptz NOT NULL DEFAULT now(),
  updated_at                      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT provider_connection_disconnected_shape
    CHECK ((status = 'disconnected' AND credential_ref IS NULL) OR status <> 'disconnected')
);

-- One budget row per credit pool. A provider's pools are its own vocabulary,
-- so they are rows and not columns.
CREATE TABLE provider_connection_budget (
  connection_id       uuid NOT NULL REFERENCES provider_connection(id) ON DELETE CASCADE,
  pool                text NOT NULL,
  monthly_ceiling     integer NULL CHECK (monthly_ceiling >= 0),
  pause_below_balance integer NULL CHECK (pause_below_balance >= 0),
  -- The provider's balance as last observed. Cached because reading it is an
  -- outbound call and the admission check runs inside the transaction that
  -- locks these rows; egress there would hold the locks for the length of
  -- somebody else's network. Unknown (NULL) never blocks work.
  last_known_balance  integer NULL CHECK (last_known_balance >= 0),
  balance_read_at     timestamptz NULL,
  PRIMARY KEY (connection_id, pool),
  CONSTRAINT provider_connection_budget_balance_shape
    CHECK ((last_known_balance IS NULL) = (balance_read_at IS NULL))
);

-- PI-DDL-2: the run ledger. Every paid call is one row, before it is made.
CREATE TABLE provider_run (
  id                         uuid PRIMARY KEY DEFAULT uuidv7(),
  subject_kind               text NOT NULL CHECK (subject_kind IN ('person')),
  -- Typed per subject kind, not a polymorphic id: the ON DELETE clause is the
  -- privileged-erasure backstop (DM-CONV-15) and a bare uuid would have none.
  person_id                  uuid NULL REFERENCES person(id) ON DELETE CASCADE,
  provider                   text NOT NULL CHECK (provider IN ('surfe')),
  trigger                    text NOT NULL CHECK (trigger IN
                               ('automatic_create','automatic_import','scheduled_refresh','manual')),
  state                      text NOT NULL CHECK (state IN
                               ('queued','submitting','in_progress','completed','no_match','skipped',
                                'submission_unknown','failed','cancelled')),
  skip_reason                text NULL CHECK (skip_reason IS NULL OR skip_reason IN
                               ('budget_exhausted','low_balance','suppressed','not_eligible',
                                'duplicate_subject_candidate','rate_limited','already_fresh')),
  -- Paid, but the values never reached the domain. The presentation layer
  -- shows this as its own state rather than as a success.
  claims_unwritten           boolean NOT NULL DEFAULT false,
  input_fingerprint          text NOT NULL,
  external_correlation_id    uuid NOT NULL UNIQUE,
  provider_job_id            text NULL,
  connection_version         bigint NOT NULL,
  -- The epoch this run was admitted under; compared against the connection's
  -- current epoch before egress and before any terminal write.
  connection_epoch           bigint NOT NULL,
  configuration_snapshot     jsonb NOT NULL,
  requested_categories       text[] NOT NULL,
  requested_by               uuid NULL REFERENCES app_user(id),
  attempt_count              integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at            timestamptz NULL,
  -- Stamped before egress, cleared ONLY by a definite answer proving the
  -- request did not land. A timeout, a dropped connection or a dead worker
  -- leaves it standing, because "we do not know" is the fact it carries.
  inflight_at                timestamptz NULL,
  last_safe_status_code      text NULL,
  submitted_at               timestamptz NULL,
  completed_at               timestamptz NULL,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT provider_run_subject_shape CHECK (
    (subject_kind = 'person' AND person_id IS NOT NULL)
  ),
  CONSTRAINT provider_run_skip_reason_shape CHECK (
    (state = 'skipped') = (skip_reason IS NOT NULL)
  ),
  CONSTRAINT provider_run_claims_unwritten_shape CHECK (
    NOT claims_unwritten OR state = 'completed'
  )
);

-- One live run per subject per fingerprint. submission_unknown is IN this
-- predicate although it is terminal: a run that may have been paid for must
-- keep blocking an identical retry until a human resolves it.
CREATE UNIQUE INDEX provider_run_one_live_person_fingerprint
  ON provider_run (person_id, provider, input_fingerprint)
  WHERE subject_kind = 'person'
    AND state IN ('queued','submitting','in_progress','submission_unknown');

CREATE INDEX provider_run_person_history
  ON provider_run (person_id, provider, created_at DESC)
  WHERE subject_kind = 'person';

-- The sweep's due-scan: in-flight polls, pending claim hand-offs, stranded
-- submissions and expired in-flight markers all resolve through this.
CREATE INDEX provider_run_due
  ON provider_run (state, next_attempt_at)
  WHERE state IN ('queued','submitting','in_progress','completed');

CREATE TABLE provider_run_reservation (
  run_id           uuid NOT NULL REFERENCES provider_run(id) ON DELETE CASCADE,
  pool             text NOT NULL,
  reserved_credits integer NOT NULL CHECK (reserved_credits >= 0),
  -- NULL until the provider says what it actually charged. On
  -- submission_unknown it stays NULL and the full reservation stays held.
  actual_credits   integer NULL CHECK (actual_credits >= 0),
  reserved_at      timestamptz NOT NULL DEFAULT now(),
  reconciled_at    timestamptz NULL,
  PRIMARY KEY (run_id, pool)
);

-- Owned by modules/people: the domain decides what a claim means and how it
-- renders. Keyed by (run, claim_key) so two runs' claims for one person
-- coexist as peer assertions — each was bought separately, and a merge must
-- not drop either (PI-AC-11).
CREATE TABLE person_provider_claim (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  person_id          uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  run_id             uuid NOT NULL REFERENCES provider_run(id) ON DELETE CASCADE,
  provider           text NOT NULL CHECK (provider IN ('surfe')),
  claim_key          text NOT NULL CHECK (claim_key IN
                       ('professional_emails','personal_emails','mobile_phones','linkedin_profile',
                        'current_employment','job_history','location','departments','seniorities')),
  value_json         jsonb NOT NULL,
  confidence         numeric(5,4) NULL CHECK (confidence BETWEEN 0 AND 1),
  validation_status  text NULL,
  source             text NOT NULL DEFAULT 'surfe',
  captured_by        text NOT NULL DEFAULT 'connector:surfe',
  retrieved_at       timestamptz NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, claim_key)
);

CREATE INDEX person_provider_claim_latest
  ON person_provider_claim (person_id, provider, retrieved_at DESC);

-- The connection carries a version the PATCH surface reads for optimistic
-- concurrency, so its trigger bumps that too; the run has no version.
CREATE TRIGGER trg_provider_connection_updated
  BEFORE UPDATE ON provider_connection
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_provider_run_updated
  BEFORE UPDATE ON provider_run
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
