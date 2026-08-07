// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command Margince supervises the bundled Margince stack as a desktop app.
//
// It starts Postgres, the event bus, the api and the worker as child
// processes, brings the schema to head, and holds them until the user quits.
// Nothing here imports the backend: the shipped binaries run unmodified, so
// this launcher is a supervisor, not a second composition root.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// stack is every service the bundle runs, in start order. Shutdown walks it
// in reverse: the database is the last thing to stop, because the processes
// holding connections to it must be gone first for a fast shutdown to be fast.
type stack struct {
	layout layout
	pg     *postgres
	bus    *valkey
	be     *backend
	web    *ui
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "margince: %v\n", err)
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
	adminPassword, err := layout.ensureConfig()
	if err != nil {
		return err
	}

	s := &stack{layout: layout}
	if err := s.start(ctx); err != nil {
		// A partial start still leaves processes running; tearing down before
		// reporting is what keeps a failed launch from holding the data
		// directory hostage on the next attempt.
		if stopErr := s.stop(); stopErr != nil {
			return fmt.Errorf("%w (and shutting down afterwards failed: %v)", err, stopErr)
		}
		return err
	}

	announce(s.web.baseURL(), layout, adminPassword)

	<-ctx.Done()
	fmt.Println("\nshutting down…")
	return s.stop()
}

func (s *stack) start(ctx context.Context) error {
	pg, err := newPostgres(s.layout)
	if err != nil {
		return err
	}
	s.pg = pg
	fmt.Println("starting database…")
	if err := pg.start(ctx); err != nil {
		return err
	}
	if err := pg.ensureSchema(); err != nil {
		return err
	}

	s.bus = &valkey{layout: s.layout}
	fmt.Println("starting event bus…")
	if err := s.bus.start(ctx); err != nil {
		return err
	}

	s.be = &backend{layout: s.layout, pg: pg, bus: s.bus}
	fmt.Println("applying migrations…")
	if err := s.be.migrate(); err != nil {
		return err
	}
	fmt.Println("starting application…")
	if err := s.be.start(ctx); err != nil {
		return err
	}

	s.web = &ui{layout: s.layout, apiURL: s.be.baseURL()}
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

// announce prints where the app is and, on the very first launch only, the
// generated credentials. The password is shown once because that is the only
// moment it is not yet the user's responsibility to have stored.
// readyPrefix is the machine-readable handshake the window process waits for.
//
// The GUI cannot open until it knows the ephemeral port, and parsing prose
// meant for a human would break the window the first time that prose is
// reworded. This line is a contract: prefix, space, URL, newline.
const readyPrefix = "MARGINCE_READY"

func announce(baseURL string, l layout, adminPassword string) {
	fmt.Printf("%s %s\n", readyPrefix, baseURL)
	fmt.Printf("\nMargince is running at %s\n", baseURL)
	fmt.Printf("data:  %s\n", l.data)
	fmt.Printf("logs:  %s\n", l.logs())
	if adminPassword != "" {
		fmt.Printf("\nFirst launch — sign in with:\n  email:    owner@margince.local\n  password: %s\n", adminPassword)
		fmt.Printf("(also saved to %s)\n", l.adminPasswordPath())
	}
	fmt.Println("\nPress Ctrl-C to quit.")
}
