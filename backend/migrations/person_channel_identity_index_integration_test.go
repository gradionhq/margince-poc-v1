// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 0152's index shape, proven against the live catalog rather than read off the
// migration text: an index the FOREIGN KEY cannot use is indistinguishable from
// one it can, until a person is deleted on a table large enough for it to
// matter.

import (
	"context"
	"testing"
)

// The person cascade on person_channel_identity needs an index Postgres can
// actually use. A PARTIAL index cannot serve a foreign key: the planner has no
// proof that a row about to be cascaded satisfies the predicate, so it falls
// back to a sequential scan of the whole table per deleted person.
//
// The index is one index serving two readers on the same columns — the live-row
// read path and the cascade — so this asserts the shape both need rather than
// the existence of a second one.
func TestPersonChannelIdentityCascadeIndexIsUnconditional(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	migrateAll(t, conn)

	var predicate *string
	var columns []string
	if err := conn.QueryRow(context.Background(), `
		SELECT pg_get_expr(i.indpred, i.indrelid),
		       array_agg(a.attname ORDER BY k.ord)
		  FROM pg_index i
		  JOIN pg_class c ON c.oid = i.indexrelid
		  JOIN unnest(i.indkey) WITH ORDINALITY k(attnum, ord) ON true
		  JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		 WHERE c.relname = 'idx_person_channel_identity_person'
		 GROUP BY i.indpred, i.indrelid`).Scan(&predicate, &columns); err != nil {
		t.Fatalf("reading idx_person_channel_identity_person: %v", err)
	}
	if predicate != nil {
		t.Errorf("the index the person cascade relies on is partial (%s); the cascade cannot use it", *predicate)
	}
	if len(columns) < 2 || columns[0] != "workspace_id" || columns[1] != "person_id" {
		t.Errorf("index columns = %v, want (workspace_id, person_id) — the cascade's own key", columns)
	}
}
