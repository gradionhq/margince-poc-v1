// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestJobKindsAreStable(t *testing.T) {
	if got := (CloseDateSweepArgs{}).Kind(); got != "close_date_sweep" {
		t.Errorf("CloseDateSweepArgs.Kind() = %q, want close_date_sweep", got)
	}
	if got := (FollowUpReconcileArgs{}).Kind(); got != "follow_up_reconcile" {
		t.Errorf("FollowUpReconcileArgs.Kind() = %q, want follow_up_reconcile", got)
	}
	if got := (GmailPushArgs{}).Kind(); got != "gmail_push_sync" {
		t.Errorf("GmailPushArgs.Kind() = %q, want gmail_push_sync", got)
	}
}

// TestGmailPushInsertOptsDedupesByArgs is the load-bearing invariant for the
// push webhook (Task 8, gmailpush.go): a Pub/Sub redelivery for the same
// mailbox while a prior push-sync is in flight must be suppressed (ByArgs),
// over the same in-flight window the periodic sweeps use — never including
// completed, so a finished push-sync does not block the next one.
func TestGmailPushInsertOptsDedupesByArgs(t *testing.T) {
	opts := gmailPushInsertOpts()
	if !opts.UniqueOpts.ByArgs {
		t.Error("gmailPushInsertOpts: ByArgs = false, want true (dedupe redeliveries for the same mailbox)")
	}
	if len(opts.UniqueOpts.ByState) != len(activeSweepStates) {
		t.Fatalf("gmailPushInsertOpts: ByState = %v, want activeSweepStates", opts.UniqueOpts.ByState)
	}
	for i, s := range activeSweepStates {
		if opts.UniqueOpts.ByState[i] != s {
			t.Errorf("gmailPushInsertOpts: ByState[%d] = %v, want %v", i, opts.UniqueOpts.ByState[i], s)
		}
	}
}

// TestUniquenessWindowExcludesCompleted is the load-bearing invariant: the
// periodic passes suppress a duplicate only while a prior run is in flight,
// never after it completes — otherwise a completed 24h sweep would block the
// next day's run until the completed row is cleaned out. It must also keep
// the states River requires when ByState is set.
func TestUniquenessWindowExcludesCompleted(t *testing.T) {
	have := map[rivertype.JobState]bool{}
	for _, s := range activeSweepStates {
		have[s] = true
	}

	if have[rivertype.JobStateCompleted] {
		t.Error("activeSweepStates includes JobStateCompleted — a completed sweep would block the next scheduled run")
	}

	// River requires these states whenever ByState is set explicitly.
	for _, required := range []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
	} {
		if !have[required] {
			t.Errorf("activeSweepStates omits required state %q", required)
		}
	}
}
