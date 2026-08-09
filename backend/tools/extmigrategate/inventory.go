// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// relationsInExt lists everything the ext schema holds. The gate runs against
// a throwaway database whose ext schema is empty before the migrations apply
// (0200 creates the schema and nothing in it), so every row here is the unit's
// doing — including rows the unit does not own, which is itself a refusal.
func relationsInExt(ctx context.Context, conn *pgx.Conn) ([]relation, error) {
	rows, err := conn.Query(ctx, `
		SELECT c.oid, c.relname, c.relkind, c.relpersistence,
		       pg_get_userbyid(c.relowner), c.relrowsecurity, c.relforcerowsecurity
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1
		 ORDER BY c.relname`, extSchema)
	if err != nil {
		return nil, fmt.Errorf("reading schema %s: %w", extSchema, err)
	}
	defer rows.Close()

	var relations []relation
	for rows.Next() {
		var (
			rel               relation
			kind, persistence string
		)
		if err := rows.Scan(&rel.oid, &rel.name, &kind, &persistence, &rel.owner, &rel.rls, &rel.force); err != nil {
			return nil, fmt.Errorf("reading schema %s: %w", extSchema, err)
		}
		rel.kind, rel.persistence = rune(kind[0]), rune(persistence[0])
		relations = append(relations, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading schema %s: %w", extSchema, err)
	}
	return relations, nil
}

// ownedObjects lists everything the extension role owns, by kind and name.
//
// includeExt selects between the gate's two questions. During validation the
// ext schema's contents are checked relation by relation and this call asks
// only "did anything land ANYWHERE ELSE" — a routine, a type, a schema, a
// default-privilege grant, a relation in another schema. After the revert it
// asks "is anything left at all", ext included.
func ownedObjects(ctx context.Context, conn *pgx.Conn, role string, includeExt bool) ([]string, error) {
	// pg_toast is always excluded: a table's TOAST relation is owned by the
	// table's owner and disappears with it, so reporting it would name an
	// object the author never wrote and cannot drop.
	//
	// The pg_type branch skips typrelid <> 0 (a table's own row type) and
	// typelem <> 0 (an array type); both are created implicitly with a
	// relation this inventory already covers. What is left is a type the unit
	// wrote out — an enum or a domain — which is outside the allowlist.
	rows, err := conn.Query(ctx, `
		SELECT 'relation', n.nspname || '.' || c.relname
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relowner = to_regrole($1::text)
		   AND n.nspname <> 'pg_toast' AND (n.nspname <> $2 OR $3::boolean)
		UNION ALL
		SELECT 'routine', n.nspname || '.' || p.proname
		  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE p.proowner = to_regrole($1::text)
		UNION ALL
		SELECT 'type', n.nspname || '.' || t.typname
		  FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
		 WHERE t.typowner = to_regrole($1::text) AND t.typrelid = 0 AND t.typelem = 0
		UNION ALL
		SELECT 'schema', n.nspname
		  FROM pg_namespace n WHERE n.nspowner = to_regrole($1::text)
		UNION ALL
		SELECT 'default privileges',
		       coalesce(dn.nspname, 'the database') || ' ' || d.defaclacl::text
		  FROM pg_default_acl d LEFT JOIN pg_namespace dn ON dn.oid = d.defaclnamespace
		 WHERE d.defaclrole = to_regrole($1::text)
		 ORDER BY 1, 2`, role, extSchema, includeExt)
	if err != nil {
		return nil, fmt.Errorf("inventorying what %s owns: %w", role, err)
	}
	defer rows.Close()

	var found []string
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return nil, fmt.Errorf("inventorying what %s owns: %w", role, err)
		}
		found = append(found, kind+" "+name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inventorying what %s owns: %w", role, err)
	}
	return found, nil
}

