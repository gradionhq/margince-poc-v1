-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- Which rows the heartbeat owns, as a FACT ABOUT THE ROW rather than a guess
-- about its text.
--
-- The tick used to mark and find its own rows by a body prefix, and both halves
-- of that were wrong. Finding: a note a human typed beginning with the same
-- glyph was counted as a tick and then DELETED by the prune — the unit
-- destroying user data on a string match. Marking: nothing stopped a human
-- typing it.
--
-- A separate migration rather than an edit to 0001, deliberately. 0001 is
-- already applied on dev and UAT databases; dbmigrate keys on the version, so an
-- amended 0001 would never re-run — the column would simply be absent, the
-- migrate step would report "schema is at head", and the unit would fail at its
-- first insert. A silent no-op is the worst available outcome for a schema
-- change.

ALTER TABLE ext.ext_crm_demo_note
    ADD COLUMN kind text NOT NULL DEFAULT 'note'
        CONSTRAINT ext_crm_demo_note_kind_known CHECK (kind IN ('note', 'heartbeat'));

-- The DEFAULT is what makes this safe on a table that already holds rows: every
-- existing row is a note, which is true — the heartbeat's own rows are
-- disposable by construction, and mislabelling one as a note only means it
-- stops being pruned, never that anything is lost.
