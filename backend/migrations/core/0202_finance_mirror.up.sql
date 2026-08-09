-- Finance ingestion (ADR-0083/A128, finance-ingestion.md FIN-DDL-1..5): a
-- bounded READ-ONLY mirror of an accounting source, so the company page can
-- answer "does this customer actually pay us, and on time?".
--
-- The read-only posture is expressed as the ABSENCE of a create/update grant
-- on the permission surface (FIN-DDL-N-1), not as a runtime refusal. There is
-- no code path to reach and no flag to set wrongly; a future contributor who
-- wants to write an invoice must add an action, an ADR and a permission.
--
-- Every amount is an integer minor-unit value with an explicit currency
-- (FIN-AC-5). No amount is a floating-point type anywhere in this schema.

-- FIN-DDL-1: one configured accounting source.
CREATE TABLE finance_connection (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id      uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  provider          text NOT NULL,
  status            text NOT NULL DEFAULT 'connecting'
                      CHECK (status IN ('connecting','active','error','disconnected')),
  capabilities      jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- A vault handle, never a secret (FIN-PARAM-9/FIN-AC-12). The credential
  -- itself appears in no row, log line, audit payload, API response or model
  -- prompt.
  credential_ref    text NOT NULL,
  -- NULL means the backfill has not completed; a resumed run continues from
  -- this checkpoint under the same run identity (FIN-AC-16).
  sync_cursor       text NULL,
  last_attempt_at   timestamptz NULL,
  last_success_at   timestamptz NULL,
  last_error_code   text NULL,
  source            text NOT NULL,
  captured_by       text NOT NULL,
  version           bigint NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  archived_at       timestamptz NULL
);

-- FIN-DDL-5: the source's own customer directory, mirrored.
--
-- Without it there is nothing to link FROM, and the unmapped state FIN-AC-7
-- requires could not be rendered — a candidate list cannot be drawn from a
-- table of decisions already made. A row here carries no money and is not
-- itself a finance record.
CREATE TABLE finance_external_customer (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id         uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connection_id        uuid NOT NULL,
  external_customer_id text NOT NULL,
  display_name         text NOT NULL,
  source_updated_at    timestamptz NULL,
  sync_hash            text NOT NULL,
  source               text NOT NULL,
  captured_by          text NOT NULL,
  version              bigint NOT NULL DEFAULT 1,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  archived_at          timestamptz NULL,
  UNIQUE (workspace_id, connection_id, external_customer_id)
);

-- FIN-DDL-2: the deliberate mapping between an accounting customer and an
-- organization. Never a merge, never automatic: an accounting customer does
-- not become an organization, and an unmapped one is a visible state rather
-- than an auto-created company (FIN-AC-7).
CREATE TABLE finance_customer_link (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id         uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connection_id        uuid NOT NULL,
  organization_id      uuid NOT NULL,
  external_customer_id text NOT NULL,
  source_updated_at    timestamptz NULL,
  sync_hash            text NOT NULL,
  source               text NOT NULL,
  captured_by          text NOT NULL,
  version              bigint NOT NULL DEFAULT 1,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  archived_at          timestamptz NULL,
  -- One link each way per connection: an accounting customer maps to at most
  -- one company, and a company to at most one accounting customer.
  UNIQUE (workspace_id, connection_id, external_customer_id),
  UNIQUE (workspace_id, connection_id, organization_id)
);

-- FIN-DDL-3: a mirrored issued invoice. `void_at` is FIN-FORM-6's tombstone —
-- a row is never deleted, so a record the source stops returning stays
-- readable as voided (FIN-AC-8).
CREATE TABLE finance_invoice (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id      uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connection_id     uuid NOT NULL,
  organization_id   uuid NOT NULL,
  external_id       text NOT NULL,
  number            text NULL,
  issued_at         date NOT NULL,
  due_at            date NULL,
  status            text NOT NULL
                      CHECK (status IN ('draft','open','partially_paid','paid',
                                        'overdue','disputed','credited','void')),
  -- The provider's own word, kept for diagnosis: our vocabulary above is a
  -- normalization, and a mapping argument is unanswerable without the input.
  raw_status        text NULL,
  currency          char(3) NOT NULL,
  net_minor         bigint NOT NULL,
  tax_minor         bigint NOT NULL DEFAULT 0,
  gross_minor       bigint NOT NULL,
  open_minor        bigint NOT NULL DEFAULT 0,
  credited_minor    bigint NOT NULL DEFAULT 0,
  -- Frozen on ISSUE date (FIN-PARAM-7/DM-FX-4). A missing rate is the existing
  -- refusal, never a silent conversion and never a zero (FIN-AC-6).
  fx_rate_to_base   numeric(20,10) NULL,
  fx_rate_date      date NULL,
  fully_paid_at     timestamptz NULL,
  disputed_at       timestamptz NULL,
  void_at           timestamptz NULL,
  -- A credit note is an invoice row pointing at what it credits (FIN-DDL-N-3),
  -- not a fifth table: one money table, one natural key, one upsert path.
  credits_invoice_id uuid NULL,
  source_updated_at timestamptz NULL,
  -- The no-op detector (FIN-AC-9): an unchanged content hash writes no row, no
  -- audit entry, no event and no version bump.
  sync_hash         text NOT NULL,
  source            text NOT NULL,
  captured_by       text NOT NULL,
  version           bigint NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  archived_at       timestamptz NULL,
  UNIQUE (workspace_id, connection_id, external_id),
  CONSTRAINT finance_invoice_paid_status
    CHECK (fully_paid_at IS NULL OR status IN ('paid','credited','void'))
);

