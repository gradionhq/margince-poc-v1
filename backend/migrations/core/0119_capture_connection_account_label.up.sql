-- Display-only name of the connected account (the mailbox address), so a user
-- can confirm they authorized the account they intended. Nullable: a connector
-- that does not report one leaves it unset, and rows predating this column stay
-- valid. Nothing routes or authorizes on it.
ALTER TABLE capture_connection ADD COLUMN account_label text;
