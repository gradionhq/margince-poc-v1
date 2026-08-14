// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The published transaction seam over pgx: the three SQL verbs a unit's own
// tables need, the door onto the governed core port, and the two cursor
// adapters behind Rows and Row. Split from extruntime.go, which owns the
// Runtime's lifetime and scope, when the Runtime grew the port — these are the
// types a unit HOLDS, and that is one concern.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extensionTx bridges the published three-verb seam to pgx. It lives in
// internal/ and not in pkg/ because pkg/ is stdlib-only (depguard
// pkg-purity, and TestPublishedSurfaceIsPure): a published interface can
// describe a transaction, but only the core can hold one.
//
// There is no lifetime guard on this type. A Tx used after its transaction
// ended answers pgx's own ErrTxClosed, which is already an honest refusal —
// and it is the accurate one, because the transaction can end while the
// Runtime is still perfectly live (the callback returned, the call did not).
// ErrRuntimeExpired there would name the wrong fault.
type extensionTx struct {
	tx pgx.Tx
	// core is what the transaction can reach BESIDE the unit's own SQL, and it
	// is built by the Runtime rather than by this type: whether the invocation
	// has a caller, and what the role bound at boot, are facts about the call,
	// not about the transaction.
	core extensionCore
	// ledger records what the unit's OWN SQL did — the audit row and the bus
	// event the three verbs below cannot write for it. Built by the Runtime for
	// the same reason core is: which unit is writing, and under whose identity,
	// are facts about the invocation rather than about the transaction.
	ledger extensionLedger
}

//nolint:ireturn // returning the published port IS the seam: a unit holds extension.Core, never a core type.
func (t extensionTx) Core() extension.Core {
	return t.core
}

func (t extensionTx) Record(ctx context.Context, ch extension.Change, ev extension.Event) error {
	return t.ledger.Record(ctx, ch, ev)
}

func (t extensionTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

//nolint:ireturn // Rows is the published cursor; a unit must never hold pgx.Rows.
func (t extensionTx) Query(ctx context.Context, sql string, args ...any) (extension.Rows, error) {
	rows, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return extensionRows{rows: rows}, nil
}

//nolint:ireturn // Row is the published deferred read; the error is deferred to Scan by design.
func (t extensionTx) QueryRow(ctx context.Context, sql string, args ...any) extension.Row {
	return extensionRow{row: t.tx.QueryRow(ctx, sql, args...)}
}

// extensionRows is pgx's cursor behind the published one. The four methods
// line up exactly, which is why the published seam was spelled this way.
type extensionRows struct{ rows pgx.Rows }

func (r extensionRows) Next() bool             { return r.rows.Next() }
func (r extensionRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r extensionRows) Err() error             { return r.rows.Err() }
func (r extensionRows) Close()                 { r.rows.Close() }

// extensionRow defers one read's error to Scan, translating the empty match
// into the published sentinel — a unit matching on pgx.ErrNoRows would be
// binding a driver this surface never published.
type extensionRow struct{ row pgx.Row }

func (r extensionRow) Scan(dest ...any) error {
	if err := r.row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return extension.ErrNoRows
		}
		return err
	}
	return nil
}
