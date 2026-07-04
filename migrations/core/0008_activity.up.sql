-- Activity timeline (data-model §7): single polymorphic activity table +
-- activity_link join so one activity ties to many entities (OQ-6 default).

CREATE TABLE activity (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  kind          text NOT NULL CHECK (kind IN ('email','call','meeting','note','task','whatsapp','telegram')),

  subject       text NULL,
  body          text NULL,          -- normalized text (email body, note text, meeting notes)
  occurred_at   timestamptz NOT NULL DEFAULT now(),

  -- task-specific (nullable unless kind='task')
  due_at        timestamptz NULL,
  assignee_id   uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  is_done       boolean NOT NULL DEFAULT false,
  done_at       timestamptz NULL,

  -- meeting/call-specific
  duration_seconds integer NULL,
  direction     text NULL CHECK (direction IS NULL OR direction IN ('inbound','outbound')),
  meeting_status text NULL CHECK (meeting_status IS NULL OR meeting_status IN ('booked','held','no_show','canceled')),

  -- idempotent capture key: re-running capture makes no dupes
  source_system text NULL,          -- 'gmail','gcal','outlook','transcript',…
  source_id     text NULL,          -- provider message/event id

  source        text NOT NULL,
  captured_by   text NOT NULL,
  raw           jsonb NULL,         -- re-parseable original; OFF the hot path (§1.6)

  search_tsv    tsvector GENERATED ALWAYS AS (
                  to_tsvector('simple', coalesce(subject,'') || ' ' || coalesce(body,''))
                ) STORED,

  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT activity_task_fields CHECK (kind = 'task' OR (due_at IS NULL AND assignee_id IS NULL AND is_done = false)),
  CONSTRAINT activity_done_at CHECK (is_done = false OR done_at IS NOT NULL)
);
CREATE TRIGGER trg_activity_updated BEFORE UPDATE ON activity
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- idempotency: the same provider record never creates two activities
CREATE UNIQUE INDEX uq_activity_source
  ON activity (workspace_id, source_system, source_id)
  WHERE source_system IS NOT NULL AND source_id IS NOT NULL;

CREATE INDEX idx_activity_ws_time   ON activity (workspace_id, occurred_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_activity_kind      ON activity (workspace_id, kind, occurred_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_activity_tasks     ON activity (workspace_id, assignee_id, due_at) WHERE kind = 'task' AND is_done = false AND archived_at IS NULL;
CREATE INDEX idx_activity_direction ON activity (workspace_id, direction, occurred_at DESC) WHERE direction IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_activity_search    ON activity USING gin (search_tsv);

-- polymorphic links: one activity ↔ many of {person, organization, deal}
CREATE TABLE activity_link (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  activity_id   uuid NOT NULL REFERENCES activity(id) ON DELETE CASCADE,
  entity_type   text NOT NULL CHECK (entity_type IN ('person','organization','deal')),
  person_id       uuid NULL REFERENCES person(id) ON DELETE CASCADE,
  organization_id uuid NULL REFERENCES organization(id) ON DELETE CASCADE,
  deal_id         uuid NULL REFERENCES deal(id) ON DELETE CASCADE,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT activity_link_shape CHECK (
    (entity_type='person'       AND person_id IS NOT NULL AND organization_id IS NULL AND deal_id IS NULL) OR
    (entity_type='organization' AND organization_id IS NOT NULL AND person_id IS NULL AND deal_id IS NULL) OR
    (entity_type='deal'         AND deal_id IS NOT NULL AND person_id IS NULL AND organization_id IS NULL)
  )
);
CREATE UNIQUE INDEX uq_activity_link ON activity_link (activity_id, entity_type, coalesce(person_id,organization_id,deal_id));
CREATE INDEX idx_alink_person ON activity_link (person_id) WHERE person_id IS NOT NULL;
CREATE INDEX idx_alink_org    ON activity_link (organization_id) WHERE organization_id IS NOT NULL;
CREATE INDEX idx_alink_deal   ON activity_link (deal_id) WHERE deal_id IS NOT NULL;
