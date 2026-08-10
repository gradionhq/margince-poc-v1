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
// core owns public (backend/migrations/core/0206_ext_schema.up.sql).
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
//
//   - CREATE, USAGE on ext and NOTHING on public. This is what converts "the
//     unit must not touch core" from something to detect into something
//     PostgreSQL refuses. It also makes an UNQUALIFIED create fail: the default
//     search_path is "$user", public, there is no schema named ext_<name>, and
//     the role cannot create in public — which is precisely the claim
//     gen-composition's textual rule makes when it accepts a bare
//     `CREATE TABLE ext_<name>_thing`.
//
//   - REFERENCES on workspace(id) alone. The tenant-table rule requires
//     `workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`,
//     and declaring that foreign key needs the REFERENCES privilege on the
//     parent, so "no grants on public at all" is not achievable alongside the
//     rule. Column-scoped and privilege-scoped is the minimum that works: the
//     role can point at workspace, and cannot select, insert or alter a byte of
//     it. It also cannot point at any OTHER unique column of workspace, which
//     is what lets assertWorkspaceForeignKeys skip checking confkey.
//
//     Accepted residue: a foreign key is an existence oracle. A role holding it
//     learns from a constraint violation whether a given workspace UUID exists.
//     That is inherent to requiring the key at all and is not worth engineering
//     away — it leaks one bit about an opaque identifier the caller already had.
func mintRole(ctx context.Context, admin *pgx.Conn, namespace, dsn string) (minted *extRole, err error) {
	role := &extRole{name: namespace}

	password, err := randomPassword()
	if err != nil {
		return nil, err
	}

	// CREATE ROLE is the claim, and it is taken FIRST. A role is CLUSTER-scoped,
	// so a name derived from the unit is shared by every concurrent run on this
	// cluster, and the earlier shape here — count live sessions, then drop and
	// recreate — was a check followed by a take, which two runs can both pass
	// before either takes. CREATE cannot be won twice: PostgreSQL answers the
	// loser 42710, so exactly one run owns the name.
	//
	// A leftover from a dead run still has to be reclaimable, so a duplicate is
	// not the end of it — but reclaiming is the ONLY thing a duplicate permits,
	// and only after the role is shown to have no live session anywhere on the
	// cluster. The remaining window is small and named rather than closed: a run
	// that has created its role but not yet connected looks idle to a second run
	// arriving in those milliseconds. Closing it needs an ownership record the
	// cluster does not have (roles carry no creation time), and the cost of the
	// window is a failed CI step, not a wrong verdict.
	claimed, why, err := claimRole(ctx, admin, role.name, password)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, fmt.Errorf("role %s could not be claimed — %s; another extmigrategate run owns it, so re-run when it finishes", role.name, why)
	}
	// Armed only once the role EXISTS: before the claim there is nothing of
	// this run's to take back down, and dropping then would destroy the role of
	// whichever run actually holds it. After it, every failure has to drop —
	// a half-minted LOGIN role is the same standing credential as a leaked one.
	defer func() {
		if err == nil {
			return
		}
		closeQuietly(ctx, role.conn)
		if dropErr := role.drop(ctx, admin); dropErr != nil {
			err = fmt.Errorf("%w (and cleaning up afterwards: %w)", err, dropErr)
		}
	}()
	for _, statement := range []string{
		`GRANT CREATE, USAGE ON SCHEMA ` + extSchema + ` TO ` + role.name,
		`GRANT REFERENCES (id) ON TABLE ` + coreTenantParent + ` TO ` + role.name,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
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

// claimRole creates the role, reclaiming a leaked one if — and only if — no
// session anywhere on the cluster is using it. It reports whether this run now
// owns the name; false means another run does, and `why` says which of the two
// ways that was established — the two are a different thing for an author to
// read, and only one of them ("a live session") tells them to go look at what
// else is running.
//
// Dropped and recreated rather than adopted: a role left behind by an earlier
// run may have been granted anything since, and inheriting it would quietly
// weaken every refusal that rests on what this role cannot do.
func claimRole(ctx context.Context, admin *pgx.Conn, name, password string) (claimed bool, why string, err error) {
	create := `CREATE ROLE ` + name + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`
	err = exec(ctx, admin, create)
	if err == nil {
		return true, "", nil
	}
	if !isDuplicateRole(err) {
		return false, "", fmt.Errorf("creating the %s role: %w", name, err)
	}

	var sessions int
	if err := admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE usename = $1`, name,
	).Scan(&sessions); err != nil {
		return false, "", fmt.Errorf("looking for live %s sessions: %w", name, err)
	}
	if sessions > 0 {
		return false, fmt.Sprintf("it already exists and has %d live session(s) on this cluster", sessions), nil
	}

	for _, statement := range []string{
		`DROP OWNED BY ` + name + ` CASCADE`,
		`DROP ROLE IF EXISTS ` + name,
	} {
		if err := exec(ctx, admin, statement); err != nil && !isMissingRole(err) {
			if isOwnedElsewhere(err) { //nolint:nestif // the two failure modes read better named than flattened
				// DROP OWNED BY reaches only the CURRENT database, while the
				// role is cluster-scoped: a run against another database on
				// this cluster that died before its cleanup leaves objects the
				// admin connection here cannot see, let alone drop. Say which
				// situation this is, because the statement's own message
				// ("cannot be dropped because some objects depend on it") reads
				// as a bug in this gate rather than as residue somewhere else
				// on the cluster.
				return false, "", fmt.Errorf("role %s still owns objects in ANOTHER database on this cluster, so it cannot be reclaimed here — "+
					"connect to that database and run `DROP OWNED BY %s CASCADE`, or point this gate at a cluster of its own: %w",
					name, name, err)
			}
			return false, "", fmt.Errorf("reclaiming the leaked %s role (%s): %w", name, statement, err)
		}
	}

	// The second CREATE is the same claim again, and losing it here means a
	// concurrent run took the name in the moment this one spent reclaiming it.
	// That is the loss reported, not an error: the other run owns it fairly.
	switch err := exec(ctx, admin, create); {
	case err == nil:
		return true, "", nil
	case isDuplicateRole(err):
		return false, "another run took the name while this one was reclaiming it", nil
	default:
		return false, "", fmt.Errorf("recreating the reclaimed %s role: %w", name, err)
	}
}

