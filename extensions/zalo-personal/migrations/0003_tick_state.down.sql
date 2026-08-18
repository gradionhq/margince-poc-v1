-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its policy, its index, its grants and its foreign key
-- with it, which is why nothing else is spelled here: a down migration that
-- revoked and dropped each piece separately would be several statements that can
-- disagree with the up migration rather than one that cannot.
--
-- WHAT REVERTING COSTS, stated because it is not nothing. Without the markers the
-- capture cannot tell a CRM reply's echo from a phone reply, and whichever way the
-- code then decides, one of the two is wrong — a duplicated reply, or a one-sided
-- conversation. Without the two columns every member is polled at the base cadence
-- forever, which is only ever a load problem and never a correctness one: the
-- direction that risks losing messages is polling LESS often, not more. And without
-- the reading positions every captured message is offered to capture again on the
-- next tick — deduplicated on its natural key, so nothing is duplicated and nothing
-- is lost, but the adaptive cadence stops working because every tick looks productive.

-- The columns first, then the table: the index on the connection goes with the
-- columns it covers, so nothing here has to name it.
ALTER TABLE ext.ext_zalo_personal_connection
    DROP COLUMN IF EXISTS idle_streak,
    DROP COLUMN IF EXISTS poll_after;

DROP TABLE IF EXISTS ext.ext_zalo_personal_sent_message;

DROP TABLE IF EXISTS ext.ext_zalo_personal_conversation_cursor;
