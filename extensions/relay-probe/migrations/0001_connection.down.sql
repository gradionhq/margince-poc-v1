-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Dropping the table takes its policy, its grants and its foreign key with it,
-- which is why nothing else is spelled here: a down migration that revoked and
-- dropped each piece separately would be four statements that can disagree
-- with the up migration rather than one that cannot.
--
-- It does NOT take the members' deposited tokens, which live in the core's
-- extension_secret table. Dropping this unit's schema layer is not the same act
-- as withdrawing a credential, and a down migration that reached into a core
-- table to do it would be this unit writing outside its own namespace.

DROP TABLE IF EXISTS ext.ext_relay_probe_connection;
