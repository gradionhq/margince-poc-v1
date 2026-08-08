-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its policy, its grants and its foreign key with it,
-- which is why nothing else is spelled here: a down migration that revoked and
-- dropped each piece separately would be four statements that can disagree
-- with the up migration rather than one that cannot.

DROP TABLE IF EXISTS ext.ext_crm_demo_note;
