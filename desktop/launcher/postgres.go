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

// Roles and database, matching scripts/db-init.sql: margince_app is the
// runtime role — non-superuser, no BYPASSRLS, not the table owner — so RLS
// binds it with no exception path. Connecting as the owner at runtime would
// silently disable every row-level policy in the schema.
const (
	ownerRole    = "margince_owner"
	appRole      = "margince_app"
	databaseName = "margince"
)

// maxUnixSocketPath is the hard limit on a unix-domain socket path.
//
// Postgres composes <dir>/.s.PGSQL.<port>, and the kernel's sockaddr_un is
// 104 bytes including the terminator. Since the socket lives inside the
// installation folder, how deeply the user unpacked that folder decides
// whether the database can start at all. This is not hypothetical: it is the
// first failure this bundle hit.
const maxUnixSocketPath = 103

type postgres struct {
	layout    layout
	socketDir string
	proc      *child
}

func newPostgres(l layout) (*postgres, error) {
	socketDir, err := resolveSocketDir(l)
	if err != nil {
		return nil, err
	}
	return &postgres{layout: l, socketDir: socketDir}, nil
}

// resolveSocketDir keeps the socket inside the installation folder, like
// every other piece of runtime state.
//
// There is deliberately no fallback to /tmp. Escaping the folder would leave
// one process's runtime state outside the directory the user can see, move
// and delete — which is the whole property this layout exists to have. When
// the path does not fit, that is a real limit the user must act on, and the
// error says exactly what to do rather than hiding the problem somewhere they
// will never look.
func resolveSocketDir(l layout) (string, error) {
	dir := l.sockets()
	if path := socketPath(dir); len(path) > maxUnixSocketPath {
		return "", fmt.Errorf(
			"the installation folder is too deeply nested: the database socket path would be %d bytes and the system limit is %d.\n"+
				"Move the Margince folder somewhere shorter (for example ~/Margince) and start it again.\n"+
				"  path: %s",
			len(path), maxUnixSocketPath, path)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the socket directory %s: %w", dir, err)
	}
	return dir, nil
}

func socketPath(dir string) string {
	return filepath.Join(dir, ".s.PGSQL.5432")
}

// ensureCluster initialises the data directory on first launch. An existing
// cluster is left untouched — this runs on every start, and the user's data
// is the thing it must never re-create.
func (p *postgres) ensureCluster() error {
	versionFile := filepath.Join(p.layout.pgData(), "PG_VERSION")
	if _, err := os.Stat(versionFile); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", versionFile, err)
	}

	// --no-locale selects the C locale, which is the only one guaranteed to
	// exist identically on every Mac; combined with UTF8 it gives byte-order
	// collation. See the design note on collation before shipping: names with
	// diacritics sort by byte value under this setting.
	cmd := exec.Command(p.layout.pgBin("initdb"),
		"-D", p.layout.pgData(),
		"-U", ownerRole,
		"--no-locale",
		"--encoding=UTF8",
		"--auth=trust",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb failed: %w\n%s", err, out)
	}
	return nil
}

// start launches the postmaster and waits for it to accept connections.
func (p *postgres) start(ctx context.Context) error {
	if err := p.ensureCluster(); err != nil {
		return err
	}
	// listen_addresses='' removes the TCP listener entirely: the database is
	// reachable only through a socket in a 0700 directory, so no port can
	// collide and no other account on the Mac can reach it.
	proc, err := startChild("postgres", p.layout.pgBin("postgres"), []string{
		"-D", p.layout.pgData(),
		"-k", p.socketDir,
		"-c", "listen_addresses=",
	}, nil, p.layout.root, p.layout.logs())
	if err != nil {
		return err
	}
	p.proc = proc

	return waitUntil(ctx, "postgres", 60*time.Second, proc.exited, func() error {
		return exec.Command(p.layout.pgBin("pg_isready"),
			"-h", p.socketDir, "-U", ownerRole).Run()
	})
}

// ensureSchema creates the runtime role and the database if they are absent.
// Both checks are existence-guarded rather than error-tolerant, so a genuine
// failure surfaces instead of being mistaken for "already there".
func (p *postgres) ensureSchema() error {
	exists, err := p.queryBool(fmt.Sprintf("SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s')", appRole))
	if err != nil {
		return fmt.Errorf("check for the %s role: %w", appRole, err)
	}
	if !exists {
		if err := p.execSQL("postgres", fmt.Sprintf("CREATE ROLE %s LOGIN", appRole)); err != nil {
			return fmt.Errorf("create the %s role: %w", appRole, err)
		}
	}

	exists, err = p.queryBool(fmt.Sprintf("SELECT EXISTS (SELECT FROM pg_database WHERE datname = '%s')", databaseName))
	if err != nil {
		return fmt.Errorf("check for the %s database: %w", databaseName, err)
	}
	if !exists {
		if err := p.execSQL("postgres", fmt.Sprintf("CREATE DATABASE %s OWNER %s", databaseName, ownerRole)); err != nil {
			return fmt.Errorf("create the %s database: %w", databaseName, err)
		}
	}
	return nil
}

func (p *postgres) psql(database string, args ...string) *exec.Cmd {
	base := []string{"-h", p.socketDir, "-U", ownerRole, "-d", database, "-v", "ON_ERROR_STOP=1"}
	return exec.Command(p.layout.pgBin("psql"), append(base, args...)...)
}

func (p *postgres) execSQL(database, sql string) error {
	if out, err := p.psql(database, "-q", "-c", sql).CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func (p *postgres) queryBool(sql string) (bool, error) {
	out, err := p.psql("postgres", "-tAc", sql).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "t", nil
}

// dsn builds a connection string for role over the unix socket. Trust auth on
// a 0700 socket directory means no password is exchanged; the filesystem is
// the access control, which on a single-user desktop is strictly stronger
// than a password stored next to the data it protects.
func (p *postgres) dsn(role string) string {
	return fmt.Sprintf("postgres://%s@/%s?host=%s", role, databaseName, p.socketDir)
}

func (p *postgres) ownerDSN() string { return p.dsn(ownerRole) }
func (p *postgres) appDSN() string   { return p.dsn(appRole) }

// stop performs a fast shutdown.
//
// SIGINT, not SIGTERM: Postgres reads SIGTERM as "smart shutdown" and waits
// for every client to disconnect first, which never happens while a pooled
// connection is still open — the app would hang on quit. SIGINT rolls back
// in-flight transactions and closes cleanly, which is what a desktop quit
// means. SIGQUIT would be faster but leaves recovery work for the next launch.
func (p *postgres) stop() error {
	return p.proc.stop(syscall.SIGINT, 30*time.Second)
}
