// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// unmodified. The port is ephemeral because nothing outside this installation
// ever addresses it.
func (v *valkey) start(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	v.port = port

	dir := filepath.Join(v.layout.data(), "valkey")
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
	}, nil, v.layout.root, v.layout.logs())
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
	layout  layout
	pg      *postgres
	bus     *valkey
	userEnv []string
	port    int
	api     *child
	worker  *child
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

// childEnv is the environment both roles inherit: the user's settings first,
// then the deployment posture.
//
// MARGINCE_ENV is appended last so it cannot be overridden from margince.env.
// A desktop installation holds a real customer's records and takes the
// production posture, which keeps the dev-only destructive switches (today,
// the admin data-reset endpoint) off. That is not a setting to expose beside
// an API key.
func (b *backend) childEnv() []string {
	return append(append([]string{}, b.userEnv...), "MARGINCE_ENV=production")
}

// aiFlags decides how the AI surfaces are driven.
//
// A routing file next to margince.yaml is the discoverable way to configure a
// real model, so it wins. Failing that, MARGINCE_AI_ROUTING in margince.env
// does the same job and must not be undercut by passing --ai-fake alongside
// it. Only when neither is present does the offline fake apply — so the
// surfaces answer instead of erroring on a fresh install.
func (b *backend) aiFlags() []string {
	if _, err := os.Stat(b.layout.aiRoutingPath()); err == nil {
		return []string{"--ai-routing", b.layout.aiRoutingPath()}
	}
	for _, entry := range b.userEnv {
		if strings.HasPrefix(entry, "MARGINCE_AI_ROUTING=") &&
			strings.TrimPrefix(entry, "MARGINCE_AI_ROUTING=") != "" {
			return nil
		}
	}
	return []string{"--ai-fake"}
}

// start boots the api, waits for it to report ready, then starts the worker.
//
// Order matters for what the user sees: the browser can be opened as soon as
// the api answers, and the worker's background sweeps do not gate a usable UI.
func (b *backend) start(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	b.port = port

	env := b.childEnv()
	apiArgs := append([]string{
		"--addr", fmt.Sprintf("127.0.0.1:%d", port),
		"--dsn", b.pg.appDSN(),
		// The owner pool is what makes the custom-fields schema operations
		// answer rather than 501; a desktop install has no DBA to run them.
		"--schema-dsn", b.pg.ownerDSN(),
		"--config", b.layout.configPath(),
		"--redis", b.bus.addr(),
	}, b.aiFlags()...)

	api, err := startChild("api", b.layout.appBin("api"), apiArgs, env, b.layout.root, b.layout.logs())
	if err != nil {
		return err
	}
	b.api = api

	if err := waitUntil(ctx, "api", 120*time.Second, api.exited, func() error {
		return httpOK(b.baseURL() + "/readyz")
	}); err != nil {
		return err
	}

	// The worker owns the automation time-scan and the retention sweeps; the
	// api's inline relay and the worker's standalone relay coexist because
	// outbox rows are claimed FOR UPDATE SKIP LOCKED.
	workerArgs := append([]string{
		"--dsn", b.pg.appDSN(),
		"--config", b.layout.configPath(),
		"--redis", b.bus.addr(),
		"--retention-interval", "24h",
	}, b.aiFlags()...)

	worker, err := startChild("worker", b.layout.appBin("worker"), workerArgs, env, b.layout.root, b.layout.logs())
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
