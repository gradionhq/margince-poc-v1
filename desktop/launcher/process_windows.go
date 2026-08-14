// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// Windows has no signals, so a graceful stop here is a console control event
// instead. The mechanism is narrower than POSIX in exactly one way that
// matters, and it costs nothing:
//
// A signal on macOS says WHICH kind of stop is wanted, and the postmaster is
// the one service that wants a different one (SIGINT, fast shutdown, rather
// than SIGTERM's wait-for-every-client). On Windows the postmaster is not a
// child at all — pg_ctl owns it and is told `-m fast` directly, see
// postgres_windows.go — so every process that DOES pass through here wants the
// same thing: quit now, cleanly. One event expresses that.
//
// CTRL_BREAK rather than CTRL_C: a child started with CREATE_NEW_PROCESS_GROUP
// has Ctrl-C disabled for its group, and the break event is what still reaches
// it. The Go runtime maps both to os.Interrupt, which is what cmd/api and
// cmd/worker already wait on, so the shipped binaries shut down here through
// the same path they use on a server.
var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvt = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

// ctrlBreakEvent is CTRL_BREAK_EVENT from the Windows console API.
const ctrlBreakEvent = 1

// statusControlCExit is the exit status Windows reports for a process torn
// down by the console control handler rather than by its own return — the
// normal outcome for a service that does not install a handler of its own.
const statusControlCExit = 0xC000013A

// configureChild puts the child in its own process group.
//
// Without this there is no way to interrupt one service without interrupting
// the launcher itself: a console control event addressed to group 0 goes to
// every process attached to the console, the launcher included, so the
// supervisor would kill itself on the way to killing its first child.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// requestStop sends CTRL_BREAK to the child's process group.
//
// The signal argument is deliberately unused: see the note above — on Windows
// there is one graceful stop, and the only service that wanted a different one
// does not come through here.
func (c *child) requestStop(_ syscall.Signal) error {
	// The second argument is a process GROUP id, which for a child created
	// with CREATE_NEW_PROCESS_GROUP is its own pid.
	result, _, err := procGenerateConsoleCtrlEvt.Call(ctrlBreakEvent, uintptr(c.cmd.Process.Pid))
	if result == 0 {
		return fmt.Errorf("ask %s to stop: %w", c.name, err)
	}
	return nil
}

// isExpectedStopExit reports whether err is the process quitting because we
// asked it to, rather than failing.
//
// Two outcomes are both "it stopped": a service that handles the event and
// returns cleanly, and one that does not, which Windows tears down with
// STATUS_CONTROL_C_EXIT. Neither is a fault worth putting in front of a user
// who just quit the app.
func isExpectedStopExit(err error, _ syscall.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return uint32(exitErr.ExitCode()) == statusControlCExit
}
