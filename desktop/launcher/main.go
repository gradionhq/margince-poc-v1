// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command margince runs the whole Margince stack from one folder.
//
// It starts Postgres, the event bus, the api and the worker as child
// processes, brings the schema to head, serves the web UI, and holds them
// until the user quits. Nothing here imports the backend: the shipped
// binaries run unmodified, so this is a supervisor, not a second composition
// root.
//
// Everything it reads and writes is relative to the folder it lives in, so
// the installation can be moved, copied or deleted as a unit.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// defaultPort is where the browser goes. Fixed rather than ephemeral so a
// bookmark survives a restart; MARGINCE_PORT in margince.env overrides it.
const defaultPort = 8800

// stack is every service, in start order. Shutdown walks it in reverse: the
// database stops last, because the processes holding connections to it must
// be gone first for a fast shutdown to actually be fast.
type stack struct {
	layout layout
	pg     *postgres
	bus    *eventBus
	be     *backend
	web    *ui
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "\nMargince could not start:\n%v\n", err)
		holdConsole()
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	layout, err := resolveLayout()
	if err != nil {
		return err
	}
	if err := ensureEnvFile(layout.envPath()); err != nil {
		return err
	}
	userEnv, err := loadEnvFile(layout.envPath())
	if err != nil {
		return err
	}
	adminPassword, err := layout.ensureConfig()
	if err != nil {
		return err
	}
	port, err := resolvePort(userEnv)
	if err != nil {
		return err
	}

	s := &stack{layout: layout}
	if err := s.start(ctx, userEnv, port); err != nil {
		// A partial start still leaves processes running; tearing down before
		// reporting is what keeps a failed launch from holding the data
		// directory hostage on the next attempt.
		if stopErr := s.stop(); stopErr != nil {
			return fmt.Errorf("%w\n(shutting down afterwards also failed: %v)", err, stopErr)
		}
		return err
	}

	announce(s.web.baseURL(), layout, adminPassword)
	openBrowser(s.web.baseURL())

	<-ctx.Done()
	say("\nShutting down…\n")
	if err := s.stop(); err != nil {
		return err
	}
	say("Margince has stopped. Your data is safe in the data folder.\n")
	return nil
}

// resolvePort reads MARGINCE_PORT from the user's settings.
//
// A value the OS would reject must fail here, with the file and key named,
// rather than surfacing later as an opaque listen error.
func resolvePort(userEnv []string) (int, error) {
	for _, entry := range userEnv {
		value, ok := strings.CutPrefix(entry, "MARGINCE_PORT=")
		if !ok || value == "" {
			continue
		}
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("MARGINCE_PORT in margince.env must be a number between 1 and 65535, got %q", value)
		}
		return port, nil
	}
	return defaultPort, nil
}

func (s *stack) start(ctx context.Context, userEnv []string, port int) error {
	pg, err := newPostgres(s.layout)
	if err != nil {
		return err
	}
	s.pg = pg
	say("Starting database…\n")
	if err := pg.start(ctx); err != nil {
		return err
	}
	if err := pg.ensureSchema(); err != nil {
		return err
	}

	s.bus = &eventBus{layout: s.layout}
	say("Starting event bus…\n")
	if err := s.bus.start(ctx); err != nil {
		return err
	}

	s.be = &backend{layout: s.layout, pg: pg, bus: s.bus, userEnv: userEnv}
	say("Updating database schema…\n")
	if err := s.be.migrate(); err != nil {
		return err
	}
	say("Starting Margince…\n")
	if err := s.be.start(ctx); err != nil {
		return err
	}

	s.web = &ui{layout: s.layout, apiURL: s.be.baseURL(), port: port}
	return s.web.start(ctx)
}

// stop tears the stack down in reverse start order, collecting failures
// instead of returning at the first one — a service that will not stop must
// not prevent the rest from being asked to.
func (s *stack) stop() error {
	var errs []error
	// The web server stops first so no new request reaches a service that is
	// already tearing down.
	if s.web != nil {
		if err := s.web.stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.be != nil {
		if err := s.be.stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.bus != nil {
		if err := s.bus.stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.pg != nil {
		if err := s.pg.stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// announce tells the user where Margince is and, on the very first launch
// only, how to sign in. The password is shown once because that is the only
// moment it is not yet the user's responsibility to have stored.
func announce(baseURL string, l layout, adminPassword string) {
	say("\n  Margince is running at  %s\n", baseURL)

	// Every launch says how to sign in, not just the first. A user who
	// closed the window on the first run would otherwise have no way to
	// discover where the credentials went — and the file is the only copy.
	say("\n  Sign in as  owner@margince.local\n")
	if adminPassword != "" {
		say("  Password    %s\n", adminPassword)
		say("              (shown once — also saved in data/admin-password)\n")
	} else {
		say("  Password    see data/admin-password\n")
	}

	say("\n  Folder    %s\n", l.root)
	say("  Settings  margince.env      turn on AI, email capture, attachments\n")
	say("  Company   margince.yaml     name, currency, timezone\n")
	say("  Logs      data/logs/\n")
	say("\n  Press Ctrl-C to stop.\n\n")
}
