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

// Runtime is the capability handle a governed tool is invoked with. It is the
// only way an extension reaches anything the core OFFERS at run time: the
// Extension value a unit's New() returns is inert declaration and holds no
// handle (see the package doc), so no capability this surface publishes is
// reachable without a Runtime the core built for that one call.
//
// WHAT THAT IS NOT. A unit is ordinary Go compiled into the same process, so
// this is a narrow, well-lit door in a building with no walls: a handler can
// import os and read the environment, open its own database connection, reach
// the network, or call into any package it lists in its own go.mod. Nothing
// here prevents that and nothing in this repository does either. The sentence
// above is a statement about the SHAPE OF THE OFFERED SURFACE — what a unit is
// given, and when — not a containment claim.
//
// THE TIER'S THREAT MODEL, said plainly because the rest of this file reads
// like a boundary: the units this tier is built for are REVIEWED, FIRST-PARTY
// OR OTHERWISE TRUSTED code. The composed set IS the trust boundary — the
// vanilla tree ships only first-party units and an installation adds one
// deliberately. Every wall documented here is DEFENCE IN DEPTH AGAINST
// MISTAKES: it makes the accidental cross-tenant query, the forgotten scope,
// the retained handle into a loud failure instead of a silent one. None of it
// is a sandbox against a hostile unit, and running an untrusted unit in a
// composed build is outside what this design supports. Issue #628 (a per-unit
// database role) is the first change that would move any part of this from
// convention to enforcement, and even that bounds only the database.
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
	// workspace the invocation belongs to. The core takes that workspace
	// from the INVOCATION, not from the ctx passed here: everything else
	// this ctx carries is honoured — a shorter deadline, a cancellation, the
	// values a handler put on it — but the tenant is re-bound from the call
	// the Runtime was minted for. So a handler cannot widen its own scope,
	// and it cannot do it by building a context either. The tenant policies
	// then hold whatever SQL it writes to that workspace.
	//
	// fn returning an error rolls the transaction back; returning nil
	// commits it. The Tx handed to fn is valid only for that call — it is
	// released with the transaction, and so is every Rows opened from it.
	//
	// On a Runtime the core has already released, Tx answers
	// ErrRuntimeExpired without opening anything.
	Tx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error

	// Caller is WHO this invocation is running as, taken from the invocation
	// itself and never from anything the handler supplies. A unit that stamps
	// authorship, writes its own audit line, or varies behaviour by seat needs
	// this, and the alternative — accepting an identity in the request body —
	// is one every caller can forge.
	//
	// It answers from state the core already holds, so it costs no query and
	// cannot fail. Everything it does NOT carry is deliberate: a display name,
	// an email or a team list would each be an app_user read, and a unit that
	// wants one should be given a capability that says so rather than have
	// every invocation pay for it.
	//
	// A job tick has no human behind it and answers the zero Caller
	// (CallerSystem, empty UserID); see Job.
	Caller() Caller
}

// CallerType is which kind of principal an invocation is running as. It mirrors
// the core's own principal vocabulary, restated here because the published
// surface may not export a kernel type — a unit compiles against this package
// and nothing beneath it.
type CallerType string

const (
	// CallerSystem is an invocation with no principal behind it: a scheduled
	// job tick, and the zero value, so an unset Caller reads as the least
	// authority rather than as a human.
	CallerSystem CallerType = ""
	// CallerHuman is a person acting through a session.
	CallerHuman CallerType = "human"
	// CallerAgent is an agent acting under a Passport, always on some human's
	// authority — OnBehalfOf names them.
	CallerAgent CallerType = "agent"
	// CallerConnector is an inbound integration acting on a human's authority.
	CallerConnector CallerType = "connector"
)

// Caller identifies the principal an invocation runs as. It is a VALUE, copied
// at construction: holding one after the invocation ends is harmless, unlike a
// retained Runtime, because it grants nothing.
type Caller struct {
	// Type is which kind of principal this is. The zero value is CallerSystem.
	Type CallerType

	// UserID is the app_user behind the call, as a string because the
	// published surface does not export the core's id type. Empty for
	// CallerSystem.
	//
	// For an agent or a connector this is the HUMAN whose authority the call
	// carries, not a synthetic id for the agent: a unit stamping authorship
	// wants the person accountable for the row, and "agent ≤ human" already
	// holds that agent's scopes to that human's.
	UserID string

	// IsAgent reports whether an agent or connector produced this call rather
	// than a person acting directly. A unit that must not be driven by an
	// agent checks this; a unit that only wants authorship uses UserID and
	// ignores it.
	IsAgent bool
}

// Tx is a workspace-pinned database transaction, and the whole of it: the
// three verbs a unit's own tables need — write a row, read one, read many.
// It deliberately does NOT mirror a driver's API. Batching, copy protocols,
// savepoints, listen/notify and connection-level state are all absent
// because none of them can be handed to extension code without also handing
// over things the core must keep (the connection's lifetime, its GUCs, its
// prepared-statement cache).
//
// The SQL is the extension's own, and nothing here parses or rewrites it: a
// wall made of statement inspection is a wall made of guesses. The wall is the
// DATABASE's, and this is where the tier's threat model (see Runtime) has to be
// stated concretely, because the honest answer differs by reader.
//
// AGAINST MISTAKES, which is what this is for, the tenant pin holds. The
// transaction is bound to the calling workspace before fn runs — from the
// INVOCATION, never from a context the handler supplies — and the
// row-level-security policies key on that binding. A unit's query that forgets
// a workspace predicate returns its own tenant's rows and nothing else, and a
// write that names another tenant's id is refused by the policy. That is a real
// property and it is the one this seam is designed around.
//
// AGAINST HOSTILE CODE, it does not hold, and neither does anything else here.
// The pin is a transaction-scoped GUC (app.workspace_id), and a unit can rebind
// it by executing `SELECT set_config('app.workspace_id', …, true)` through the
// very verbs below — after which the policies read the new value. This was
// verified against the shipped schema, not assumed. It is not fixable at this
// layer: re-binding before every statement is defeated by ONE statement (a CTE
// that rebinds and a sibling scan that reads under the new value, in the same
// query), and a PostgreSQL GUC cannot be made immutable for a session. The real
// answer is that RLS must key on something a unit cannot set — a per-unit
// database ROLE, tracked as issue #628 — and until that lands the tenant wall
// is defence in depth, not a boundary.
//
// WHAT IS NOT WALLED AT ALL, today, within one tenant:
//   - the CORE's tables. This runs on the shared application role, so a unit's
//     SQL can address any table that role can.
//   - OTHER UNITS' tables. Every unit's migration grants the same application
//     role DML on its own ext_<name>_* tables, so unit A can read, rewrite or
//     delete unit B's rows.
//   - extension_secret. It is workspace-RLS'd and nothing more, so the secret
//     namespacing Secrets enforces at the PORT is reachable around it through
//     these three verbs — and because a unit runs in-process it can also read
//     the keyvault root key from the environment and decrypt the ciphertext
//     directly. "Sovereign inside its namespace, powerless outside it" is a
//     property of polite units, not of this transaction.
//
// All four are the same missing thing (#628) and all four are inside the
// trusted-unit threat model above.
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
//	rows, err := tx.Query(ctx, "SELECT id, body FROM ext_notes_note")
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
