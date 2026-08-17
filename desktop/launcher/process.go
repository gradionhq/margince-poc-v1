// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// loopbackHost is the only interface anything in this installation listens on.
//
// Every service here — the database, the bus, the api, the web ui — is reached
// by another process on the same machine and by nothing else. Binding the
// loopback address rather than a wildcard is what keeps a laptop on a café
// network from serving a CRM to it.
const loopbackHost = "127.0.0.1"

// child is one supervised service process. A service the bundle runs is a
// child of the launcher rather than a daemon, so quitting the app cannot leave
// an orphan holding a port or a data directory — the failure that makes the
// next launch report a stale lock file the user cannot interpret.
//
// Postgres is the one exception, and only on Windows, where it has to be
// pg_ctl's process rather than ours (postgres_windows.go says why). The same
// property is bought there by stopping a stray postmaster at startup instead.
type child struct {
	name string
	cmd  *exec.Cmd
	log  *os.File

	// done carries the process's exit, and gone reports that it has happened.
	//
	// Exactly ONE goroutine calls Wait, started with the process, because Wait
	// is what populates the exit state and it may only be called once. Reading
	// cmd.ProcessState instead would report "still running" until something
	// called Wait — so a service that died a second after starting looked
	// alive until its readiness timeout expired, and the user waited two
	// minutes for an error the launcher already had.
	done chan error
	gone atomic.Bool
}

// startChild launches bin in workDir and streams its output to
// logDir/<name>.log.
//
// workDir is always the installation folder, never the directory the user
// happened to launch from. Relative paths in the configuration — the
// bootstrap password file among them — resolve against the child's working
// directory, so pinning it here is what lets the configuration stay relative
// and the whole folder stay portable.
//
// Service output goes to a file rather than the launcher's stdout because a
// double-clicked start shows only the launcher's own summary; when something
// fails at a user's desk, that file is the only evidence there is.
func startChild(name, bin string, args, env []string, workDir, logDir string) (*child, error) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logPath := filepath.Join(logDir, name+".log")
	// #nosec G304 -- logPath is logDir joined with this launcher's own service name; both are ours
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	// #nosec G204 -- bin is a shipped binary addressed through layout.appBin/pgBin; the args are built in this file, never from input
	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	configureChild(cmd)
	if err := cmd.Start(); err != nil {
		if closeErr := logFile.Close(); closeErr != nil {
			return nil, fmt.Errorf("start %s: %w (and closing its log failed: %v)", name, err, closeErr)
		}
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	c := &child{name: name, cmd: cmd, log: logFile, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		// gone is set before done is sent, so a reader that sees the send has
		// necessarily seen the flag too.
		c.gone.Store(true)
		c.done <- err
	}()
	return c, nil
}

// stop asks the process to quit and waits for it to exit, giving up after
// grace.
//
// The signal is a parameter because Postgres does not use the conventional
// one: SIGTERM asks it to wait for every client to disconnect, which never
// completes while the api still holds its pool, so the postmaster gets
// SIGINT (fast shutdown) instead. Windows has no signal vocabulary at all —
// requestStop there sends the one console control event that means the same
// thing, and process_windows.go explains why the distinction costs nothing.
func (c *child) stop(sig syscall.Signal, grace time.Duration) error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	// A process that has already exited needs no asking, and asking anyway is
	// how the request fails: Windows has no process group left to address once
	// the last member is gone.
	var requestErr error
	if !c.gone.Load() {
		requestErr = c.requestStop(sig)
	}

	var stopErr error
	select {
	case err := <-c.done:
		// The process is gone, which is the whole point of stop(). A request
		// that failed and was followed by a clean exit asked something of a
		// process that had already left — not a fault, and not worth putting
		// in front of a user who is quitting. Only how it exited matters.
		if err != nil && !isExpectedStopExit(err, sig) {
			stopErr = fmt.Errorf("%s exited: %w", c.name, err)
		}
	case <-time.After(grace):
		// The kill happens either way: a process still running after the grace
		// period has to go, and a failed request is the likeliest reason it
		// never heard us — a reason to report, not a reason to leave it alive.
		killErr := c.cmd.Process.Kill()
		if killErr != nil && errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		switch {
		case killErr != nil:
			stopErr = fmt.Errorf("kill %s after %s: %w", c.name, grace, killErr)
		case requestErr != nil:
			stopErr = fmt.Errorf("%s could not be asked to stop and was killed after %s: %w", c.name, grace, requestErr)
		default:
			stopErr = fmt.Errorf("%s did not exit within %s and was killed", c.name, grace)
		}
		<-c.done
	}

	if err := c.log.Close(); err != nil && stopErr == nil {
		stopErr = fmt.Errorf("close %s log: %w", c.name, err)
	}
	return stopErr
}

// exited reports whether the process is already gone, so a readiness wait can
// fail immediately with the real reason instead of polling a dead service
// until its timeout expires.
func (c *child) exited() bool {
	return c.gone.Load()
}

// waitUntil polls probe until it succeeds, the context ends, or timeout
// elapses. dead lets the caller abandon the wait the moment the service it is
// waiting for has crashed.
func waitUntil(ctx context.Context, what string, timeout time.Duration, dead func() bool, probe func() error) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var last error
	for {
		if last = probe(); last == nil {
			return nil
		}
		if dead != nil && dead() {
			return fmt.Errorf("%s exited before becoming ready (last probe: %w)", what, last)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s (last probe: %w)", what, timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// dialTCP probes a listening socket.
func dialTCP(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// httpOK probes an endpoint that must answer 200 — the readiness contract
// /readyz already implements.
func httpOK(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() {
		// The body is drained by Close here; a failure to close leaks a
		// connection but must not mask a successful probe.
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return nil
}

// freePort reserves an ephemeral port by binding and releasing it.
//
// The gap between release and the service binding is a real race, but the
// alternative — passing an inherited listener — is not something the shipped
// binaries accept, and on a single-user desktop the collision window is
// microseconds against a machine with no other process hunting for ports.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", loopbackHost+":0")
	if err != nil {
		return 0, fmt.Errorf("reserve a local port: %w", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		// Unreachable for a "tcp" listener, but a bare assertion here would
		// turn a future change of network to a panic in front of the user.
		unexpected := fmt.Errorf("reserved a local port on an unexpected address type %T", listener.Addr())
		if closeErr := listener.Close(); closeErr != nil {
			return 0, fmt.Errorf("%w (and releasing it failed: %v)", unexpected, closeErr)
		}
		return 0, unexpected
	}
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release reserved port %d: %w", addr.Port, err)
	}
	return addr.Port, nil
}