-- The company page's access path: this account's invoices, newest first.
CREATE INDEX finance_invoice_account_ix
  ON finance_invoice (workspace_id, organization_id, issued_at DESC);

-- The open-balance path, which is the figure the page leads with.
CREATE INDEX finance_invoice_open_ix
  ON finance_invoice (workspace_id, organization_id)
  WHERE open_minor > 0 AND void_at IS NULL;

-- The credit-note pointer's own access path. Postgres indexes the referenced
-- side for free and the referencing side never, so without this "which credit
-- notes reduced this invoice?" scans every invoice in the workspace.
CREATE INDEX finance_invoice_credits_ix
  ON finance_invoice (workspace_id, credits_invoice_id)
  WHERE credits_invoice_id IS NOT NULL;

-- FIN-DDL-4: a mirrored payment, optionally against one invoice.
CREATE TABLE finance_payment (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id      uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connection_id     uuid NOT NULL,
  organization_id   uuid NOT NULL,
  external_id       text NOT NULL,
  invoice_id        uuid NULL,
  paid_at           timestamptz NOT NULL,
  currency          char(3) NOT NULL,
  amount_minor      bigint NOT NULL,
  source_updated_at timestamptz NULL,
  sync_hash         text NOT NULL,
  source            text NOT NULL,
  captured_by       text NOT NULL,
  version           bigint NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  archived_at       timestamptz NULL,
  UNIQUE (workspace_id, connection_id, external_id)
);

-- The invoice FK's referencing side, for "what paid this invoice?".
CREATE INDEX finance_payment_invoice_ix
  ON finance_payment (workspace_id, invoice_id)
  WHERE invoice_id IS NOT NULL;

-- The account's payment history, which FIN-FORM-3..5 read to answer whether a
-- customer settles early or late.
CREATE INDEX finance_payment_account_ix
  ON finance_payment (workspace_id, organization_id, paid_at DESC);

-- Composite (workspace_id, id) uniqueness, so every reference below can name
-- the workspace as part of its target. A plain (id) foreign key is checked as
-- the table owner and so bypasses RLS: it would accept a well-formed id
-- belonging to ANOTHER workspace and persist a cross-tenant reference the
-- application layer never sees. The composite form makes the database reject
-- it (0019 did the same sweep for the core tables).
ALTER TABLE finance_connection
  ADD CONSTRAINT uq_finance_connection_ws_id UNIQUE (workspace_id, id);
ALTER TABLE finance_invoice
  ADD CONSTRAINT uq_finance_invoice_ws_id UNIQUE (workspace_id, id);

ALTER TABLE finance_external_customer
  ADD CONSTRAINT finance_external_customer_connection_fk
  FOREIGN KEY (workspace_id, connection_id)
  REFERENCES finance_connection (workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_customer_link
  ADD CONSTRAINT finance_customer_link_connection_fk
  FOREIGN KEY (workspace_id, connection_id)
  REFERENCES finance_connection (workspace_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT finance_customer_link_organization_fk
  FOREIGN KEY (workspace_id, organization_id)
  REFERENCES organization (workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice
  ADD CONSTRAINT finance_invoice_connection_fk
  FOREIGN KEY (workspace_id, connection_id)
  REFERENCES finance_connection (workspace_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT finance_invoice_organization_fk
  FOREIGN KEY (workspace_id, organization_id)
  REFERENCES organization (workspace_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT finance_invoice_credits_fk
  FOREIGN KEY (workspace_id, credits_invoice_id)
  REFERENCES finance_invoice (workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_payment
  ADD CONSTRAINT finance_payment_connection_fk
  FOREIGN KEY (workspace_id, connection_id)
  REFERENCES finance_connection (workspace_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT finance_payment_organization_fk
  FOREIGN KEY (workspace_id, organization_id)
  REFERENCES organization (workspace_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT finance_payment_invoice_fk
  FOREIGN KEY (workspace_id, invoice_id)
  REFERENCES finance_invoice (workspace_id, id) ON DELETE RESTRICT;

-- Tenant isolation (FIN-AC-13): every finance table is workspace-scoped with
-- row-level security enabled AND forced, so the owner role migrations run as
-- is bound by it too. Unbound, the policy resolves to NULL and denies.
ALTER TABLE finance_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE finance_connection FORCE ROW LEVEL SECURITY;
CREATE POLICY finance_connection_ws ON finance_connection
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE finance_external_customer ENABLE ROW LEVEL SECURITY;
ALTER TABLE finance_external_customer FORCE ROW LEVEL SECURITY;
CREATE POLICY finance_external_customer_ws ON finance_external_customer
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE finance_customer_link ENABLE ROW LEVEL SECURITY;
ALTER TABLE finance_customer_link FORCE ROW LEVEL SECURITY;
CREATE POLICY finance_customer_link_ws ON finance_customer_link
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE finance_invoice ENABLE ROW LEVEL SECURITY;
ALTER TABLE finance_invoice FORCE ROW LEVEL SECURITY;
CREATE POLICY finance_invoice_ws ON finance_invoice
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE finance_payment ENABLE ROW LEVEL SECURITY;
ALTER TABLE finance_payment FORCE ROW LEVEL SECURITY;
CREATE POLICY finance_payment_ws ON finance_payment
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
