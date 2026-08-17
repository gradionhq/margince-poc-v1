-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the column takes its default and its CHECK with it, which is why
-- neither is named here: a down migration that undid each piece separately would
-- be several statements that can disagree with the up migration rather than one
-- that cannot.

ALTER TABLE ext.ext_zalo_oa_connection
    DROP COLUMN IF EXISTS poll_request_budget;
