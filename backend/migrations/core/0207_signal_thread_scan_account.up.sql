-- The account a conversation was last READ for.
--
-- signal_thread_scan is keyed on the thread alone, which was right while the
-- producer resolved a thread's account through a direct activity_link row: the
-- link was written once with the message, so a thread's account could not move
-- without the thread moving.
--
-- Resolution is now the three-arm walk (activities.OrgReachSet), and two of its
-- arms are LIVE relationships rather than facts stamped on the message: the
-- deal an activity is linked to, and the employer of the contact it is about. A
-- contact changing employer re-points every quiet thread they are on at a
-- different account, with no new mail anywhere. Keyed on the thread alone, the
-- watermark would say "already read" and the new account would never see the
-- events stated in its own correspondence.
--
-- Nullable, and no backfill: a NULL reads as "no account recorded", which the
-- due rule treats as a difference and re-reads. Existing rows are exactly the
-- rows that should be re-read, since they were scanned under the narrower walk.
--
-- SET NULL rather than CASCADE on delete, and only the account column: losing
-- the account should make the conversation due again, not erase where the
-- producer got to. The reference is composite so the database itself refuses an
-- account from another workspace.
ALTER TABLE signal_thread_scan
  ADD COLUMN resolved_org_id uuid,
  ADD CONSTRAINT signal_thread_scan_resolved_org_fkey
    FOREIGN KEY (workspace_id, resolved_org_id) REFERENCES organization (workspace_id, id)
    ON DELETE SET NULL (resolved_org_id);
