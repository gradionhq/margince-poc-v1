// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// extSchema is the one schema an extension's migrations may create in; the
// core owns public (backend/migrations/core/0198_ext_schema.up.sql).
const extSchema = "ext"

// coreTenantParent is the core table every extension tenant table hangs off.
// It is the ONLY core relation the extension role is granted anything on, and
// the grant is REFERENCES on one column — enough to declare the foreign key
// this gate requires and not enough to read a row of it.
const coreTenantParent = "public.workspace"

// extRole is the restricted role a unit's migrations are applied as: the
// ext_<name> role a deployed installation would own its tables under.
type extRole struct {
	name string
	conn *pgx.Conn
}

// mintRole creates ext_<name> with the narrowest set of privileges a unit's
// migrations can possibly need, connects as it, and refuses to proceed if that
// role turns out to hold anything more.
//
// The privilege set IS the gate's teeth, so each piece is deliberate:
//
//   - NOSUPERUSER, NOBYPASSRLS. Both exemptions make FORCE ROW LEVEL SECURITY
//     a no-op for the role holding them, so every RLS conclusion drawn over
//     such a connection is vacuous. Asserted again after connecting, because a
//     cluster where the role already existed with different attributes would
//     otherwise turn this whole gate into a test of nothing.
//   - CREATE, USAGE on ext and NOTHING on public. This is what converts "the
//     unit must not touch core" from something to detect into something
//     PostgreSQL refuses. It also makes an UNQUALIFIED create fail: the default
//     search_path is "$user", public, there is no schema named ext_<name>, and
//     the role cannot create in public — which is precisely the claim
//     gen-composition's textual rule makes when it accepts a bare
//     `CREATE TABLE ext_<name>_thing`.
//   - REFERENCES on workspace(id) alone. The tenant-table rule requires
//     `workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`,
//     and declaring that foreign key needs the REFERENCES privilege on the
//     parent. Column-scoped and privilege-scoped: the role can point at
//     workspace, and cannot select, insert or alter a byte of it.
func mintRole(ctx context.Context, admin *pgx.Conn, namespace, dsn string) (minted *extRole, err error) {
	role := &extRole{name: namespace}

	// A role is CLUSTER-scoped, so a name derived from the unit is shared by
	// every concurrent run on this cluster. Adopting or dropping one that a
	// live run is using would corrupt that run instead of this one, so an
	// in-use role is refused and only a leaked one is cleaned up.
	var sessions int
	if err := admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE usename = $1`, role.name,
	).Scan(&sessions); err != nil {
		return nil, fmt.Errorf("looking for live %s sessions: %w", role.name, err)
	}
	if sessions > 0 {
		return nil, fmt.Errorf("role %s has %d live session(s) on this cluster — another extmigrategate run owns it; re-run when it finishes", role.name, sessions)
	}

	password, err := randomPassword()
	if err != nil {
		return nil, err
	}
	// Armed BEFORE the role exists and only from here: everything above this
	// point must NOT drop the role, since the one failure it reports is that
	// another run owns it. Everything below created the role, so a failure
	// there has to take it back down — a half-minted LOGIN role is the same
	// standing credential as a leaked one.
	defer func() {
		if err == nil {
			return
		}
		closeQuietly(ctx, role.conn)
		if dropErr := role.drop(ctx, admin); dropErr != nil {
			err = fmt.Errorf("%w (and cleaning up afterwards: %w)", err, dropErr)
		}
	}()
	// Dropped and recreated rather than reused: a role left behind by an
	// earlier run may have been granted anything since, and inheriting it would
	// quietly weaken every refusal that rests on what this role cannot do.
	for _, statement := range []string{
		`DROP OWNED BY ` + role.name + ` CASCADE`,
		`DROP ROLE IF EXISTS ` + role.name,
		`CREATE ROLE ` + role.name + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
		`GRANT CREATE, USAGE ON SCHEMA ` + extSchema + ` TO ` + role.name,
		`GRANT REFERENCES (id) ON TABLE ` + coreTenantParent + ` TO ` + role.name,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil && !isMissingRole(err) {
			return nil, fmt.Errorf("preparing the %s role (%s): %w", role.name, statement, err)
		}
	}

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing the throwaway DSN: %w", err)
	}
	config.User, config.Password = role.name, password
	if role.conn, err = pgx.ConnectConfig(ctx, config); err != nil {
		return nil, fmt.Errorf("connecting as %s: %w", role.name, err)
	}
	if err = role.assertRestricted(ctx); err != nil {
		return nil, err
	}
	return role, nil
}

// assertRestricted proves the role is the restricted thing this gate assumes
// before a single migration runs. Without it, a cluster that had already
// granted the role something — or a Postgres old enough to still grant CREATE
// on public to PUBLIC — would let every migration pass for the wrong reason.
func (r *extRole) assertRestricted(ctx context.Context) error {
	var (
		super, bypass       bool
		createPublic        bool
		usagePublic         bool
		createExt, usageExt bool
	)
	if err := r.conn.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls,
		       has_schema_privilege('public', 'CREATE'),
		       has_schema_privilege('public', 'USAGE'),
		       has_schema_privilege($1, 'CREATE'),
		       has_schema_privilege($1, 'USAGE')
		  FROM pg_roles WHERE rolname = current_user`, extSchema,
	).Scan(&super, &bypass, &createPublic, &usagePublic, &createExt, &usageExt); err != nil {
		return fmt.Errorf("reading the %s role's privileges: %w", r.name, err)
	}
	switch {
	case super || bypass:
		return fmt.Errorf("role %s holds rolsuper=%t rolbypassrls=%t — FORCE ROW LEVEL SECURITY does not bind such a role, so every tenancy assertion below would prove nothing", r.name, super, bypass)
	case createPublic:
		return fmt.Errorf("role %s holds CREATE on schema public — the namespace wall is that it does not, and with it a migration can create a core-schema table that this gate would then have to detect rather than have refused", r.name)
	case !usagePublic:
		// Not a violation of the role's own shape, but the tenant foreign key
		// cannot be declared without it, so say so here rather than let every
		// unit fail inside its own CREATE TABLE.
		return fmt.Errorf("role %s cannot USE schema public, so it cannot name %s in a foreign key — grant USAGE on public to PUBLIC on this cluster (CREATE stays revoked)", r.name, coreTenantParent)
	case !createExt || !usageExt:
		return fmt.Errorf("role %s lacks CREATE/USAGE on schema %s — migration 0198 creates that schema; is this database migrated to head?", r.name, extSchema)
	}
	return nil
}

