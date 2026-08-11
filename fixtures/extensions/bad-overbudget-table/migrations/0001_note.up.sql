-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- NEGATIVE FIXTURE. The table below IS inside the unit's namespace, and the
-- identifier it derives is 64 bytes — one past PostgreSQL's limit, which
-- truncates silently. Generation must refuse it AT THIS LINE.
CREATE TABLE ext.ext_bad_overbudget_table_note_with_a_deliberately_long_suffix_xy (
    id           uuid NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    body         text NOT NULL
);
