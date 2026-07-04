-- Pipelines, stages, deals, stage history, FX (data-model §6).

CREATE TABLE pipeline (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name         text NOT NULL,
  is_default   boolean NOT NULL DEFAULT false, -- exactly one seeded default per workspace
  position     integer NOT NULL DEFAULT 0,
  version      bigint NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL,
  CONSTRAINT pipeline_name_unique UNIQUE (workspace_id, name)
);
CREATE UNIQUE INDEX uq_pipeline_default ON pipeline (workspace_id) WHERE is_default AND archived_at IS NULL;
CREATE TRIGGER trg_pipeline_updated BEFORE UPDATE ON pipeline
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE stage (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  pipeline_id     uuid NOT NULL REFERENCES pipeline(id) ON DELETE CASCADE,
  name            text NOT NULL,
  position        integer NOT NULL,            -- unique within pipeline
  semantic        text NOT NULL DEFAULT 'open' CHECK (semantic IN ('open','won','lost')),
  win_probability smallint NOT NULL DEFAULT 0 CHECK (win_probability BETWEEN 0 AND 100),
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  archived_at     timestamptz NULL,

  -- terminal-stage probability rule: won=100, lost=0
  CONSTRAINT stage_terminal_prob CHECK (
    (semantic = 'won'  AND win_probability = 100) OR
    (semantic = 'lost' AND win_probability = 0)   OR
    (semantic = 'open')
  ),
  -- target of the deal(stage_id, pipeline_id) composite FK below (OQ-5:
  -- "the stage belongs to its pipeline" is DB-guaranteed, no trigger)
  CONSTRAINT stage_id_pipeline_unique UNIQUE (id, pipeline_id)
);
CREATE UNIQUE INDEX uq_stage_position ON stage (pipeline_id, position) WHERE archived_at IS NULL;
CREATE INDEX idx_stage_pipeline ON stage (pipeline_id) WHERE archived_at IS NULL;
CREATE TRIGGER trg_stage_updated BEFORE UPDATE ON stage
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE deal (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name            text NOT NULL,

  -- money (§1.4): minor units + ISO-4217, never floats
  amount_minor    bigint NULL,
  currency        char(3) NULL CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),

  -- FX freeze for base-currency roll-ups: frozen at close; daily fx_rate while open
  fx_rate_to_base numeric(20,10) NULL,
  fx_rate_date    date NULL,

  pipeline_id     uuid NOT NULL REFERENCES pipeline(id) ON DELETE RESTRICT,
  stage_id        uuid NOT NULL REFERENCES stage(id)    ON DELETE RESTRICT,
  organization_id uuid NULL REFERENCES organization(id) ON DELETE SET NULL, -- never a raw lead (ADR-0008 §5)
  owner_id        uuid NULL REFERENCES app_user(id)     ON DELETE SET NULL,
  partner_org_id  uuid NULL REFERENCES organization(id) ON DELETE SET NULL, -- deal registration / referral attribution (A41)

  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open','won','lost')),
  lost_reason     text NULL,
  expected_close_date date NULL,
  closed_at       timestamptz NULL,

  forecast_category text NULL CHECK (forecast_category IS NULL OR forecast_category IN ('commit','best_case','pipeline','omitted')),

  wait_until      date NULL,              -- suppresses the stalled flag, NOT the overdue close-date flag
  last_activity_at timestamptz NULL,      -- drives the deterministic stalled/idle flag

  source          text NOT NULL,
  captured_by     text NOT NULL,
  raw             jsonb NULL,

  search_tsv      tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(name,''))) STORED,

  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  archived_at     timestamptz NULL,

  CONSTRAINT deal_lost_reason CHECK (status <> 'lost' OR lost_reason IS NOT NULL),
  CONSTRAINT deal_closed_at   CHECK (status = 'open' OR closed_at IS NOT NULL),
  -- closed deals need a frozen FX rate so base-currency roll-ups reproduce
  CONSTRAINT deal_closed_fx   CHECK (status = 'open' OR amount_minor IS NULL OR fx_rate_to_base IS NOT NULL),
  -- "the stage belongs to its pipeline", DB-guaranteed (OQ-5 composite FK)
  CONSTRAINT deal_stage_in_pipeline FOREIGN KEY (stage_id, pipeline_id)
    REFERENCES stage (id, pipeline_id)
);

CREATE INDEX idx_deal_ws_live  ON deal (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_deal_stage    ON deal (stage_id) WHERE archived_at IS NULL;        -- Kanban column read
CREATE INDEX idx_deal_pipeline ON deal (pipeline_id, stage_id) WHERE archived_at IS NULL;
CREATE INDEX idx_deal_owner    ON deal (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_deal_org      ON deal (organization_id) WHERE organization_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_deal_partner  ON deal (workspace_id, partner_org_id) WHERE partner_org_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_deal_stalled  ON deal (workspace_id, last_activity_at) WHERE status = 'open' AND archived_at IS NULL;
CREATE INDEX idx_deal_close    ON deal (workspace_id, expected_close_date) WHERE status = 'open' AND archived_at IS NULL;
CREATE INDEX idx_deal_search   ON deal USING gin (search_tsv);
CREATE TRIGGER trg_deal_updated BEFORE UPDATE ON deal
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- Append-only stage-change snapshot: "pipeline as of date X" + conversion reports.
CREATE TABLE deal_stage_history (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  deal_id       uuid NOT NULL REFERENCES deal(id) ON DELETE CASCADE,
  from_stage_id uuid NULL REFERENCES stage(id) ON DELETE SET NULL,
  to_stage_id   uuid NOT NULL REFERENCES stage(id) ON DELETE RESTRICT,
  changed_by    text NOT NULL,                 -- principal string (human:/agent:)
  changed_at    timestamptz NOT NULL DEFAULT now(),
  amount_minor_at_change bigint NULL,
  currency_at_change     char(3) NULL
);
CREATE INDEX idx_dsh_deal    ON deal_stage_history (deal_id, changed_at);
CREATE INDEX idx_dsh_ws_time ON deal_stage_history (workspace_id, changed_at);

CREATE TABLE fx_rate (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  from_currency char(3) NOT NULL,
  to_currency   char(3) NOT NULL,              -- = workspace.base_currency
  rate          numeric(20,10) NOT NULL,
  rate_date     date NOT NULL,                 -- one rate per pair per UTC day
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT fx_rate_pair_day UNIQUE (workspace_id, from_currency, to_currency, rate_date)
);
CREATE INDEX idx_fx_rate_lookup ON fx_rate (workspace_id, from_currency, to_currency, rate_date);
