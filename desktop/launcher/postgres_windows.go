// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Postgres on Windows differs from the macOS path in three ways, all of them
// forced rather than chosen:
//
//  1. There is no unix socket. Windows Postgres speaks TCP only, so the
//     cluster listens on loopback and every connection authenticates with
//     scram-sha-256 against a generated password. macOS can use a 0700
//     directory as the access control; here the password IS the access
//     control, and trust auth would open the database to every other account
//     on the machine.
//  2. The postmaster is started through pg_ctl rather than as a direct child.
//     postgres.exe refuses to run under an account holding administrative
//     rights — "Execution of PostgreSQL by a user with administrative
//     permissions is not permitted" — and pg_ctl is what creates the
//     restricted process that drops them. Launching postgres.exe ourselves
//     would work for a standard account and fail for an administrator, which
//     is the kind of split that only ever shows up on someone else's machine.
//  3. Shutdown is `pg_ctl stop -m fast`, because the process is pg_ctl's and
//     not ours. It means what SIGINT means on macOS: roll back what is in
//     flight and close cleanly, rather than wait for every client to go away.
//
// Losing the socket also removes the 103-byte path ceiling the macOS bundle
// has to police, so a Windows installation may sit wherever the user put it.

// pgCtlNoServer is the exit status pg_ctl uses for "the data directory is
// there but nothing is running" — the ordinary case on a clean start, and the
// one reading that must not be mistaken for a failure to ask the question.
const pgCtlNoServer = 3

type postgres struct {
	cluster
	port          int
	ownerPassword string
	appPassword   string
}

// dbPasswordPath is where a role's generated password lives. It is Windows-only
// state: on macOS no password exists to store.
func (l layout) dbPasswordPath(role string) string {
	return filepath.Join(l.data(), "db-"+role+"-password")
}

func newPostgres(l layout) (*postgres, error) {
	ownerPassword, err := readOrCreateSecret(l.dbPasswordPath(ownerRole))
	if err != nil {
		return nil, err
	}
	appPassword, err := readOrCreateSecret(l.dbPasswordPath(appRole))
	if err != nil {
		return nil, err
	}

	return &postgres{
		cluster: cluster{
			layout:  l,
			connEnv: []string{"PGPASSWORD=" + ownerPassword},
			// A single-quoted SQL literal is safe here because the generator
			// emits base64url — letters, digits, '-' and '_' — so the value
			// cannot contain a quote or a backslash to break out with.
			appRoleOptions: " PASSWORD '" + appPassword + "'",
		},
		ownerPassword: ownerPassword,
		appPassword:   appPassword,
	}, nil
}

