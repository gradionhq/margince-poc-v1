-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- NEGATIVE FIXTURE. The table below is spelled without the unit's
-- ext_bad_unprefixed_table_ namespace, so it squats a name in the shared ext
-- schema that another unit could own. Generation must refuse it AT THIS LINE.
CREATE TABLE ext.crm_demo_note (
    id           uuid NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    body         text NOT NULL
);
