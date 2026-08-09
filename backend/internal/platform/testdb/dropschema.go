// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// dropTableBatch is how many tables one DROP TABLE statement names. It bounds
// the statement's HEAVYWEIGHT LOCK COUNT, which is the whole point — see
// dropPublicSchema.
//
// 8 is measured, not guessed. A batch's cost is not its table count: CASCADE
// also locks each dropped table's indexes and every constraint riding on it, so
// on this schema a batch runs ~30-40 locks per table. Batches of 24 peaked at
// 780; batches of 8 peak at 339, which puts the drop at or below what one
// ordinary Reset already costs — the point past which shrinking it further only
// buys round trips.
const dropTableBatch = 8

// unreferencedTables lists tables no OTHER table's foreign key points at — the
// leaves of the reference graph, at this instant. Dropping those first is what
// keeps a batch's lock count near the batch size: CASCADE on a table that IS
// referenced must also lock every referencing table to drop the constraint, so
// naming `workspace` early drags most of the schema into that one transaction
// no matter how small the batch is. Emptied leaf by leaf, the heavily
// referenced tables become leaves themselves before their turn comes.
//
// A self-reference (conrelid = confrelid) does not disqualify a table: dropping
// it takes that constraint with it and locks nothing else.
const unreferencedTables = `
	SELECT c.oid FROM pg_class c
	 WHERE c.relnamespace = 'public'::regnamespace
	   AND c.relkind IN ('r', 'p')
	   AND NOT EXISTS (SELECT 1 FROM pg_constraint fk
	                    WHERE fk.contype = 'f' AND fk.confrelid = c.oid
	                      AND fk.conrelid <> c.oid)`

// anyTables is the fallback for a reference CYCLE, where every remaining table
// is pointed at by another and unreferencedTables is empty. Naming a batch of
// them with CASCADE breaks the cycle and guarantees the loop makes progress —
// at a higher lock count for that one statement, which is the honest price of a
// shape the leaf order cannot resolve.
//
// This schema DOES reach it, on the last handful of tables: person ↔ lead and
// passport ↔ oauth_grant are mutual references, so the loop ends with one
// fallback round of nine tables (353 locks, measured). The branch is live, not
// defensive — do not "simplify" it away on the assumption the leaf order always
// terminates.
const anyTables = `
	SELECT c.oid FROM pg_class c
	 WHERE c.relnamespace = 'public'::regnamespace
	   AND c.relkind IN ('r', 'p')`

// maxDropRounds bounds dropPublicSchema's loop. Every round removes at least
// one batch, so the ceiling is reached only if something outside this function
// is re-creating tables underneath it — which is a bug to report, not a
// condition to spin on.
const maxDropRounds = 1000 / dropTableBatch

// dropCandidates names up to dropTableBatch tables from one of the selectors
// above, already quoted and schema-qualified for interpolation into DROP TABLE.
func dropCandidates(ctx context.Context, owner *pgx.Conn, selector string) ([]string, error) {
	tables, err := queryIdents(ctx, owner, fmt.Sprintf(`
		SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname)
		  FROM (%s ORDER BY c.oid LIMIT %d) s
		  JOIN pg_class c ON c.oid = s.oid
		  JOIN pg_namespace n ON n.oid = c.relnamespace`, selector, dropTableBatch))
	if err != nil {
		return nil, fmt.Errorf("listing tables to drop: %w", err)
	}
	return tables, nil
}

