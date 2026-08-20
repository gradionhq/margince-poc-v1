// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"syscall"
	"testing"
	"time"
)

// TestStopIsSafeOnAServiceThatNeverStarted pins the property that makes the
// shutdown path correct after a PARTIAL start.
//
// stack.start returns at the first failure, and stack.stop then asks every
// service to stop — including the ones after the failure, whose *child is still
// nil. That works because (*child).stop answers for a nil receiver, which is
// legal in Go and easy to delete: whoever removes that guard sees only an
// if-block that "cannot happen", and the launcher then panics on the very path
// that reports why the start failed. The user would get a crash instead of the
// reason.
//
// So the assertion is not "no nil dereference" in the abstract — it is that each
// of these entry points survives being called on the zero value.
func TestStopIsSafeOnAServiceThatNeverStarted(t *testing.T) {
	t.Run("a child that was never started", func(t *testing.T) {
		var never *child
		if err := never.stop(syscall.SIGTERM, time.Second); err != nil {
			t.Fatalf("stopping a nil child reported an error: %v", err)
		}
	})

	t.Run("the event bus before its process exists", func(t *testing.T) {
		bus := &eventBus{}
		if err := bus.stop(); err != nil {
			t.Fatalf("stopping an unstarted bus reported an error: %v", err)
		}
	})

	// The api fails readiness often enough for this to be the common shape, not a
	// theoretical one: api is assigned, worker is not, and the shutdown that
	// reports the api's failure must not die on the worker.
	t.Run("both roles when neither was started", func(t *testing.T) {
		be := &backend{}
		if err := be.stop(); err != nil {
			t.Fatalf("stopping an unstarted backend reported an error: %v", err)
		}
	})

	t.Run("the web server before it listens", func(t *testing.T) {
		web := &ui{}
		if err := web.stop(); err != nil {
			t.Fatalf("stopping an unstarted web server reported an error: %v", err)
		}
	})
}
