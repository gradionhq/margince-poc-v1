-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its policy, its grants and its foreign key with it,
-- which is why nothing else is spelled here: a down migration that revoked and
-- dropped each piece separately would be several statements that can disagree
-- with the up migration rather than one that cannot.
--
-- It does NOT take the admin's sealed token pair, which lives in the core's
-- extension_secret table. Dropping this unit's schema layer is not the same act
-- as withdrawing a credential, and a down migration reaching into a core table
-- to do it would be this unit writing outside its own namespace.

DROP TABLE IF EXISTS ext.ext_zalo_oa_connection;