// dropPublicSchema empties and recreates public — the same end state the single
// `DROP SCHEMA public CASCADE` this replaced produced, reached in bounded steps
// so that no ONE transaction holds a lock on the entire schema at once.
//
// WHY, precisely, because the one-liner reads better and was correct:
// PostgreSQL's lock table is CLUSTER-WIDE and fixed at startup —
// max_locks_per_transaction × (max_connections + max_prepared_transactions)
// slots, shared by every backend on the instance, not a per-transaction budget.
// `DROP SCHEMA public CASCADE` takes an AccessExclusiveLock on every relation,
// index, constraint, view, sequence, function and type in the schema and holds
// all of them to commit: measured on this schema at 4999 locks against the
// stack's 6400 slots (infra/docker-compose.dev.yml, 64 × 100). One is 78% of the
// instance. TWO CONCURRENT ONES CANNOT BOTH FIT, and the loser does not wait —
// the lock table is not a queue, so it fails outright with
//
//	ERROR: out of shared memory (SQLSTATE 53200)
//	HINT:  You might need to increase max_locks_per_transaction.
//
// The integration lane runs INTEGRATION_JOBS packages at once (16 in CI), each
// its own process against its own clone database on the SHARED instance, and
// each calls EnsureSchema as its first act — so the collision is not exotic, it
// is what the lane does on every run. It surfaced as three unrelated suites
// failing in one shard on "migrating the test schema" / "resetting schema",
// which names the victim rather than the cause; the cause is whichever two
// backends happened to overlap here.
//
// The drop is therefore taken apart into steps that are each their own
// statement, and so each their own transaction, releasing before the next
// begins: tables leaves-first in batches of dropTableBatch, then each extension
// on its own, then the near-empty schema. Measured peak locks in any ONE
// transaction, on this schema:
//
//	one DROP SCHEMA public CASCADE           4999   (78% of the instance)
//	batched by oid, 24 at a time             1951
//	batched leaves-first, 24 at a time       1086
//	batched leaves-first, 8 at a time         653   ← here
//
// Two orderings earn their keep. Leaves first (unreferencedTables): CASCADE on a
// referenced table must lock every referencing table too, so naming `workspace`
// early drags most of the schema into one transaction whatever the batch size.
// Extensions individually: with the tables gone, `DROP SCHEMA CASCADE` was still
// 1086 locks, essentially all of it btree_gist and vector taking their operators,
// opclasses and support functions with them — split out, the worst is 653 and the
// final schema drop is 18.
//
// 653 is no longer an outlier: it is what one ordinary Reset already costs on
// this schema, so EnsureSchema stops being the statement that alone cannot share
// the instance. Sizing the instance for the lane's remaining steady-state demand
// is a separate matter, and is done where the instance is defined —
// infra/docker-compose.dev.yml, which CI runs via `make db-up`.
//
// The split is the fix and the ceiling is the headroom, in that order and not the
// other way round: the arithmetic depends on the schema's size and the lane's
// concurrency, both of which grow, so a ceiling raised to fit today's numbers is
// re-crossed by the next twenty tables. Not holding the whole schema in one
// transaction does not expire.
func dropPublicSchema(ctx context.Context, owner *pgx.Conn) error {
	// Bounded rather than `for {}`: DROP TABLE cannot fail to remove what it
	// named, so a batch that lists tables and then finds the same ones again
	// means something is re-creating them underneath us. Spinning forever on
	// that is strictly worse than failing with the reason.
	//
	// `cleared` rather than reading the counter afterwards: the loop spends one
	// pass finding nothing left, so a schema needing exactly maxDropRounds
	// batches exits with the counter at zero having dropped everything
	// correctly — and a check on the counter would call that a failure. What
	// the bound is about is whether the loop TERMINATED, which only the exit
	// path knows.
	var cleared bool
	rounds := 1 + maxDropRounds
	for rounds > 0 {
		rounds--
		tables, err := dropCandidates(ctx, owner, unreferencedTables)
		if err != nil {
			return err
		}
		if len(tables) == 0 {
			// No leaves and the schema is not empty means a reference cycle;
			// anyTables breaks it. Empty here too means there is nothing left
			// to drop, which is the loop's ordinary exit.
			if tables, err = dropCandidates(ctx, owner, anyTables); err != nil {
				return err
			}
			if len(tables) == 0 {
				cleared = true
				break
			}
		}
		// CASCADE, and the batch is named in ONE statement: two tables in the
		// same batch may reference each other, and dropping them one at a time
		// would refuse on the dependency rather than remove it. Same injection
		// posture as the reset's DELETE batch — every name is quote_ident()
		// over pg_class, schema-qualified, never caller input.
		if _, err := owner.Exec(ctx, `DROP TABLE `+strings.Join(tables, `, `)+` CASCADE`); err != nil {
			return fmt.Errorf("dropping tables: %w", err)
		}
	}
	if !cleared {
		return fmt.Errorf("public still holds tables after %d drop rounds — something is re-creating them", maxDropRounds)
	}
	// Extensions next, one statement each. They are the second concentration:
	// btree_gist and vector each drag hundreds of operators, operator classes and
	// support functions along, and leaving them to the final CASCADE put that
	// back into a single transaction (1086 locks measured, against 653 split).
	//
	// plpgsql is excluded because it is the language every trigger function is
	// written in and no migration installs it; the others are ours and the
	// migrations recreate them. Dropped one at a time, so a dependency between
	// two of them cannot make the batch refuse.
	exts, err := queryIdents(ctx, owner,
		`SELECT quote_ident(extname) FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname`)
	if err != nil {
		return fmt.Errorf("listing extensions to drop: %w", err)
	}
	for _, ext := range exts {
		// IF EXISTS: dropping one extension can cascade another away, and the
		// list was taken before any of them ran.
		if _, err := owner.Exec(ctx, `DROP EXTENSION IF EXISTS `+ext+` CASCADE`); err != nil {
			return fmt.Errorf("dropping extension %s: %w", ext, err)
		}
	}
	// Whatever is left is not a table or an extension — functions, types, the odd
	// view a batch did not cascade away — and there are few enough of them (18
	// locks measured) that the wholesale drop is now a small transaction. CREATE
	// and GRANT ride the same statement because the schema must not be observable
	// as missing.
	if _, err := owner.Exec(ctx,
		`DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		return fmt.Errorf("recreating the public schema: %w", err)
	}
	return nil
}
