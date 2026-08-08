// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

import (
	"context"
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
	// that said only "tick #7" would demonstrate the single-tenant case and
	// leave the multi-tenant guarantee untested.
	if strings.Count(insert, callerWorkspace) != 2 {
		t.Errorf("the tick does not both scope and NAME its workspace:\n%s", insert)
	}
	// One statement for the write: a count in one and an insert in another
	// would race two concurrent ticks of the same workspace onto one number.
	if rt.tx.args[0][0] != heartbeatLike {
		t.Errorf("the tick counts previous ticks by %v, want the heartbeat prefix", rt.tx.args[0][0])
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
	// Bounded by keptHeartbeats, and scoped to the tick's OWN rows on both
	// sides of the NOT IN — a delete that matched every row would take the
	// notes with it, which is the one way this could be worse than the leak it
	// fixes.
	if got := strings.Count(prune, "body LIKE $1"); got != 2 {
		t.Errorf("the prune is not confined to heartbeat rows (%d LIKE clauses):\n%s", got, prune)
	}
	if rt.tx.args[1][0] != heartbeatLike || rt.tx.args[1][1] != keptHeartbeats {
		t.Errorf("the prune runs with %v, want (%q, %d)", rt.tx.args[1], heartbeatLike, keptHeartbeats)
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

// TestHeartbeatPrefixIsALiteralLikePattern: the count above is a LIKE, so a %
// or _ in the prefix would silently widen what counts as a previous tick.
func TestHeartbeatPrefixIsALiteralLikePattern(t *testing.T) {
	// It bounds a DELETE now, not only a count: a metacharacter here would let
	// the prune match notes a human typed.
	if strings.ContainsAny(heartbeatPrefix, "%_\\") {
		t.Errorf("heartbeatPrefix %q carries a LIKE metacharacter", heartbeatPrefix)
	}
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
