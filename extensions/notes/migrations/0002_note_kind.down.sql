-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the column takes its CHECK constraint with it.

ALTER TABLE ext.ext_notes_note DROP COLUMN IF EXISTS kind;
