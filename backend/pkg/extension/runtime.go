// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Runtime, the transaction seam, and their errors are part of the published
// extension surface.
//
//margince:extension-surface

package extension

import (
	"context"
	"errors"
)

// ErrRuntimeExpired reports that a Runtime outlived the call it was built
// for. The core mints one per invocation over call-scoped resources and
// invalidates it the moment the handler returns, so a handler that stashes
// its Runtime in a package variable and reaches for it on a later call is
// told so, rather than quietly working against released state.
//
// This is a guarantee the CORE keeps, not one this type can make about
// itself: an interface cannot enforce its own lifetime, so what is published
// here is the error a handler must expect, and the invalidation lives in the
// core's per-call adapter.
var ErrRuntimeExpired = errors.New("extension: this runtime belongs to a call that has finished")

// ErrNoRows reports that a single-row read matched nothing. It is what
// Row.Scan returns for an empty result, so the ordinary "is it there?" read
// is an errors.Is check rather than a sentinel the extension has to guess.
var ErrNoRows = errors.New("extension: the query matched no rows")

// Runtime is the capability handle a governed tool is invoked with. It is
// the ONLY way an extension reaches anything in the core at run time: the
// Extension value a unit's New() returns is inert declaration and holds no
// handle (see the package doc), so nothing an extension can do is reachable
// without a Runtime the core built for that one call.
//
// The core constructs it and knows which unit it is invoking, which is why
// nothing here takes a unit name or re-scopes to one — a handler holds
// exactly the namespace it was invoked under.
//
// Its lifetime is the invocation. It must not be retained: every method on a
// Runtime the core has released answers ErrRuntimeExpired.
//
// Like Extension, Runtime grows ADDITIVELY — a new capability kind is a new
// method — so a handler written against today's surface keeps compiling.
// Additive growth of an interface is only safe because extensions consume
// Runtime and never implement it.
type Runtime interface {
	// Secrets is the unit's own secret namespace in the calling workspace.
	Secrets() Secrets

	// Tx runs fn inside ONE database transaction, already pinned to the
	// workspace the invocation belongs to. The pinning happens in the core
	// before fn is called and there is no parameter through which fn could
	// ask for a different workspace, so a handler cannot widen its own
	// scope; the tenant policies on the unit's own tables then hold whatever
	// SQL it writes to that workspace.
	//
	// fn returning an error rolls the transaction back; returning nil
	// commits it. The Tx handed to fn is valid only for that call — it is
	// released with the transaction, and so is every Rows opened from it.
	//
	// On a Runtime the core has already released, Tx answers
	// ErrRuntimeExpired without opening anything.
	Tx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is a workspace-pinned database transaction, and the whole of it: the
// three verbs a unit's own tables need — write a row, read one, read many.
// It deliberately does NOT mirror a driver's API. Batching, copy protocols,
// savepoints, listen/notify and connection-level state are all absent
// because none of them can be handed to extension code without also handing
// over things the core must keep (the connection's lifetime, its GUCs, its
// prepared-statement cache).
//
// The SQL is the extension's own, run under the extension's own database
// role against its own ext_<name>_* tables. Nothing here parses or rewrites
// it: the wall around what a unit may touch is the role's grants and the
// tenant policies, which hold whatever statement arrives.
//
// args is ...any because SQL arguments are genuinely heterogeneous — a
// statement's parameters are whatever its placeholders are — and every
// database/sql-shaped API in the ecosystem, the pgx this is implemented over
// included, spells them the same way. AGENTS.md's no-`any` rule is aimed at
// TypeScript's escape hatches; a Go query-argument list is not one.
type Tx interface {
	// Exec runs a statement that returns no rows (INSERT, UPDATE, DELETE)
	// and reports how many rows it affected — which is how a delete says
	// whether it deleted anything.
	Exec(ctx context.Context, sql string, args ...any) (rowsAffected int64, err error)

	// Query runs a statement that returns rows. The caller must Close the
	// Rows; it is released with the transaction either way, but holding one
	// open pins the connection until then.
	Query(ctx context.Context, sql string, args ...any) (Rows, error)

	// QueryRow runs a statement expected to match at most one row. Any error
	// — including ErrNoRows for an empty result — is deferred to Row.Scan,
	// so the ordinary read is two lines rather than four.
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// Rows is a forward-only cursor over a Query result.
//
// The idiom is the stdlib's, deliberately, so it needs no learning:
//
//	rows, err := tx.Query(ctx, "SELECT id, body FROM ext_crm_demo_note")
//	if err != nil {
//		return err
//	}
//	defer rows.Close()
//	for rows.Next() {
//		if err := rows.Scan(&id, &body); err != nil {
//			return err
//		}
//	}
//	return rows.Err()
type Rows interface {
	// Next advances to the next row, reporting false when the result is
	// exhausted OR when reading it failed — Err says which.
	Next() bool

	// Scan reads the current row into dest, one pointer per selected column.
	Scan(dest ...any) error

	// Err reports the error that ended the iteration, or nil if the result
	// was simply exhausted. A loop that does not check it cannot tell a
	// complete read from a truncated one.
	Err() error

	// Close releases the rows. It is safe to call more than once, and safe
	// after Next has returned false.
	Close()
}

// Row is one deferred single-row read.
type Row interface {
	// Scan reads the matched row into dest. It returns ErrNoRows when the
	// query matched nothing, and the query's own error when it failed.
	Scan(dest ...any) error
}
