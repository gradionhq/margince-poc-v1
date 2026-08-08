// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestHeartbeatWritesOneRowNamingItsWorkspace(t *testing.T) {
	rt := newRuntime()
	if err := heartbeat(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.tx.statements) != 2 {
		t.Fatalf("the tick issued %d statements, want the insert and the prune:\n%s",
			len(rt.tx.statements), strings.Join(rt.tx.statements, "\n---\n"))
	}
	insert := rt.tx.statements[0]
	if !strings.Contains(insert, "INSERT INTO "+noteTable) {
		t.Errorf("the tick does not write the unit's own table:\n%s", insert)
	}
	// The fan-out made visible. The dispatcher enqueues one child per live
	// workspace; on a single-workspace dev install that is one child, so a row
	// naming no tenant would demonstrate the single-tenant case and leave the
	// multi-tenant guarantee untested.
	if strings.Count(insert, callerWorkspace) != 2 {
		t.Errorf("the tick does not both scope and NAME its workspace:\n%s", insert)
	}
	if rt.tx.args[0][0] != kindHeartbeat {
		t.Errorf("the tick writes kind %v, want %q — the column is what marks the row as the job's", rt.tx.args[0][0], kindHeartbeat)
	}
}

// TestHeartbeatCarriesNoTickNumber is a REGRESSION test, and the thing it
// guards is a counter that looked right in review and was wrong on screen.
//
// `count(*) + 1` over surviving rows, with the prune holding the population at
// keptHeartbeats, climbs to keptHeartbeats+1 and then stops: from that tick
// onward every row carries the identical label. The comment claimed the prune
// "renumbered"; it did not, it saturated, and consecutive rows read the same.
// created_at carries the sequence now, and the screen already renders it.
func TestHeartbeatCarriesNoTickNumber(t *testing.T) {
	rt := newRuntime()
	if err := heartbeat(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	insert := rt.tx.statements[0]
	for _, banned := range []string{"count(*)", "tick #"} {
		if strings.Contains(insert, banned) {
			t.Errorf("the tick is numbering itself again (%q) — the count saturates at keptHeartbeats+1 "+
				"and every later row repeats it:\n%s", banned, insert)
		}
	}
	if !strings.Contains(heartbeatPrefix, "heartbeat") {
		t.Errorf("the row no longer says what it is: %q", heartbeatPrefix)
	}
}

// TestHeartbeatPrunesItsOwnHistory is the assertion that keeps the demo
// usable, not a tidiness check.
//
// At 60s a tick writes 1,440 rows per workspace per day into the table the
// screen reads with LIMIT 200. Unpruned, every note a human typed drops below
// the read window after about 3.3 hours of uptime — so UAT step 4, "add a
// note, restart the stack, it is still there", stops being observable, and the
// step that proves the migrations layer works fails for an unrelated reason.
func TestHeartbeatPrunesItsOwnHistory(t *testing.T) {
	rt := newRuntime()
	if err := heartbeat(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	prune := rt.tx.statements[1]
	if !strings.HasPrefix(strings.TrimSpace(prune), "DELETE FROM "+noteTable) {
		t.Fatalf("the tick does not prune:\n%s", prune)
	}
	if rt.tx.args[1][0] != kindHeartbeat || rt.tx.args[1][1] != keptHeartbeats {
		t.Errorf("the prune runs with %v, want (%q, %d)", rt.tx.args[1], kindHeartbeat, keptHeartbeats)
	}
}

// TestThePruneCannotReachAUserNote is the second half of the same correction,
// and it is the one that was destroying data.
//
// The prune used to select its victims with `body LIKE '⟳ heartbeat — tick #%'`.
// Nothing stopped a person typing that — the glyph is one paste away — and a
// note that did was counted as a tick and then deleted by the next tick. The
// row's identity is a column now, so the delete cannot be reached by anything a
// user can write into a body.
func TestThePruneCannotReachAUserNote(t *testing.T) {
	rt := newRuntime()
	if err := heartbeat(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	prune := rt.tx.statements[1]
	if strings.Contains(prune, "body") {
		t.Fatalf("the prune still reads the body, so a note a human typed can select itself for deletion:\n%s", prune)
	}
	if strings.Count(prune, "kind = $1") != 2 {
		t.Errorf("the prune is not confined to the job's own rows on both sides of the NOT IN:\n%s", prune)
	}
	// And the value it matches is one the notes path never writes: addNote
	// leaves `kind` to its column default.
	if kindHeartbeat == kindNote {
		t.Fatal("the two kinds are the same string, so the prune matches every note")
	}
	for _, statement := range noteWriteStatements(t) {
		if strings.Contains(statement, kindHeartbeat) {
			t.Errorf("a notes-path statement writes the heartbeat kind:\n%s", statement)
		}
	}
}

// noteWriteStatements runs the note writers and returns the SQL they issued, so
// the test above can assert none of them can label a row as the job's.
func noteWriteStatements(t *testing.T) []string {
	t.Helper()
	rt := newRuntime()
	rt.tx.row = []any{"11111111-1111-4111-8111-111111111111", "a note", stamp}
	if _, err := addNote(context.Background(), rt, json.RawMessage(`{"body":"a note"}`)); err != nil {
		t.Fatal(err)
	}
	return rt.tx.statements
}

func TestHeartbeatFailsTheAttemptRatherThanSwallowingTheError(t *testing.T) {
	// A tick that logged and returned nil would be a green River row over a
	// workspace that got no heartbeat, which is indistinguishable in every
	// gauge from one that ran.
	rt := newRuntime()
	rt.tx.err = errors.New("relation does not exist")
	if err := heartbeat(context.Background(), rt); err == nil {
		t.Fatal("a failed tick reported success")
	}
}

// TestHeartbeatPruneFailureFailsTheTick: the prune is in the same transaction
// as the insert, so a tick either writes and prunes or does neither. A prune
// that failed quietly would leave the unbounded growth in place while every
// River row stayed green.
func TestHeartbeatPruneFailureFailsTheTick(t *testing.T) {
	rt := newRuntime()
	rt.tx.err, rt.tx.failFrom = errors.New("deadlock detected"), 2
	if err := heartbeat(context.Background(), rt); err == nil {
		t.Fatal("a failed prune reported a successful tick")
	}
	if len(rt.tx.statements) != 2 {
		t.Fatalf("the insert did not run, so this tested the wrong failure: %v", rt.tx.statements)
	}
}
