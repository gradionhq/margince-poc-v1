-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- HOW MANY PROVIDER REQUESTS ONE TICK MAY SPEND, on the row rather than in a
-- constant, because the right number is a property of the account and not of
-- this code.
--
-- It is on the connection because that is what the number is about: an
-- installation with four conversations and one with four hundred need different
-- ceilings, and an operator can only reason about the one in front of them. It is
-- also what the status screen renders, so the value that governs the tick is the
-- value a human reads.
--
-- The ceiling exists because the per-OA rate limit is NOT surfaced in any
-- response header — it can only be hit, not observed. At the two-minute cadence
-- the default of 40 is roughly 20 requests a minute against a limit measured at
-- 100 on this account's package, which leaves room for the sends a rep makes
-- while a tick is running.
--
-- The bounds are the honest ones. Below 2 a tick cannot both read the account and
-- read a page, so it could never make progress. Above 200 a single tick would
-- outrun the provider's per-minute ceiling on its own, and a tick that is rate
-- limited half way through is a tick that read part of a conversation.
ALTER TABLE ext.ext_zalo_oa_connection
    ADD COLUMN poll_request_budget integer NOT NULL DEFAULT 40
        CHECK (poll_request_budget BETWEEN 2 AND 200);
