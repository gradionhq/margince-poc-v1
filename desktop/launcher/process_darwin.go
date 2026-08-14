// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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

// isExpectedStopExit reports whether err is the process dying from sig — the
// normal outcome of stop(), not a fault to report.
func isExpectedStopExit(err error, sig syscall.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == sig
}