// drop removes the role and everything it owns. A failure is returned rather
// than logged away: a login role left on the cluster owns the tables it just
// created and can drop their tenant-isolation policies, which is a standing
// credential, not a tidiness issue.
func (r *extRole) drop(ctx context.Context, admin *pgx.Conn) error {
	for _, statement := range []string{
		`DROP OWNED BY ` + r.name + ` CASCADE`,
		`REVOKE ALL ON TABLE ` + coreTenantParent + ` FROM ` + r.name,
		`DROP ROLE IF EXISTS ` + r.name,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil && !isMissingRole(err) {
			return fmt.Errorf("removing the %s role (%s): %w", r.name, statement, err)
		}
	}
	return nil
}

// undefinedObject is SQLSTATE 42704, which is what "role ... does not exist"
// arrives as.
const undefinedObject = "42704"

// isMissingRole reports whether err is Postgres complaining that the role does
// not exist. DROP OWNED BY and REVOKE have no IF EXISTS spelling, so the first
// run on a clean cluster and the cleanup after a failed mint both hit this;
// any OTHER failure of those statements is real and must propagate.
func isMissingRole(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == undefinedObject
	}
	return false
}

// randomPassword mints the run's credential: hex of 16 random bytes, so there
// is nothing to quote inside the CREATE ROLE literal and nothing derivable
// from the repository.
func randomPassword() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("minting the extension role's password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
