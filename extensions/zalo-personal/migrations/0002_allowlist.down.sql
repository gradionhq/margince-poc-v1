-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its index, its policy, its grants and its foreign key
-- with it, and dropping the column takes its CHECK: which is why nothing else is
-- spelled here. A down migration that undid each piece separately would be
-- several statements that can disagree with the up migration rather than one
-- that cannot.
--
-- The mode columns go too, and their CHECK constraints go with them. Reverting them
-- leaves an installation with verdict rows and no reading of them, which is why the
-- table goes in the same breath.

ALTER TABLE ext.ext_zalo_personal_connection
    DROP CONSTRAINT IF EXISTS ext_zalo_personal_connection_armed_has_a_mode,
    DROP COLUMN IF EXISTS capture_mode,
    DROP COLUMN IF EXISTS capture_mode_since;

DROP TABLE IF EXISTS ext.ext_zalo_personal_allowlist;
