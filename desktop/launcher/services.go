// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// valkey is the event bus behind platform/events.
type valkey struct {
	layout layout
	port   int
	proc   *child
}

func (v *valkey) addr() string { return fmt.Sprintf("127.0.0.1:%d", v.port) }

// start launches the bus on loopback at an ephemeral port.
//
// Loopback rather than a unix socket because the shipped api and worker build
// their client with redis.Options{Addr}, which speaks TCP — reaching the bus
// over a socket would mean changing product code the bundle is meant to run
// unmodified.
func (v *valkey) start(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	v.port = port

	dir := filepath.Join(v.layout.data, "valkey")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the valkey directory: %w", err)
	}

	// Persistence stays ON. The outbox in Postgres is the durable record, but
	// consumer-group offsets and the events.Dedupe keys live here — losing
	// them across a restart turns at-least-once delivery into visibly
	// duplicated work for the user.
	proc, err := startChild("valkey", v.layout.appBin("valkey-server"), []string{
		"--port", fmt.Sprintf("%d", port),
		"--bind", "127.0.0.1",
		"--dir", dir,
		"--appendonly", "yes",
	}, nil, v.layout.logs())
	if err != nil {
		return err
	}
	v.proc = proc

	return waitUntil(ctx, "valkey", 30*time.Second, proc.exited, func() error {
		return dialTCP(v.addr())
	})
}

func (v *valkey) stop() error { return v.proc.stop(syscall.SIGTERM, 15*time.Second) }

// backend runs the two process roles the server is split into.
type backend struct {
	layout layout
	pg     *postgres
	bus    *valkey
	port   int
	api    *child
	worker *child
}

func (b *backend) baseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", b.port) }

// migrate brings the schema to head before either role starts. It runs with
// the owner DSN — the app role deliberately cannot alter the schema it is
// bound by.
func (b *backend) migrate() error {
	cmd := exec.Command(b.layout.appBin("migrate"), "up", "-dsn", b.pg.ownerDSN())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("migrate failed: %w\n%s", err, out)
	}
	return nil
}

// start boots the api, waits for it to report ready, then starts the worker.
//
// Order matters for what the user sees: the window can open as soon as the
// api answers, and the worker's background sweeps do not gate a usable UI.
func (b *backend) start(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	b.port = port

	env := []string{"MARGINCE_ENV=desktop"}
	api, err := startChild("api", b.layout.appBin("api"), []string{
		"--addr", fmt.Sprintf("127.0.0.1:%d", port),
		"--dsn", b.pg.appDSN(),
		// The owner pool is what makes the custom-fields schema operations
		// answer rather than 501; a desktop install has no DBA to run them.
		"--schema-dsn", b.pg.ownerDSN(),
		"--config", b.layout.configPath(),
		"--redis", b.bus.addr(),
		// No BYOK key is configured at first launch, so the AI surfaces run
		// on the offline fake rather than failing. Replacing this with a real
		// provider is the Keychain work the design doc defers.
		"--ai-fake",
	}, env, b.layout.logs())
	if err != nil {
		return err
	}
	b.api = api

	readyURL := b.baseURL() + "/readyz"
	if err := waitUntil(ctx, "api", 120*time.Second, api.exited, func() error {
		return httpOK(readyURL)
	}); err != nil {
		return err
	}

	// The worker owns the automation time-scan and the retention sweeps; the
	// api's inline relay and the worker's standalone relay coexist because
	// outbox rows are claimed FOR UPDATE SKIP LOCKED.
	worker, err := startChild("worker", b.layout.appBin("worker"), []string{
		"--dsn", b.pg.appDSN(),
		"--config", b.layout.configPath(),
		"--redis", b.bus.addr(),
		"--retention-interval", "24h",
		"--ai-fake",
	}, env, b.layout.logs())
	if err != nil {
		return err
	}
	b.worker = worker
	return nil
}

// stop shuts both roles down. Both errors are collected rather than
// short-circuited: a worker that refuses to exit must not stop the api from
// being asked to, or the next launch inherits an orphan.
func (b *backend) stop() error {
	workerErr := b.worker.stop(syscall.SIGTERM, 20*time.Second)
	apiErr := b.api.stop(syscall.SIGTERM, 20*time.Second)
	if apiErr != nil && workerErr != nil {
		return fmt.Errorf("%w; and %w", apiErr, workerErr)
	}
	if apiErr != nil {
		return apiErr
	}
	return workerErr
}
