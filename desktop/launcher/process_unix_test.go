// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Signal semantics are the POSIX half's; Windows decides the same question from
// an exit code and has its own implementation.
//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
)

// exitErrorFromSignal runs a real process and kills it with sig, returning the
// error Wait reports.
//
// A hand-built exec.ExitError cannot stand in: the answer depends on
// syscall.WaitStatus, which only the kernel fills in, so a fabricated one would
// test the test. /bin/sleep is execed directly rather than through a shell —
// a shell would take the signal itself and leave the sleep orphaned.
func exitErrorFromSignal(t *testing.T, sig syscall.Signal) error {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the stand-in service: %v", err)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal it with %v: %v", sig, err)
	}
	err := cmd.Wait()
	if err == nil {
		t.Fatalf("the process exited cleanly despite %v, so this proves nothing", sig)
	}
	return err
}

// TestATerminalInterruptIsNotAFault covers the shutdown a user actually
// performs.
//
// configureChild keeps the services in the launcher's own process group so that
// Ctrl-C reaches all of them at once. The terminal therefore delivers SIGINT to
// api, worker and bus DIRECTLY, while stop() asks them with SIGTERM — so on
// every ordinary quit the child is already dead of a signal other than the one
// being asked about. Comparing against sig alone printed a fault for each
// service as the last thing on screen after a clean quit.
func TestATerminalInterruptIsNotAFault(t *testing.T) {
	t.Run("SIGINT while SIGTERM was requested: the Ctrl-C case", func(t *testing.T) {
		err := exitErrorFromSignal(t, syscall.SIGINT)
		if !isExpectedStopExit(err, syscall.SIGTERM) {
			t.Fatalf("a service killed by the terminal's SIGINT was reported as a fault: %v", err)
		}
	})

	t.Run("the requested signal is still expected", func(t *testing.T) {
		err := exitErrorFromSignal(t, syscall.SIGTERM)
		if !isExpectedStopExit(err, syscall.SIGTERM) {
			t.Fatalf("a service killed by the signal we sent was reported as a fault: %v", err)
		}
	})

	t.Run("Postgres is asked with SIGINT and that is expected too", func(t *testing.T) {
		err := exitErrorFromSignal(t, syscall.SIGINT)
		if !isExpectedStopExit(err, syscall.SIGINT) {
			t.Fatalf("the postmaster's own stop signal was reported as a fault: %v", err)
		}
	})

	// The widening has to stay narrow. If it became "signalled at all", a crash
	// would be reported as a clean stop and the log line telling the user why the
	// app died would disappear.
	t.Run("a crash is still a fault", func(t *testing.T) {
		err := exitErrorFromSignal(t, syscall.SIGSEGV)
		if isExpectedStopExit(err, syscall.SIGTERM) {
			t.Fatalf("a segfault was reported as an expected stop, hiding a real crash: %v", err)
		}
	})

	t.Run("a non-signal exit is not a stop", func(t *testing.T) {
		cmd := exec.Command("/bin/sh", "-c", "exit 3")
		err := cmd.Run()
		if err == nil {
			t.Fatal("expected a non-zero exit")
		}
		if isExpectedStopExit(err, syscall.SIGTERM) {
			t.Fatalf("a service that exited 3 on its own was reported as an expected stop: %v", err)
		}
	})
}
