// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// appRole is the runtime role an extension table may be granted DML on. It is
// the only grantee outside the owner that the allowlist admits: the app reads
// and writes extension rows under RLS, and nothing else has business here.
const appRole = "margince_app"

// tableDML is the privilege set appRole may hold on a TABLE, as PostgreSQL
// spells it in an ACL item: a(ppend/INSERT), r(ead/SELECT), w(rite/UPDATE),
// d(elete). Notably absent are D (TRUNCATE — bypasses the policy's USING clause
// and empties every tenant's rows at once), x (REFERENCES) and t (TRIGGER).
const tableDML = "arwd"

// sequenceUse is the equivalent for a SEQUENCE: r(SELECT), w(UPDATE) and
// U(SAGE). USAGE is the one that matters — an INSERT into a table whose column
// defaults to nextval() needs it — and a sequence carries no tenant rows, so
// the allowlist here is about completeness rather than isolation.
const sequenceUse = "rwU"

// tenantColumn is the column that makes a row belong to a workspace. Every
// extension table carries it under the same name as the core tables do,
// because the policy predicate, the app's WithWorkspaceTx binding and this
// gate all address it by that name.
const tenantColumn = "workspace_id"

// tenantPredicate is the ONE tenant-isolation expression, as pg_get_expr
// renders it. Comparing the rendered form rather than the source text is what
// makes the check total: any spelling that means something else — USING (true)
// most obviously, but equally a comparison against a different setting, a
// different column, a missing NULLIF guard, or an OR that widens it — renders
// differently and is refused, without this gate having to enumerate the ways to
// get it wrong.
//
// IF A UNIT EVER NEEDS A NARROWER SCOPE, do not loosen this. A "the canonical
// predicate must appear as a conjunct" rule is defeated by
// `(canonical AND true) OR something`, which is how an exact pin becomes an
// enumeration again. Admit ONE ADDITIONAL RESTRICTIVE POLICY instead:
// restrictive policies AND together and can only ever narrow what this one
// admits, so the widening case stays impossible by construction.
const tenantPredicate = `(workspace_id = (NULLIF(current_setting('app.workspace_id'::text, true), ''::text))::uuid)`

// relation is one row of the ext schema's contents.
type relation struct {
	oid         uint32
	name        string
	kind        rune
	persistence rune
	owner       string
	rls, force  bool
}

// validateCatalog asserts that what the unit's migrations left in the database
// is exactly an allowlisted shape, and refuses everything else by name.
//
// It runs over the ext_<name> connection, not an admin one. Every fact below
// is a catalog read that any role may make, so the choice does not change what
// is visible — but the RLS probe at the end is only meaningful from a role that
// row-level security actually binds, and running the whole validation from one
// connection removes the chance of the probe drifting onto the exempt one.
func validateCatalog(ctx context.Context, conn *pgx.Conn, namespace string) error {
	if err := assertNoStrayObjects(ctx, conn, namespace); err != nil {
		return err
	}
	relations, err := relationsInExt(ctx, conn)
	if err != nil {
		return err
	}
	if len(relations) == 0 {
		return fmt.Errorf("the migrations created no relation in schema %s — a unit with migrations owns at least one table there", extSchema)
	}
	prefix := namespace + "_"
	for _, rel := range relations {
		if err := assertRelationAllowed(ctx, conn, rel, namespace, prefix); err != nil {
			return err
		}
	}
	if err := assertNoTriggers(ctx, conn); err != nil {
		return err
	}
	return assertNoRules(ctx, conn)
}

