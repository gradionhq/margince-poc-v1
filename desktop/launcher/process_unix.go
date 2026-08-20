// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The POSIX half, not the macOS one: the socket, the signals and the path
// shapes here are the same on any unix. The constraint is explicit because only
// GOOS filename suffixes are implicit, and "unix" is not one of them.
//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// configureChild leaves the child in the launcher's own process group, which
// is what makes Ctrl-C at the terminal reach every service at once. The
// launcher still stops each one explicitly on the way down; this only means a
// user who interrupts the terminal is not waiting on a supervisor to relay it.
func configureChild(*exec.Cmd) {}

// requestStop delivers the signal the caller chose. See child.stop for why the
// choice matters — the postmaster wants SIGINT where everything else wants
// SIGTERM.
func (c *child) requestStop(sig syscall.Signal) error {
	if err := c.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal %s: %w", c.name, err)
	}
	return nil
}

// isExpectedStopExit reports whether err is the process dying because it was
// asked to — the normal outcome of stop(), not a fault to report.
//
// SIGINT counts however stop() was called, and configureChild above is the
// reason: the children stay in the launcher's own process group so that Ctrl-C
// at the terminal reaches all of them at once. That means the terminal delivers
// SIGINT to api, worker and bus DIRECTLY, while stop() asks them with SIGTERM —
// so on every ordinary quit the child is already dead of a different signal than
// the one this is comparing against. Matching only sig reported each service as
// having faulted, which is the last thing on screen after a clean Ctrl-C.
//
// It stays narrow: SIGINT and the requested signal, not "signalled at all". A
// service killed by SIGSEGV or SIGKILL is still a fault worth showing.
func isExpectedStopExit(err error, sig syscall.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return false
	}
	return status.Signal() == sig || status.Signal() == syscall.SIGINT
}
