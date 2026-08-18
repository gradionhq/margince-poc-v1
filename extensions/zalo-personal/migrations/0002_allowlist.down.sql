-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its index, its policy, its grants and its foreign key
-- with it, and dropping the column takes its CHECK: which is why nothing else is
-- spelled here. A down migration that undid each piece separately would be
-- several statements that can disagree with the up migration rather than one
-- that cannot.
--
-- The cursor goes with the verdict it belongs to, which is the whole reason it is a
-- column on this table rather than on the connection: there is no second thing to
-- revert, and no state where an installation holds a bookmark into a capture nothing
-- can arm.

DROP TABLE IF EXISTS ext.ext_zalo_personal_allowlist;