// assertRelationAllowed applies the per-relation rules: who owns it, whether
// its name is inside the unit's namespace, and whether its relkind is one an
// extension may create at all.
func assertRelationAllowed(ctx context.Context, conn *pgx.Conn, rel relation, namespace, prefix string) error {
	where := extSchema + "." + rel.name
	if rel.owner != namespace {
		return fmt.Errorf("%s is owned by %q, not by the unit's %s role — an object the unit does not own is one its own migrations cannot revert", where, rel.owner, namespace)
	}
	// Enforced over EVERY relkind, not only tables: indexes, sequences and
	// views share one per-schema relation namespace with tables in PostgreSQL,
	// so an index named ext_a_b_c collides with another unit's table of that
	// name. gen-composition's textual rule collects tables only and delegates
	// the rest here.
	if !strings.HasPrefix(rel.name, prefix) {
		return fmt.Errorf("%s is outside the unit's namespace — every relation a unit creates is %s<name>, which is what keeps two units sharing the %s schema from colliding or addressing each other's rows", where, prefix, extSchema)
	}

	switch rel.kind {
	case 'r', 'p':
		return assertTenantTable(ctx, conn, rel, namespace)
	case 'i', 'I':
		return nil // an index carries no rows of its own, and pg_class.relacl is always null for one
	case 'S':
		// A sequence has no workspace_id to isolate, but it does carry an ACL,
		// and this allowlist advertises completeness.
		return assertGrants(ctx, conn, rel, namespace, where, sequenceUse)
	case 'f':
		return fmt.Errorf("%s is a FOREIGN TABLE — PostgreSQL cannot enforce row-level security on one, so its rows would be reachable across every workspace; extension data lives in ordinary tables in %s", where, extSchema)
	case 'm':
		return fmt.Errorf("%s is a MATERIALIZED VIEW — PostgreSQL cannot enforce row-level security on one, so it would hold a cross-workspace copy of whatever it selected", where)
	case 'v':
		return fmt.Errorf("%s is a VIEW — a view is not in the allowlist: it runs with its owner's rights over the base tables and is one more surface the policy has to be re-argued on", where)
	default:
		return fmt.Errorf("%s has relkind %q, which is not in the allowlist (ordinary and partitioned tables, their indexes and sequences)", where, string(rel.kind))
	}
}

// assertTenantTable is the positive shape every extension table must have.
func assertTenantTable(ctx context.Context, conn *pgx.Conn, rel relation, namespace string) error {
	where := extSchema + "." + rel.name
	if rel.persistence != 'p' {
		return fmt.Errorf("%s has relpersistence %q — an UNLOGGED or TEMPORARY tenant table loses its rows on a crash or at disconnect, silently", where, string(rel.persistence))
	}
	if err := assertTenantColumn(ctx, conn, rel, where); err != nil {
		return err
	}
	if !rel.rls || !rel.force {
		return fmt.Errorf("%s has relrowsecurity=%t relforcerowsecurity=%t — both are required: ENABLE alone leaves the table's OWNER, which is the unit's own role, reading and writing every workspace's rows", where, rel.rls, rel.force)
	}
	if err := assertSinglePolicy(ctx, conn, rel, where); err != nil {
		return err
	}
	if err := assertGrants(ctx, conn, rel, namespace, where, tableDML); err != nil {
		return err
	}
	// Partitioned tables plan as an Append over their partitions, so the
	// single-relation plan shape the probe reads does not apply to them; the
	// catalog facts above already bind, and each partition is itself a
	// relation this loop visits.
	//
	// A CONSEQUENCE a unit author will hit face-first: `CREATE TABLE … PARTITION
	// OF` propagates neither relrowsecurity, relforcerowsecurity nor policies to
	// the child. Because this loop visits each partition as its own relation and
	// applies the full tenant-table rule to it, a partitioned unit table is only
	// accepted if the author enables AND forces RLS and writes an identical
	// policy on the parent and on every partition. That errs strict rather than
	// lax — an unprotected partition is an unprotected table — but it is not
	// obvious from the DDL, so it is written down here.
	if rel.kind != 'r' {
		return nil
	}
	return assertRLSBindsTheOwner(ctx, conn, rel, where)
}