// readOrCreateSecret returns the secret at path, generating and storing one on
// first use.
//
// Unlike the bootstrap admin password, this value is read back on EVERY
// launch: it is how the launcher authenticates to the database it started, so
// forgetting it would lock the installation out of its own data.
func readOrCreateSecret(path string) (string, error) {
	existing, err := os.ReadFile(path)
	if err == nil {
		return string(existing), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	secret, err := generateSecret()
	if err != nil {
		return "", fmt.Errorf("generate the database password: %w", err)
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return secret, nil
}

// ensureCluster initialises the data directory on first launch. An existing
// cluster is left untouched — this runs on every start, and the user's data
// is the thing it must never re-create.
func (p *postgres) ensureCluster() (err error) {
	fresh, err := needsInitdb(p.layout)
	if err != nil || !fresh {
		return err
	}

	// The superuser password reaches initdb through a file rather than an
	// argument: a command line is readable by every process on the machine,
	// and this one would be the credential to the whole database.
	pwFile := filepath.Join(p.layout.data(), "initdb-password")
	if err := os.WriteFile(pwFile, []byte(p.ownerPassword), 0o600); err != nil {
		return fmt.Errorf("write the temporary initdb password file: %w", err)
	}
	defer func() {
		// The file has served its only purpose; leaving a second copy of the
		// database superuser password on disk is not something to shrug at,
		// so a failure to remove it fails the start.
		if removeErr := os.Remove(pwFile); removeErr != nil && err == nil {
			err = fmt.Errorf("remove the temporary initdb password file: %w", removeErr)
		}
	}()

	// --no-locale gives byte-order collation, matching the macOS bundle so the
	// two platforms sort identically. scram-sha-256 rather than trust because
	// the connection is TCP: see the note at the top of this file.
	cmd := exec.Command(
		p.layout.pgBin("initdb"),
		"-D", p.layout.pgData(),
		"-U", ownerRole,
		"--no-locale",
		"--encoding=UTF8",
		"--auth=scram-sha-256",
		"--pwfile="+pwFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb failed: %w\n%s", err, out)
	}
	return nil
}

// pgCtl builds a pg_ctl invocation against this installation's data directory.
func (p *postgres) pgCtl(args ...string) *exec.Cmd {
	return exec.Command(p.layout.pgBin("pg_ctl"),
		append([]string{"-D", p.layout.pgData()}, args...)...)
}

// stopStray shuts down a postmaster left running by a previous launch.
//
// On macOS the postmaster is a child and dies with the launcher. Here it is
// pg_ctl's, so a launcher killed from Task Manager — or a machine powered off
// mid-session — can leave one holding the data directory. Without this, the
// next launch would fail forever with a lock-file message the user has no way
// to interpret, and the only fix would be a reboot.
func (p *postgres) stopStray() error {
	out, err := p.pgCtl("status").CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == pgCtlNoServer {
			return nil
		}
		// Anything else — an unreadable data directory, a pg_ctl that is not
		// there — is a real fault, and one the start below would report as a
		// confusing secondary failure if it were passed over here.
		return fmt.Errorf("check whether a database is already running: %w\n%s", err, out)
	}

	if out, err := p.pgCtl("-m", "fast", "-w", "-t", "30", "stop").CombinedOutput(); err != nil {
		return fmt.Errorf(
			"a database from a previous session is still running and could not be stopped: %w\n%s\n"+
				"Sign out and back in, or restart the computer, and start Margince again.",
			err, out)
	}
	return nil
}

// start brings the postmaster up and waits for it to accept connections.
func (p *postgres) start(ctx context.Context) error {
	if err := p.ensureCluster(); err != nil {
		return err
	}
	if err := p.stopStray(); err != nil {
		return err
	}
	if err := os.MkdirAll(p.layout.logs(), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	// The port is ephemeral like the bus's and the api's: nothing outside this
	// installation addresses the database, and a fixed one would collide with
	// whatever else on the machine already answers there.
	//
	// It is reserved HERE and not in newPostgres, because a reserved port is
	// only nominally free — the window between releasing it and pg_ctl binding
	// it is the window something else can take it. Choosing it before initdb
	// would stretch that window across the whole first-run cluster creation.
	port, err := freePort()
	if err != nil {
		return err
	}
	p.port = port
	p.connArgs = []string{"-h", loopbackHost, "-p", strconv.Itoa(port), "-U", ownerRole}

	// listen_addresses is pinned to loopback: the database must be reachable
	// by this installation's own processes and by nothing on the network.
	options := fmt.Sprintf("-c listen_addresses=%s -p %d", loopbackHost, p.port)
	logPath := filepath.Join(p.layout.logs(), "postgres.log")
	if out, err := p.pgCtl("-l", logPath, "-o", options, "-w", "-t", "60", "start").CombinedOutput(); err != nil {
		return fmt.Errorf("could not start the database: %w\n%s\nSee %s", err, out, logPath)
	}

	// pg_ctl -w already waited, so this normally passes first try. It stays
	// because "pg_ctl returned" and "the database answers" are not the same
	// claim, and every later step assumes the second one.
	return waitUntil(ctx, "postgres", 30*time.Second, nil, func() error {
		return exec.Command(p.layout.pgBin("pg_isready"),
			"-h", loopbackHost, "-p", strconv.Itoa(p.port), "-U", ownerRole).Run()
	})
}

// dsn builds a connection string for role.
//
// sslmode=disable is not a weakening: the cluster has no TLS configured and
// never leaves the loopback interface, so the alternative is a negotiation
// round trip that always ends in the same place.
func (p *postgres) dsn(role, password string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(role, password),
		Host:     net.JoinHostPort(loopbackHost, strconv.Itoa(p.port)),
		Path:     "/" + databaseName,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func (p *postgres) ownerDSN() string { return p.dsn(ownerRole, p.ownerPassword) }
func (p *postgres) appDSN() string   { return p.dsn(appRole, p.appPassword) }

// stop performs a fast shutdown — the same intent as SIGINT on macOS, spelled
// the way pg_ctl spells it.
func (p *postgres) stop() error {
	if out, err := p.pgCtl("-m", "fast", "-w", "-t", "30", "stop").CombinedOutput(); err != nil {
		return fmt.Errorf("stop the database: %w\n%s", err, out)
	}
	return nil
}
