-- The admin trace walks terminal calls newest-first. Keeping retry attempts out
-- of the index makes paging cost follow completed logical calls, not retries.
CREATE INDEX ai_call_terminal_trace_idx
  ON ai_call (workspace_id, occurred_at DESC, id DESC)
  WHERE is_terminal;
