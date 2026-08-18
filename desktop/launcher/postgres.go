// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// cluster is how the launcher TALKS to a running database.
//
// How the postmaster is STARTED and STOPPED is the part the two platforms
// genuinely disagree about — macOS gets a unix socket and a supervised child,
// Windows a loopback port and pg_ctl — so that lives in postgres_darwin.go and
// postgres_windows.go. Everything below is the same question on both: run this
// SQL against the database this launcher just started.
//
// The three fields are what the platform files fill in. They exist so the SQL
// helpers never have to ask which OS they are on.
type cluster struct {
	layout layout

	// connArgs is the host/port/user half of every psql invocation, spelled
	// once so no caller can reach a DIFFERENT postgres than the one this
	// launcher started.
	connArgs []string

	// connEnv carries PGPASSWORD where the platform authenticates by password.
	// It is empty on macOS, where the connection is a unix socket in a 0700
	// directory and no password is exchanged at all.
	connEnv []string

	// appRoleOptions is appended to CREATE ROLE for the runtime role: empty
	// where trust auth applies, a PASSWORD clause where the connection is a
	// TCP one that has to authenticate.
	appRoleOptions string
}

// needsInitdb reports whether the data directory still has to be created.
//
// Both platforms run this on every start, so the question it answers is the
// one that matters: the user's existing cluster must never be re-created.
//
// PG_VERSION is a sound marker ONLY because initCluster below moves a finished
// cluster into place in one step. initdb itself writes that file first, not
// last — "Top level PG_VERSION is checked by bootstrapper, so make it first",
// src/bin/initdb/initdb.c — so in a directory initdb wrote in place, its
// presence would mean initdb *started*, and a run interrupted a second later
// would leave a half-built cluster that this function called finished.
func needsInitdb(l layout) (bool, error) {
	versionFile := filepath.Join(l.pgData(), "PG_VERSION")
	if _, err := os.Stat(versionFile); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", versionFile, err)
	}
	return true, nil
}

// pgDataStaging is where a new cluster is built before it becomes the real one.
// It is always this launcher's own scratch space and never the user's data.
func (l layout) pgDataStaging() string { return l.pgData() + ".incomplete" }

// initCluster runs initdb into a staging directory and moves it into place
// only after it succeeds, so the data directory either does not exist or holds
// a cluster initdb finished.
//
// The alternative — initdb straight into data/pg — has no way to tell a
// finished cluster from one whose creation was interrupted by a closed lid or
// a power cut, and the failure surfaces later as a database that will not
// start, at which point the user is asked to interpret a bootstrap error.
// A rename within one directory is atomic, so the ambiguity never exists.
func initCluster(l layout, build func(dataDir string) *exec.Cmd) error {
	staging := l.pgDataStaging()
	// Left behind by an interrupted run. Removing it is safe precisely because
	// nothing has ever been moved into place from it.
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("clear the incomplete database directory %s: %w", staging, err)
	}

	if out, err := build(staging).CombinedOutput(); err != nil {
		return fmt.Errorf("initdb failed: %w\n%s", explainStartFailure("the database initialiser", err), out)
	}
	if err := os.Rename(staging, l.pgData()); err != nil {
		return fmt.Errorf("move the new database into place: %w", err)
	}
	return nil
}

// ensureSchema creates the runtime role and the database if they are absent.
// Both checks are existence-guarded rather than error-tolerant, so a genuine
// failure surfaces instead of being mistaken for "already there".
func (c cluster) ensureSchema() error {
	exists, err := c.queryBool(fmt.Sprintf("SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s')", appRole))
	if err != nil {
		return fmt.Errorf("check for the %s role: %w", appRole, err)
	}
	if !exists {
		if err := c.execSQL("postgres", "CREATE ROLE "+appRole+" LOGIN"+c.appRoleOptions); err != nil {
			return fmt.Errorf("create the %s role: %w", appRole, err)
		}
	}

	exists, err = c.queryBool(fmt.Sprintf("SELECT EXISTS (SELECT FROM pg_database WHERE datname = '%s')", databaseName))
	if err != nil {
		return fmt.Errorf("check for the %s database: %w", databaseName, err)
	}
	if !exists {
		if err := c.execSQL("postgres", fmt.Sprintf("CREATE DATABASE %s OWNER %s", databaseName, ownerRole)); err != nil {
			return fmt.Errorf("create the %s database: %w", databaseName, err)
		}
	}
	return nil
}

func (c cluster) psql(database string, args ...string) *exec.Cmd {
	base := append(append([]string{}, c.connArgs...), "-d", database, "-v", "ON_ERROR_STOP=1")
	// #nosec G204 -- psql ships in runtime/pgsql/bin and is addressed by absolute path; the SQL is this file's own literals
	cmd := exec.Command(c.layout.pgBin("psql"), append(base, args...)...)
	if len(c.connEnv) > 0 {
		cmd.Env = append(os.Environ(), c.connEnv...)
	}
	return cmd
}

func (c cluster) execSQL(database, sql string) error {
	if out, err := c.psql(database, "-q", "-c", sql).CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func (c cluster) queryBool(sql string) (bool, error) {
	out, err := c.psql("postgres", "-tAc", sql).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "t", nil
}