func exec(ctx context.Context, conn *pgx.Conn, statement string) error {
	_, err := conn.Exec(ctx, statement)
	return err
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
		return fmt.Errorf("role %s lacks CREATE/USAGE on schema %s — migration 0206 creates that schema; is this database migrated to head?", r.name, extSchema)
	}
	return r.assertNoCorePrivileges(ctx)
}

// assertNoCorePrivileges proves the role can neither read nor write any core
// relation.
//
// Schema-level CREATE is what the checks above pin, and it is not the same
// question. The gate's refusal of a unit writing rows into public.person rests
// entirely on PostgreSQL denying the statement, which in turn rests on the role
// holding no privilege on that table. A single
// `GRANT INSERT ON ALL TABLES IN SCHEMA public TO PUBLIC` on the cluster would
// make that refusal quietly disappear while every test still passed, so the
// premise is asserted rather than assumed.
//
// (The example is described rather than quoted on purpose: backendarch's
// identity-spine fitness test greps the tree for the literal statement, and a
// comment carrying it reads to that gate as a new unsanctioned mint site.)
//
// TRIGGER is in the probe alongside the four DML verbs and TRUNCATE, because it
// is a write verb wearing another name: a role holding it can install a trigger
// function of its own on a core table, and from then on every core write runs
// that function. Nothing else in this gate would notice.
//
// REFERENCES is probed separately rather than excluded, because the gate grants
// exactly `REFERENCES (id) ON public.workspace` itself and the required tenant
// foreign key cannot be declared without it. Excluding the verb outright was the
// earlier posture and it was too broad: a cluster that had widened REFERENCES to
// some other core table would let a unit's migration hang a foreign key off it,
// which is not an inert declaration — it takes a lock on core writes and can
// refuse a core delete forever after. So the exemption is narrowed to the exact
// column the gate itself grants.
func (r *extRole) assertNoCorePrivileges(ctx context.Context) error {
	var reachable string
	err := r.conn.QueryRow(ctx, `
		SELECT n.nspname || '.' || c.relname
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		   AND (has_table_privilege(c.oid, 'SELECT') OR has_table_privilege(c.oid, 'INSERT')
		     OR has_table_privilege(c.oid, 'UPDATE') OR has_table_privilege(c.oid, 'DELETE')
		     OR has_table_privilege(c.oid, 'TRUNCATE') OR has_table_privilege(c.oid, 'TRIGGER'))
		 ORDER BY 1 LIMIT 1`).Scan(&reachable)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("checking what %s can reach in public: %w", r.name, err)
	default:
		return fmt.Errorf("role %s can read, write or trigger on %s — this gate refuses a unit's DML on core relations by letting PostgreSQL deny it, and that denial does not happen on a cluster where the core tables are reachable", r.name, reachable)
	}
	return r.assertNoWiderReferences(ctx)
}

// assertNoWiderReferences proves the one core grant the gate makes is the only
// core grant the role holds: REFERENCES on public.workspace(id), nothing else on
// that table and nothing at all on any other.
//
// The probe is column-scoped, not table-scoped: has_table_privilege answers for
// the whole relation and so returns false for the very grant this gate makes,
// which would make a table-level probe blind to a widening on any column.
func (r *extRole) assertNoWiderReferences(ctx context.Context) error {
	var reachable string
	err := r.conn.QueryRow(ctx, `
		SELECT n.nspname || '.' || c.relname || '(' || a.attname || ')'
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		   AND has_column_privilege(c.oid, a.attnum, 'REFERENCES')
		   AND NOT (n.nspname || '.' || c.relname = $1 AND a.attname = 'id')
		 ORDER BY 1 LIMIT 1`, coreTenantParent).Scan(&reachable)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("checking what %s can reference in public: %w", r.name, err)
	}
	return fmt.Errorf("role %s can declare a foreign key against %s — the only core dependency a unit may take is %s(id), and a key on anything else makes core deletes wait on, or refuse for, a unit's table", r.name, reachable, coreTenantParent)
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
// arrives as. dependentObjectsStillExist is 2BP01, which is what a DROP ROLE
// blocked by objects the current database cannot see arrives as.
// duplicateObject is 42710, which is what "role ... already exists" arrives as
// — the losing side of the CREATE ROLE claim.
const (
	undefinedObject            = "42704"
	dependentObjectsStillExist = "2BP01"
	duplicateObject            = "42710"
)

// isDuplicateRole reports whether err is Postgres refusing a CREATE ROLE
// because the name is taken.
func isDuplicateRole(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == duplicateObject
}

// isOwnedElsewhere reports whether err is Postgres refusing to drop the role
// because something still depends on it. Inside this database DROP OWNED BY
// CASCADE has just run, so the only remaining source is another database on the
// same cluster.
func isOwnedElsewhere(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == dependentObjectsStillExist
}

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
