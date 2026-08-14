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

// maxUnixSocketPath is the hard limit on a unix-domain socket path.
//
// Postgres composes <dir>/.s.PGSQL.<port>, and the kernel's sockaddr_un is
// 104 bytes including the terminator. Since the socket lives inside the
// installation folder, how deeply the user unpacked that folder decides
// whether the database can start at all. This is not hypothetical: it is the
// first failure this bundle hit.
const maxUnixSocketPath = 103

// postgres is the macOS cluster: a postmaster supervised as a child process,
// reachable only over a unix socket inside the installation folder.
type postgres struct {
	cluster
	socketDir string
	proc      *child
}

// sockets is the socket directory. It is macOS-only state: Windows Postgres
// has no unix-socket transport at all.
func (l layout) sockets() string { return filepath.Join(l.data(), "sockets") }

func newPostgres(l layout) (*postgres, error) {
	socketDir, err := resolveSocketDir(l)
	if err != nil {
		return nil, err
	}
	return &postgres{
		// Trust auth over a 0700 socket directory means no password is
		// exchanged; the filesystem is the access control, which on a
		// single-user Mac is strictly stronger than a password stored next to
		// the data it protects. That is why connEnv and appRoleOptions are
		// both empty here and both filled in on Windows.
		cluster: cluster{
			layout:   l,
			connArgs: []string{"-h", socketDir, "-U", ownerRole},
		},
		socketDir: socketDir,
	}, nil
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
			len(path), maxUnixSocketPath, path,
		)
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
	fresh, err := needsInitdb(p.layout)
	if err != nil || !fresh {
		return err
	}

	// --no-locale selects the C locale, which is the only one guaranteed to
	// exist identically on every Mac; combined with UTF8 it gives byte-order
	// collation. See the design note on collation before shipping: names with
	// diacritics sort by byte value under this setting.
	return initCluster(p.layout, func(dataDir string) *exec.Cmd {
		return exec.Command(
			p.layout.pgBin("initdb"),
			"-D", dataDir,
			"-U", ownerRole,
			"--no-locale",
			"--encoding=UTF8",
			"--auth=trust",
		)
	})
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

// dsn builds a connection string for role over the unix socket. No password
// appears in it because none is exchanged — see newPostgres.
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
