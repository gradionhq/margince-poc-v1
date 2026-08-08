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
	sql := rt.tx.only(t)
	if !strings.Contains(sql, "INSERT INTO "+noteTable) {
		t.Errorf("the tick does not write the unit's own table:\n%s", sql)
	}
	// The fan-out made visible. The dispatcher enqueues one child per live
	// workspace; on a single-workspace dev install that is one child, so a row
	// that said only "tick #7" would demonstrate the single-tenant case and
	// leave the multi-tenant guarantee untested.
	if strings.Count(sql, callerWorkspace) != 2 {
		t.Errorf("the tick does not both scope and NAME its workspace:\n%s", sql)
	}
	// One statement: a count in one and an insert in another would race two
	// concurrent ticks of the same workspace onto one number.
	if rt.tx.args[0][0] != heartbeatPrefix+"%" {
		t.Errorf("the tick counts previous ticks by %v, want the heartbeat prefix", rt.tx.args[0][0])
	}
}

// TestHeartbeatPrefixIsALiteralLikePattern: the count above is a LIKE, so a %
// or _ in the prefix would silently widen what counts as a previous tick.
func TestHeartbeatPrefixIsALiteralLikePattern(t *testing.T) {
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
