// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// THE GATE'S PREMISE, HELD FOR ITS OWN TESTS.
//
// extmigrategate applies a unit's migrations as the unit's ext_<name> role and
// then asserts what schema ext holds — every object in it must be one the unit
// under test just created and owns ("the database is thrown away after one run
// and always starts empty", main.go). Everything downstream of that rests on
// it: an object the unit does not own is an object its own down-migration
// cannot revert, so the gate refuses it, by design.
//
// The integration lane hands every package a clone of the shared margince_test
// template, and since cmd/migrate began applying the composed set (Task 8) that
// template carries ext.ext_<name>_* for every enabled unit that ships a
// migrations layer — crm-demo's ext_crm_demo_note today. A clone of it is
// therefore precisely the database the gate is documented not to run on, and
// the tests saw it: the isolation acceptance test refused a correct migration,
// and the FOREIGN TABLE / MATERIALIZED VIEW refusals fired on crm-demo's table
// instead of on the relation the test had planted, so they asserted a message
// about the wrong object. Task 8's review predicted exactly this ("the clone
// always starts empty invariant becomes violable once a unit ships migrations")
// and fixed the production caller, scripts/check-ext-migrations.sh, the same
// way this fixes the test one.
//
// The fix is the same reasoning, not a second mechanism: this package MIGRATES
// ITS OWN throwaway database rather than reverting the template clone's
// extension objects out of the way. Dropping them afterwards would re-implement
// what core's ext-schema migration establishes, and would need extending every
// time an extension namespace grows a kind of object; migrating a fresh
// database makes the premise structurally true instead of restored-after-the-
// fact, and it removes the shared template from the gate's dependencies
// entirely — so the result no longer depends on whether `make test-db-up` or
// the integration lane touched the template last, and the suite is green on a
// second consecutive run with no database reset in between.
//
// VANILLA BY CONSTRUCTION, not by configuration. check-ext-migrations.sh pins
// GOWORK to the root workspace so `go run ./cmd/migrate` resolves the vanilla
// composition stub. Here there is nothing to pin: the database is brought to
// head by testdb.EnsureSchema, the integration lane's one migrate-once
// mechanism, which reads migrations.Core and migrations.Custom and knows
// nothing of the composition — so no environment can put an extension's
// namespace into this database. River is not migrated either: the gate reads
// public.workspace and schema ext, and nothing here touches a job.
//
// The clone the harness gave us is still created and dropped by the harness,
// and is used here for one thing only: the credentials, host and connection
// parameters to reach the cluster. Its schema is never read.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// gateDSN is the vanilla throwaway database every test in this package runs
// against; ownerDSN returns it. Set once by TestMain, before any test runs.
var gateDSN string

func TestMain(m *testing.M) {
	// os.Exit skips deferred functions, so the teardown is explicit and runs
	// on every path: a leaked database on the test cluster outlives the run
	// and the next one clones a cluster with rubbish on it.
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "extmigrategate tests: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (code int, err error) {
	harness := os.Getenv("MARGINCE_TEST_DSN")
	if harness == "" {
		return 0, fmt.Errorf("MARGINCE_TEST_DSN is unset — run this package through `make test-it DIR=backend/tools/extmigrategate`")
	}
	ctx := context.Background()

	// Named from the pid, like the unit names and therefore the cluster-scoped
	// roles the gate mints: one process owns one database and one set of roles,
	// so two shards of the integration lane on one cluster cannot collide.
	name := fmt.Sprintf("margince_extgate_test_%d", os.Getpid())
	maintenance, err := dsnForDatabase(harness, "postgres")
	if err != nil {
		return 0, err
	}
	gateDSN, err = dsnForDatabase(harness, name)
	if err != nil {
		return 0, err
	}

	admin, err := pgx.Connect(ctx, maintenance)
	if err != nil {
		return 0, fmt.Errorf("connecting to the maintenance database: %w", err)
	}
	defer closeQuietly(ctx, admin)

	// WITH (FORCE): a previous run killed mid-test leaves backends attached,
	// and a DROP that merely fails would make every later run of this suite
	// fail on the CREATE instead of on whatever it was testing.
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		return 0, fmt.Errorf("dropping a leftover %s: %w", name, err)
	}
	if err := reapAbandonedDatabases(ctx, admin, name); err != nil {
		return 0, err
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		return 0, fmt.Errorf("creating the throwaway %s: %w", name, err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); dropErr != nil {
			dropErr = fmt.Errorf("dropping the throwaway %s — leaked on the test cluster: %w", name, dropErr)
			if err == nil {
				err = dropErr
			} else {
				err = fmt.Errorf("%w (and %w)", err, dropErr)
			}
		}
	}()

	if err := migrateVanilla(ctx, gateDSN); err != nil {
		return 0, err
	}
	return m.Run(), nil
}

