-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its policy, its constraints, its grants and its foreign
-- key with it, which is why nothing else is spelled here: a down migration that
-- revoked and dropped each piece separately would be several statements that can
-- disagree with the up migration rather than one that cannot.
--
-- WHAT REVERTING COSTS, stated because it is the whole reason the table exists. Every
-- record of when an exclusion was lifted is destroyed, and the defect comes back
-- exactly as it was: a member who blocks somebody, waits, and unblocks them receives
-- the whole blocked period on the next tick, silently, from precisely the window they
-- had decided against. It is not recoverable afterwards — the floors are the only
-- copy of that fact, because the verdict rows they were derived from were already
-- deleted by the act of lifting.

DROP TABLE IF EXISTS ext.ext_zalo_personal_conversation_floor;
