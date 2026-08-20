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

// eventBus is the event bus behind platform/events — Valkey on macOS, Redis
// 7.2 on Windows, which has no Valkey build. Both speak the same protocol and
// take the same flags, so only the executable's name differs (busBinary, in
// the platform_*.go file that explains the choice).
type eventBus struct {
	layout layout
	port   int
	proc   *child
}

func (b *eventBus) addr() string { return fmt.Sprintf("%s:%d", loopbackHost, b.port) }

// start launches the bus on loopback at an ephemeral port.
//
// Loopback rather than a unix socket because the shipped api and worker build
// their client with redis.Options{Addr}, which speaks TCP — reaching the bus
// over a socket would mean changing product code the bundle is meant to run
// unmodified. The port is ephemeral because nothing outside this installation
// ever addresses it.
func (b *eventBus) start(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	b.port = port

	dir := filepath.Join(b.layout.data(), "bus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the event bus directory: %w", err)
	}

	// Persistence stays ON. The outbox in Postgres is the durable record, but
	// consumer-group offsets and the events.Dedupe keys live here — losing
	// them across a restart turns at-least-once delivery into visibly
	// duplicated work for the user.
	proc, err := startChild("bus", b.layout.appBin(busBinary), []string{
		"--port", fmt.Sprintf("%d", port),
		"--bind", loopbackHost,
		"--dir", dir,
		"--appendonly", "yes",
	}, nil, b.layout.root, b.layout.logs())
	if err != nil {
		return err
	}
	b.proc = proc

	return explainNotReady(waitUntil(ctx, "bus", 30*time.Second, proc.exited, func() error {
		return dialTCP(b.addr())
	}), proc)
}

func (b *eventBus) stop() error { return b.proc.stop(syscall.SIGTERM, 15*time.Second) }

// backend runs the two process roles the server is split into.
type backend struct {
	layout  layout
	pg      *postgres
	bus     *eventBus
	userEnv []string
	port    int
	api     *child
	worker  *child
}

func (b *backend) baseURL() string { return fmt.Sprintf("http://%s:%d", loopbackHost, b.port) }

// migrate brings the schema to head before either role starts. It runs with
// the owner DSN — the app role deliberately cannot alter the schema it is
// bound by.
func (b *backend) migrate() error {
	// #nosec G204 -- migrate is a shipped binary addressed by absolute path; the DSN travels in the environment, not argv
	cmd := exec.Command(b.layout.appBin("migrate"), "up")
	cmd.Env = append(os.Environ(), "MARGINCE_OWNER_DSN="+b.pg.ownerDSN())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("migrate failed: %w\n%s", err, out)
	}
	return nil
}

// childEnv is the environment a role inherits, in three layers: what this
// launcher DEFAULTS, what the user sets, and what the launcher OWNS outright.
//
// EVERY DSN is in the third layer rather than on the command line. A Windows DSN
// carries the database password, and any local process can read another
// process's arguments — the same reason initdb is handed its password in a file
// (postgres_windows.go). An environment is not secret either, but reading
// another process's takes more than `ps`. Nothing in margince.env can displace
// them, because they come last and the last value of a duplicated key wins.
//
// MARGINCE_ENV is in the FIRST layer, and that is a deliberate reversal.
//
// A serving role boots on a license or it does not boot, and MARGINCE_ENV is
// fail-closed: an installation that names nothing is production and is held to a
// license it was never issued. So a desktop bundle pinned to production could
// not start at all — which is what happened, with the reason buried in api.log.
//
// `dev` is what a single person running their own copy actually is. What it
// costs is narrow and worth naming precisely, because the obvious fear is
// wrong: it does NOT arm the admin data-reset endpoint. That needs
// operations.allow_data_reset in margince.yaml, which layout.go never writes,
// and the posture alone leaves it a 404. What the posture does change is that
// /me reports non_production, and that a license minted by a test authority
// would be honoured — this installation has neither.
//
// It is a default rather than a decision, so an operator who HAS a license can
// put MARGINCE_ENV=production in margince.env and be held to it.
func (b *backend) childEnv(launcherOwned ...string) []string {
	env := []string{"MARGINCE_ENV=" + defaultRuntimeEnv}
	env = append(env, b.userEnv...)
	return append(env, launcherOwned...)
}

// defaultRuntimeEnv is the posture a desktop installation takes unless its
// margince.env names another. The value is the one the api parses; anything it
// does not recognise means production, so a typo here would silently demand a
// license.
const defaultRuntimeEnv = "dev"

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

	apiArgs := append([]string{
		"--addr", fmt.Sprintf("%s:%d", loopbackHost, port),
		"--config", b.layout.configPath(),
		"--redis", b.bus.addr(),
	}, b.aiFlags()...)

	// The owner pool is what makes the custom-fields schema operations answer
	// rather than 501; a desktop install has no DBA to run them. The worker
	// below is not given it — it has no schema work to do.
	apiEnv := b.childEnv(
		"MARGINCE_DSN="+b.pg.appDSN(),
		"MARGINCE_SCHEMA_DSN="+b.pg.ownerDSN(),
	)

	api, err := startChild("api", b.layout.appBin("api"), apiArgs, apiEnv, b.layout.root, b.layout.logs())
	if err != nil {
		return err
	}
	b.api = api

	if err := waitUntil(ctx, "api", 120*time.Second, api.exited, func() error {
		return httpOK(b.baseURL() + "/readyz")
	}); err != nil {
		return explainNotReady(err, api)
	}

	// The worker owns the automation time-scan and the retention sweeps; the
	// api's inline relay and the worker's standalone relay coexist because
	// outbox rows are claimed FOR UPDATE SKIP LOCKED.
	workerArgs := append([]string{
		"--config", b.layout.configPath(),
		"--redis", b.bus.addr(),
		"--retention-interval", "24h",
	}, b.aiFlags()...)

	workerEnv := b.childEnv("MARGINCE_DSN=" + b.pg.appDSN())
	worker, err := startChild("worker", b.layout.appBin("worker"), workerArgs, workerEnv, b.layout.root, b.layout.logs())
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
