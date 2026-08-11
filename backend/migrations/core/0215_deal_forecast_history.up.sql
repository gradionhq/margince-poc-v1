-- Append-only snapshot of the forecast fields a deal carries, written when one
-- of them moves WITHOUT a stage change: an amount revised in place, or a close
-- date slipped. Those two are invisible today — deal_stage_history is written
-- only on creation and on a move — so a forecast reconstructed from history
-- reconciles over stage movement and silently omits the rest, while presenting
-- itself as the whole answer.
--
-- WHY A SECOND TABLE, rather than an arm on deal_stage_history. That table's
-- own comment is "append-only stage-change snapshot", and five readers hold it
-- to that: deals/health.go takes max(changed_at) as "when did this deal last
-- move", automation previews COUNT rows as stage movements, and org360's
-- baseline and the close-date sweep read it the same way. A row that is not a
-- transition would make every edited deal look freshly moved and inflate every
-- movement count — a silent corruption of stalled-deal detection, which is
-- exactly the kind of answer nobody re-derives once it looks plausible. Two
-- tables keep both meanings intact by construction rather than by every present
-- and future reader remembering to filter.
--
-- SNAPSHOT, not diff, mirroring the sibling: each row freezes what the deal
-- read at that instant, and a reconstruction diffs consecutive rows. Amount and
-- currency are frozen for the reason deal_stage_history freezes them — a deal's
-- current amount cannot answer what the forecast SAID last month.
--
-- No birth row here: every deal already gets one in deal_stage_history at
-- creation, carrying amount and currency, so the baseline exists and a
-- reconstruction unions the two tables in time order.
CREATE TABLE deal_forecast_history (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  deal_id      uuid NOT NULL,
  changed_by   text NOT NULL,                 -- principal string (human:/agent:)
  changed_at   timestamptz NOT NULL DEFAULT now(),
  amount_minor_at_change bigint NULL,
  currency_at_change     char(3) NULL,
  -- Nullable because the column it snapshots is: a deal with no close date and
  -- a deal whose close date was just cleared are different states, and the
  -- clearing is itself a forecast movement worth a row.
  close_date_at_change   date NULL,
  -- Composite so the database itself refuses a deal from another workspace,
  -- matching what 0019 did for deal_stage_history.
  CONSTRAINT deal_forecast_history_deal_id_fkey
    FOREIGN KEY (workspace_id, deal_id) REFERENCES deal (workspace_id, id) ON DELETE CASCADE
);

-- The read this table exists for: one deal's forecast movements in time order.
CREATE INDEX idx_deal_forecast_history_deal
  ON deal_forecast_history (workspace_id, deal_id, changed_at);

-- Tenant table ⇒ RLS with the deny-on-unset policy every other one carries
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE deal_forecast_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE deal_forecast_history FORCE ROW LEVEL SECURITY;
CREATE POLICY deal_forecast_history_tenant_isolation ON deal_forecast_history
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