// assertNoStrayObjects refuses anything the unit created outside schema ext,
// plus the object kinds the allowlist admits nowhere at all.
//
// Most of these the role could not have created — it holds no CREATE on public
// and no CREATEDB — so this is not the primary defence; it is the check that
// makes the primary defence's failure legible instead of silent, and it does
// catch the kinds a restricted role CAN still make inside ext: a function, an
// enum, an ALTER DEFAULT PRIVILEGES grant.
func assertNoStrayObjects(ctx context.Context, conn *pgx.Conn, role string) error {
	stray, err := ownedObjects(ctx, conn, role, false)
	if err != nil {
		return err
	}
	if len(stray) > 0 {
		return fmt.Errorf("the migrations left objects outside the %s schema's table allowlist:\n  %s\nan extension owns tables in %s and nothing else — a routine runs with its owner's rights, a default-privilege grant applies to objects that do not exist yet, and neither is visible in the table shape this gate checks",
			extSchema, strings.Join(stray, "\n  "), extSchema)
	}
	return nil
}

// assertNoTriggers refuses user triggers on the unit's tables. A trigger is
// arbitrary code on the write path of a tenant table, running as whoever
// wrote the row; internal triggers (the ones a foreign key or a deferrable
// constraint installs) are excluded because they are not the unit's to write.
func assertNoTriggers(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT c.relname, t.tgname
		  FROM pg_trigger t
		  JOIN pg_class c ON c.oid = t.tgrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE NOT t.tgisinternal AND n.nspname = $1
		 ORDER BY 1, 2`, extSchema)
	if err != nil {
		return fmt.Errorf("reading triggers in schema %s: %w", extSchema, err)
	}
	defer rows.Close()

	for rows.Next() {
		var table, trigger string
		if err := rows.Scan(&table, &trigger); err != nil {
			return fmt.Errorf("reading triggers in schema %s: %w", extSchema, err)
		}
		return fmt.Errorf("trigger %s on %s.%s is outside the allowlist — a trigger is arbitrary code on a tenant table's write path, and the policy this gate checks says nothing about what it does", trigger, extSchema, table)
	}
	return rows.Err()
}

// assertNoRules refuses rewrite rules on the unit's relations.
//
// A rule is not an isolation break the way a missing policy is: the rewritten
// action runs with the invoking role's privileges and RLS still binds the
// target it rewrites to. It is refused because it is arbitrary behaviour on a
// tenant table's write path — `ON INSERT DO INSTEAD NOTHING` silently discards
// every write — which is the same reason assertNoTriggers gives, and because
// this design is a positive allowlist: the question is not "is this harmful"
// but "is this on the list".
//
// _RETURN is excluded: it is the rule that IS a view, and a view in ext is
// already refused by name in assertRelationAllowed, with a better message than
// this one could give.
func assertNoRules(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT c.relname, r.rulename
		  FROM pg_rewrite r
		  JOIN pg_class c ON c.oid = r.ev_class
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND r.rulename <> '_RETURN'
		 ORDER BY 1, 2`, extSchema)
	if err != nil {
		return fmt.Errorf("reading rewrite rules in schema %s: %w", extSchema, err)
	}
	defer rows.Close()

	for rows.Next() {
		var table, rule string
		if err := rows.Scan(&table, &rule); err != nil {
			return fmt.Errorf("reading rewrite rules in schema %s: %w", extSchema, err)
		}
		return fmt.Errorf("rewrite rule %s on %s.%s is outside the allowlist — a rule rewrites statements against a tenant table before they run, and DO INSTEAD NOTHING discards them entirely", rule, extSchema, table)
	}
	return rows.Err()
}

// validateReverted proves the down half is a real reverse and not a partial
// one. A migration that leaves an object behind leaves an ext_<name>-owned
// relation on a database that no longer records the migration, and the next
// apply then fails inside a CREATE that looks correct.
func validateReverted(ctx context.Context, conn *pgx.Conn, namespace, unit string) error {
	left, err := ownedObjects(ctx, conn, namespace, true)
	if err != nil {
		return fmt.Errorf("%s: %w", unit, err)
	}
	if len(left) > 0 {
		return fmt.Errorf("%s: the down-migrations left these behind:\n  %s\nevery migration must reverse completely, or a re-apply fails inside a CREATE that is itself correct",
			unit, strings.Join(left, "\n  "))
	}
	return nil
}