// assertTenantColumn requires the column and its type, then hands off to the
// foreign-key rule. The cascade is not cosmetic: without it, deleting a
// workspace fails on the extension's rows, so tenant erasure stops at the first
// installed unit.
func assertTenantColumn(ctx context.Context, conn *pgx.Conn, rel relation, where string) error {
	var (
		typeName string
		notNull  bool
	)
	err := conn.QueryRow(ctx, `
		SELECT format_type(a.atttypid, a.atttypmod), a.attnotnull
		  FROM pg_attribute a
		 WHERE a.attrelid = $1 AND a.attname = $2 AND NOT a.attisdropped`,
		rel.oid, tenantColumn).Scan(&typeName, &notNull)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("%s has no %s column — a table without it belongs to no workspace, and no policy can isolate it", where, tenantColumn)
	}
	if err != nil {
		return fmt.Errorf("reading %s's columns: %w", where, err)
	}
	if typeName != "uuid" || !notNull {
		return fmt.Errorf("%s.%s is %s%s — it must be `uuid NOT NULL`, or a row can carry a null or a value the policy's comparison silently never matches", where, tenantColumn, typeName, nullability(notNull))
	}

	return assertWorkspaceForeignKeys(ctx, conn, rel, where)
}

// assertWorkspaceForeignKeys enumerates EVERY foreign key the table declares
// onto the core workspace table and requires there to be exactly one, on
// workspace_id, cascading.
//
// Enumerating all of them rather than looking the tenant one up is the whole
// point. The role holds REFERENCES on workspace(id), so nothing stops a
// migration declaring a SECOND key from a different column:
//
//	workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,  -- correct
//	pinned_ws    uuid          REFERENCES workspace(id)                     -- NO ACTION
//
// A lookup of the tenant key alone passes that, and the second key reinstates
// exactly the harm the cascade rule exists to prevent: deleting a workspace
// fails on this unit's rows, so erasing a tenant stops at the first installed
// extension. The rule is a property of the TABLE, not of one column, so it has
// to be checked over the whole set.
//
// There is deliberately no check that the key points at workspace's `id`
// specifically. That one is covered by PRIVILEGE rather than by assertion: the
// role's grant is `REFERENCES (id) ON public.workspace`, column-scoped, so a
// key onto any other unique column of workspace is refused by PostgreSQL at
// CREATE time and never reaches this query.
func assertWorkspaceForeignKeys(ctx context.Context, conn *pgx.Conn, rel relation, where string) error {
	rows, err := conn.Query(ctx, `
		SELECT co.conname, co.confdeltype,
		       (SELECT coalesce(string_agg(att.attname, ', ' ORDER BY k.ord), '')
		          FROM unnest(co.conkey) WITH ORDINALITY AS k(num, ord)
		          JOIN pg_attribute att ON att.attrelid = co.conrelid AND att.attnum = k.num)
		  FROM pg_constraint co
		 WHERE co.conrelid = $1 AND co.contype = 'f' AND co.confrelid = $2::regclass
		 ORDER BY co.conname`, rel.oid, coreTenantParent)
	if err != nil {
		return fmt.Errorf("reading %s's foreign keys: %w", where, err)
	}
	defer rows.Close()

	type foreignKey struct{ name, delete, columns string }
	var keys []foreignKey
	for rows.Next() {
		var key foreignKey
		if err := rows.Scan(&key.name, &key.delete, &key.columns); err != nil {
			return fmt.Errorf("reading %s's foreign keys: %w", where, err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading %s's foreign keys: %w", where, err)
	}

	if len(keys) == 0 {
		return fmt.Errorf("%s.%s has no foreign key onto %s(id) — without it the column is an unenforced claim, and a row can name a workspace that does not exist", where, tenantColumn, coreTenantParent)
	}
	if len(keys) > 1 {
		named := make([]string, 0, len(keys))
		for _, key := range keys {
			named = append(named, fmt.Sprintf("%s on (%s)", key.name, key.columns))
		}
		return fmt.Errorf("%s declares %d foreign keys onto %s — exactly one is allowed, on %s: %s. A second reference to a workspace is a second tenancy claim on the row, and it is the one nothing forces to cascade",
			where, len(keys), coreTenantParent, tenantColumn, strings.Join(named, "; "))
	}
	key := keys[0]
	if key.columns != tenantColumn {
		return fmt.Errorf("%s references %s from (%s) rather than from %s — the tenant claim must be the column the policy compares, or the row belongs to one workspace and points at another", where, coreTenantParent, key.columns, tenantColumn)
	}
	if key.delete != "c" {
		return fmt.Errorf("%s.%s references %s(id) without ON DELETE CASCADE — deleting a workspace would then fail on this unit's rows, so erasing a tenant stops at the first installed extension", where, tenantColumn, coreTenantParent)
	}
	return nil
}

// assertSinglePolicy requires exactly one policy and pins every one of its
// dimensions. Exactly one, because a second permissive policy is an OR: it can
// only ever widen what the first admits, and a set of policies is not something
// a reader can evaluate by looking at one of them.
func assertSinglePolicy(ctx context.Context, conn *pgx.Conn, rel relation, where string) error {
	rows, err := conn.Query(ctx, `
		SELECT p.polname, p.polcmd, p.polpermissive, p.polroles::text,
		       coalesce(pg_get_expr(p.polqual, p.polrelid), ''),
		       coalesce(pg_get_expr(p.polwithcheck, p.polrelid), '')
		  FROM pg_policy p WHERE p.polrelid = $1 ORDER BY p.polname`, rel.oid)
	if err != nil {
		return fmt.Errorf("reading %s's policies: %w", where, err)
	}
	defer rows.Close()

	type policy struct {
		name, cmd, roles, qual, withCheck string
		permissive                        bool
	}
	var policies []policy
	for rows.Next() {
		var p policy
		if err := rows.Scan(&p.name, &p.cmd, &p.permissive, &p.roles, &p.qual, &p.withCheck); err != nil {
			return fmt.Errorf("reading %s's policies: %w", where, err)
		}
		policies = append(policies, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading %s's policies: %w", where, err)
	}

	if len(policies) != 1 {
		names := make([]string, 0, len(policies))
		for _, p := range policies {
			names = append(names, p.name)
		}
		return fmt.Errorf("%s carries %d policies (%s) — exactly one is required: permissive policies OR together, so a second one can only widen what the first admits", where, len(policies), strings.Join(names, ", "))
	}
	p := policies[0]
	switch {
	case !p.permissive:
		return fmt.Errorf("policy %s on %s is RESTRICTIVE and is the only policy on the table — a restrictive policy narrows a permissive one, and with none to narrow it admits no row at all", p.name, where)
	case p.cmd != "*":
		return fmt.Errorf("policy %s on %s applies to %q only — the single policy must cover ALL commands, or the ones it omits are unrestricted", p.name, where, commandName(p.cmd))
	case p.roles != "{0}":
		return fmt.Errorf("policy %s on %s is scoped TO specific roles (polroles=%s) — a role outside that list is unrestricted by it; the policy must apply to PUBLIC", p.name, where, p.roles)
	case p.qual != tenantPredicate:
		return fmt.Errorf("policy %s on %s has USING %s — it must be exactly %s, which is the one predicate the app's workspace binding sets", p.name, where, p.qual, tenantPredicate)
	case p.withCheck != tenantPredicate:
		return fmt.Errorf("policy %s on %s has WITH CHECK %q — it must be exactly %s, or the table admits INSERTs and UPDATEs that write rows into another workspace", p.name, where, p.withCheck, tenantPredicate)
	}
	return nil
}

// assertGrants refuses every privilege outside the allowlist, on the relation
// and on its individual columns. allowed is the privilege set appRole may hold
// on this relkind — tableDML for a table, sequenceUse for a sequence, which are
// different letters and must not be checked against one list.
func assertGrants(ctx context.Context, conn *pgx.Conn, rel relation, namespace, where, allowed string) error {
	var acl []string
	if err := conn.QueryRow(ctx,
		`SELECT coalesce(c.relacl::text[], '{}') FROM pg_class c WHERE c.oid = $1`, rel.oid,
	).Scan(&acl); err != nil {
		return fmt.Errorf("reading %s's grants: %w", where, err)
	}
	for _, item := range acl {
		grantee, privileges, ok := strings.Cut(item, "=")
		if !ok {
			return fmt.Errorf("%s carries an unreadable ACL item %q", where, item)
		}
		privileges, _, _ = strings.Cut(privileges, "/")
		switch {
		case grantee == namespace:
			continue // the owner's own entry
		case grantee == "":
			return fmt.Errorf("%s grants %q to PUBLIC — every role on the cluster, including ones that predate this unit and ones RLS does not bind", where, privileges)
		case grantee != appRole:
			return fmt.Errorf("%s grants %q to %q — only the owner and %s may hold privileges on an extension table", where, privileges, grantee, appRole)
		case strings.Trim(privileges, allowed) != "":
			return fmt.Errorf("%s grants %q to %s — only %q is allowed on this relation; TRUNCATE in particular empties every workspace's rows without consulting the policy", where, privileges, appRole, allowed)
		}
	}

	var column, columnACL string
	err := conn.QueryRow(ctx, `
		SELECT a.attname, a.attacl::text FROM pg_attribute a
		 WHERE a.attrelid = $1 AND a.attacl IS NOT NULL LIMIT 1`, rel.oid).Scan(&column, &columnACL)
	if err == nil {
		return fmt.Errorf("%s.%s carries a column-level grant %s — column grants are outside the allowlist: they are invisible in the table's own ACL and split the privilege story in two", where, column, columnACL)
	}
	if err != pgx.ErrNoRows {
		return fmt.Errorf("reading %s's column grants: %w", where, err)
	}
	return nil
}

// assertRLSBindsTheOwner is the one BEHAVIORAL check here, and it exists
// because the catalog facts above are necessary and not sufficient: FORCE ROW
// LEVEL SECURITY is silently ignored for a superuser or a BYPASSRLS role, so
// relforcerowsecurity=true over such a connection means nothing. mintRole
// already proved this role holds neither exemption; this proves the effect,
// which is the claim that actually matters — the planner puts the policy's
// qualifier into the plan for this role, and would not for an exempt one.
func assertRLSBindsTheOwner(ctx context.Context, conn *pgx.Conn, rel relation, where string) error {
	// Sanitized rather than concatenated: relname comes from pg_class and is
	// only constrained to START WITH the unit's prefix, so ext."ext_de_x; --"
	// reaches here. It would execute as the restricted role and grant nothing
	// the migration did not already have, but a gate that can be made to emit a
	// confusing error instead of a verdict is not one to leave standing.
	target := pgx.Identifier{extSchema, rel.name}.Sanitize()
	rows, err := conn.Query(ctx, `EXPLAIN (COSTS OFF) SELECT * FROM `+target)
	if err != nil {
		return fmt.Errorf("planning a read of %s as its owner: %w", where, err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("planning a read of %s as its owner: %w", where, err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("planning a read of %s as its owner: %w", where, err)
	}
	// The whole rendered predicate, not the bare column name: a unit table
	// called ext_de_workspace_id_map puts "workspace_id" into the plan's
	// `Seq Scan on …` line, and a probe that looked for that would pass
	// vacuously on exactly the table it exists to check.
	if !strings.Contains(plan.String(), "Filter: "+tenantPredicate) {
		return fmt.Errorf("a read of %s by its own owner plans without the tenant filter:\n%s— row-level security is not binding the owner, so the policy on this table isolates nothing", where, plan.String())
	}
	return nil
}

func nullability(notNull bool) string {
	if notNull {
		return " NOT NULL"
	}
	return " NULL"
}

// commandName renders pg_policy.polcmd for a human.
func commandName(cmd string) string {
	switch cmd {
	case "r":
		return "SELECT"
	case "a":
		return "INSERT"
	case "w":
		return "UPDATE"
	case "d":
		return "DELETE"
	default:
		return cmd
	}
}
