// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"testing"
)

// TestLogicalCallAppendKeepsExactlyOneTerminal proves the invariant every
// flush relies on: after any number of appends, exactly the LAST one
// appended carries IsTerminal — a later attempt always supersedes an
// earlier one, never the reverse.
func TestLogicalCallAppendKeepsExactlyOneTerminal(t *testing.T) {
	lc := newLogicalCall()
	lc.append(Call{Tier: TierPremium, ErrorSentinel: "provider_error"})
	lc.append(Call{Tier: TierCheapCloud, ErrorSentinel: "provider_error"})
	lc.append(Call{Tier: TierLocalSmall})

	terminalCount := 0
	for i, c := range lc.attempts {
		if c.Attempt != i+1 {
			t.Fatalf("attempt %d has Attempt=%d, want %d", i, c.Attempt, i+1)
		}
		if c.LogicalCallID != lc.id {
			t.Fatalf("attempt %d does not carry the logical call's id", i)
		}
		if c.IsTerminal {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("want exactly 1 terminal attempt, got %d", terminalCount)
	}
	if !lc.attempts[2].IsTerminal {
		t.Fatal("the last attempt appended must be the terminal one")
	}
	if lc.terminal().Tier != TierLocalSmall {
		t.Fatalf("terminal() = %+v, want the last-appended attempt", lc.terminal())
	}
}

// TestComputeConfigHashIsDeterministicAndSensitiveToEveryField: the same
// four inputs always digest to the same hash (EnsureConfig's ON CONFLICT
// DO NOTHING depends on this to collapse repeats onto one row), and
// changing any ONE input must change the hash — two config snapshots that
// differ only in prompt version, say, must not collide onto the same
// dimension row.
func TestComputeConfigHashIsDeterministicAndSensitiveToEveryField(t *testing.T) {
	base := computeConfigHash("task-hash", "routing-hash", "v1", json.RawMessage(`{}`))
	again := computeConfigHash("task-hash", "routing-hash", "v1", json.RawMessage(`{}`))
	if base != again {
		t.Fatalf("identical inputs produced different hashes: %q vs %q", base, again)
	}
	variants := []string{
		computeConfigHash("other-task-hash", "routing-hash", "v1", json.RawMessage(`{}`)),
		computeConfigHash("task-hash", "other-routing-hash", "v1", json.RawMessage(`{}`)),
		computeConfigHash("task-hash", "routing-hash", "v2", json.RawMessage(`{}`)),
		computeConfigHash("task-hash", "routing-hash", "v1", json.RawMessage(`{"k":"v"}`)),
	}
	for i, v := range variants {
		if v == base {
			t.Fatalf("variant %d collided with the base hash — a changed field must change the digest", i)
		}
	}
}

// TestNewConfigSnapshotUsesTheGeneratedTaskContractHash pins the task-
// contract half of the spec §4 config key to the generated constant
// (tasks_gen.go) rather than a hand-maintained copy that could drift from
// the contract it is meant to fingerprint.
func TestNewConfigSnapshotUsesTheGeneratedTaskContractHash(t *testing.T) {
	snap := newConfigSnapshot("routing-hash")
	if snap.TaskContractHash != TaskContractHash {
		t.Fatalf("TaskContractHash = %q, want the generated constant %q", snap.TaskContractHash, TaskContractHash)
	}
	if snap.RoutingConfigHash != "routing-hash" {
		t.Fatalf("RoutingConfigHash = %q, want the passed-in digest", snap.RoutingConfigHash)
	}
	if snap.Hash == "" {
		t.Fatal("newConfigSnapshot must compute Hash, not leave it zero")
	}
	if snap.Hash != computeConfigHash(snap.TaskContractHash, snap.RoutingConfigHash, snap.PromptVersion, snap.ProviderParams) {
		t.Fatal("Hash does not match computeConfigHash over the snapshot's own fields")
	}
}
