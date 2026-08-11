// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"context"
	"errors"
	"testing"
)

// The transaction seam is a published CONTRACT with no implementation in
// this package — the core builds one per invocation. What can be pinned here
// is that the contract is satisfiable in stdlib terms, which is the property
// the whole surface rests on: if a later edit put a driver type in one of
// these signatures, this file would stop compiling before the purity gates
// even ran, and it names why.
//
// It doubles as the reference shape for the core's adapter: a released
// Runtime answers ErrRuntimeExpired from Tx, not just from Secrets.

type fakeRuntime struct {
	secrets Secrets
	live    bool
	rows    [][]string
	caller  Caller
}

func (r *fakeRuntime) Secrets() Secrets { return r.secrets }

// Caller is a plain field read: the identity is decided when the core builds
// the Runtime, so a fake has nothing to derive it from and nothing to refuse
// — which is itself the shape the contract promises (it cannot fail, and it
// answers after release too).
func (r *fakeRuntime) Caller() Caller { return r.caller }

func (r *fakeRuntime) Tx(ctx context.Context, fn func(context.Context, Tx) error) error {
	if !r.live {
		return ErrRuntimeExpired
	}
	return fn(ctx, &fakeTx{rows: r.rows})
}

type fakeTx struct{ rows [][]string }

func (t *fakeTx) Exec(_ context.Context, _ string, args ...any) (int64, error) {
	return int64(len(args)), nil
}

func (t *fakeTx) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return &fakeRows{rows: t.rows, at: -1}, nil
}

func (t *fakeTx) QueryRow(_ context.Context, _ string, _ ...any) Row {
	if len(t.rows) == 0 {
		return errRow{ErrNoRows}
	}
	return &fakeRows{rows: t.rows[:1], at: 0}
}

type fakeRows struct {
	rows [][]string
	at   int
}

func (r *fakeRows) Next() bool { r.at++; return r.at < len(r.rows) }
func (r *fakeRows) Err() error { return nil }
func (r *fakeRows) Close()     {}

func (r *fakeRows) Scan(dest ...any) error {
	if r.at >= len(r.rows) {
		return ErrNoRows
	}
	for i, d := range dest {
		p, ok := d.(*string)
		if !ok || i >= len(r.rows[r.at]) {
			return errors.New("fake: unsupported scan target")
		}
		*p = r.rows[r.at][i]
	}
	return nil
}

type errRow struct{ err error }

func (e errRow) Scan(...any) error { return e.err }

var (
	_ Runtime = (*fakeRuntime)(nil)
	_ Tx      = (*fakeTx)(nil)
	_ Rows    = (*fakeRows)(nil)
	_ Row     = (*fakeRows)(nil)
	_ Row     = errRow{}
)

// TestRuntimeTxContractIsUsable walks the four things the seam exists for —
// a write, a many-row read, a single-row read, and an empty single-row read
// — through a stdlib-only implementation.
func TestRuntimeTxContractIsUsable(t *testing.T) {
	rt := &fakeRuntime{live: true, rows: [][]string{{"first"}, {"second"}}}

	var got []string
	if err := rt.Tx(context.Background(), func(ctx context.Context, tx Tx) error {
		n, err := tx.Exec(ctx, "INSERT INTO ext_demo_note (body) VALUES ($1)", "hello")
		if err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("Exec reported %d rows affected, want 1", n)
		}

		rows, err := tx.Query(ctx, "SELECT body FROM ext_demo_note")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			got = append(got, body)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		var one string
		return tx.QueryRow(ctx, "SELECT body FROM ext_demo_note LIMIT 1").Scan(&one)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("iterated %v, want [first second]", got)
	}

	empty := &fakeRuntime{live: true}
	if err := empty.Tx(context.Background(), func(ctx context.Context, tx Tx) error {
		var body string
		return tx.QueryRow(ctx, "SELECT body FROM ext_demo_note").Scan(&body)
	}); !errors.Is(err, ErrNoRows) {
		t.Fatalf("an empty single-row read = %v, want ErrNoRows", err)
	}
}

// TestRuntimeTxFailsClosedWhenReleased: the lifetime guarantee is the core's
// to keep, and it covers Tx as well as Secrets — a retained Runtime must not
// be able to open a transaction on a workspace the call has left.
func TestRuntimeTxFailsClosedWhenReleased(t *testing.T) {
	released := &fakeRuntime{live: false}
	called := false
	err := released.Tx(context.Background(), func(context.Context, Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrRuntimeExpired) {
		t.Fatalf("a released Runtime's Tx = %v, want ErrRuntimeExpired", err)
	}
	if called {
		t.Fatal("a released Runtime ran the callback before refusing")
	}
}

// TestTxRollsBackOnError pins the contract's one control-flow rule: the
// callback's error is the transaction's verdict, and it reaches the caller
// unchanged so a handler can branch on its own sentinel.
func TestTxRollsBackOnError(t *testing.T) {
	sentinel := errors.New("the handler's own refusal")
	rt := &fakeRuntime{live: true}
	if err := rt.Tx(context.Background(), func(context.Context, Tx) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Tx = %v, want the callback's own error", err)
	}
}
