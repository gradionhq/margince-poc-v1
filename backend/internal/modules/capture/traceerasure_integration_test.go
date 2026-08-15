// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// Erasure reaching the trace. The sweep bounds exposure to a day; it does not
// ANSWER a request made inside that day, and a request honoured everywhere
// except one diagnostic table is not honoured.

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestErasureDeletesTracePayloadsForTheSubjectsAddress(t *testing.T) {
	ctx, ws, db, _ := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)

	// Two messages: one from the subject, one from somebody else. The erasure
	// must reach the first and leave the second — a purge that took the page
	// would be as wrong as one that took nothing.
	for _, sender := range []struct{ id, email string }{
		{"erase-me", "dana@client.io"},
		{"keep-me", "sam@other.io"},
	} {
		entry := capture.TraceEntry{
			UserID: me, Connector: "gmail", SourceSystem: "gmail", SourceID: sender.id,
			Outcome: capture.TraceCaptured, Counterparty: sender.email, Subject: "Quarterly numbers",
		}
		if err := db.Tx(memberCtx, func(tx pgx.Tx) error {
			return capture.Trace(memberCtx, tx, entry, true) // payload posture ON
		}); err != nil {
			t.Fatalf("seeding a payload trace: %v", err)
		}
	}

	// The statement the erasure cascade runs (privacy/erasuretimeline.go).
	if err := db.Tx(memberCtx, func(tx pgx.Tx) error {
		_, err := tx.Exec(memberCtx,
			`DELETE FROM capture_trace WHERE counterparty = lower($1)`, "Dana@Client.io")
		return err
	}); err != nil {
		t.Fatalf("erasing: %v", err)
	}

	if got := traceRows(memberCtx, t, db, "erase-me"); got != 0 {
		t.Errorf("rows for the erased subject = %d, want 0 — and note the address was mixed-case, which is why the column is written normalized", got)
	}
	if got := traceRows(memberCtx, t, db, "keep-me"); got != 1 {
		t.Errorf("rows for another sender = %d, want 1", got)
	}
}
