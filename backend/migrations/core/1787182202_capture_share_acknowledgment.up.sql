-- The mail-sharing acknowledgment: connecting a mailbox shares its captured
-- correspondence with every colleague who can see the contact, and that
-- default is a stated choice, not a surprise. The connect flow refuses
-- without the acknowledgment; the grant stamps it here before the first pull.
SET LOCAL lock_timeout = '3s';

ALTER TABLE capture_connection ADD COLUMN share_acknowledged_at timestamptz NULL;

-- Connections that predate the acknowledgment keep capturing: their humans
-- connected under the rules of their day, and parking live mailboxes until
-- each owner returns to re-consent would silently stop capture workspace-wide.
-- The stamp records when the sharing default became binding for the row.
UPDATE capture_connection SET share_acknowledged_at = now();