// reapAbandonedDatabases drops every throwaway this suite has ever left behind
// that nothing is connected to.
//
// The teardown above runs through a deferred function, and a test binary killed
// by `go test -timeout` (SIGQUIT) or SIGKILL runs no defers at all. The name
// embeds THIS process's pid, so the run that comes after cannot clean up the
// one that died — it computes a different name and reaps nothing. Left alone
// that is not a leak, it is an accumulation: one abandoned database per timeout
// forever, each holding a full migrated schema, on a cluster shared by the
// whole integration lane.
//
// Zero backends is the liveness test, and DROP without FORCE is the second
// half of it: another shard of this same lane is a legitimate concurrent owner
// of its own pid-named database, and the pair means such a database is left
// alone even if it goes idle between the query and the drop. `mine` is skipped
// because the caller has just dropped it WITH (FORCE), which is right for this
// process's own name and wrong for anyone else's.
func reapAbandonedDatabases(ctx context.Context, admin *pgx.Conn, mine string) error {
	rows, err := admin.Query(ctx, `
		SELECT quote_ident(d.datname)
		  FROM pg_database d
		 WHERE d.datname LIKE 'margince\_extgate\_test\_%'
		   AND d.datname <> $1
		   AND NOT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datname = d.datname)`, mine)
	if err != nil {
		return fmt.Errorf("looking for abandoned throwaway databases: %w", err)
	}
	var abandoned []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("looking for abandoned throwaway databases: %w", err)
		}
		abandoned = append(abandoned, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("looking for abandoned throwaway databases: %w", err)
	}
	for _, name := range abandoned {
		// Not fatal: a database that acquired a backend between the query and
		// here belongs to somebody, and refusing to run this suite over
		// somebody else's housekeeping would be the wrong trade.
		if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
			fmt.Fprintf(os.Stderr, "extmigrategate tests: leaving %s in place: %v\n", name, err)
		}
	}
	return nil
}

// migrateVanilla brings the throwaway database to head over the core and
// custom namespaces and NOTHING else.
//
// It rides testdb.EnsureSchema rather than calling the migrator itself, which
// is the lane-wide obligation (TestIntegrationSuitesMigrateOncePerProcess) and
// is also the right mechanism here on its own merits: it is the ONE place that
// decides what "a migrated test database" means, and a second spelling of that
// in the gate's fixture is how the gate would end up asserting over a schema
// no other suite sees. Once per process is exactly the cadence this suite
// wants — every test reverts its own migration, so nothing between them needs
// resetting and Reset is never called.
func migrateVanilla(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to the throwaway database: %w", err)
	}
	defer closeQuietly(ctx, conn)
	if err := testdb.EnsureSchema(ctx, conn); err != nil {
		return fmt.Errorf("migrating the throwaway database: %w", err)
	}
	// EnsureSchema begins with DROP SCHEMA public CASCADE + CREATE SCHEMA
	// public, and a schema created that way does NOT carry the USAGE grant to
	// PUBLIC that PostgreSQL puts on the public schema of a freshly created
	// database. Every other suite is indifferent — they connect as the owner or
	// as margince_app, which EnsureSchema grants explicitly — but this one is
	// the only suite that connects as a role holding no grant of its own, by
	// design, and the tenant-table rule requires that role to name
	// public.workspace in a foreign key. Without USAGE it cannot, and every
	// test fails on "cannot USE schema public" instead of on what it probes.
	//
	// This RESTORES a PostgreSQL default rather than inventing a privilege: it
	// is the posture the production gate runs against, where cmd/migrate
	// migrates a database created by CREATE DATABASE and never touches schema
	// public's ACL (scripts/check-ext-migrations.sh). CREATE stays revoked,
	// which is the grant the gate's teeth actually rest on — role.go's
	// unqualified-create refusal.
	if _, err := conn.Exec(ctx, `GRANT USAGE ON SCHEMA public TO PUBLIC`); err != nil {
		return fmt.Errorf("restoring the default USAGE grant on schema public: %w", err)
	}
	return nil
}

// dsnForDatabase swaps the database segment of the harness DSN, preserving the
// credentials, host and every connection parameter (sslmode and the rest) —
// the same swap scripts/lib-testdb.sh does for its clones, and the reason a
// keyword-form DSN is refused rather than guessed at: silently connecting
// somewhere else is how a gate ends up asserting over the wrong database.
func dsnForDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing MARGINCE_TEST_DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("MARGINCE_TEST_DSN must be a postgres:// URL so this suite can point it at its own throwaway database, got %q", u.Scheme)
	}
	u.Path = "/" + name
	return u.String(), nil
}
