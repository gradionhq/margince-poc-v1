// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crmdemo

// The fakes the handler tests run against.
//
// A unit's own suite cannot reach the core: extensions/ modules import only the
// published surface, so there is no pool, no custodian and no transaction here.
// What CAN be tested at this level is exactly what this unit owns — the
// argument decoding, the refusals, the HMAC construction, the result shapes,
// and the statements each handler issues. Whether those statements are correct
// SQL against a real schema is the migration gate's and the integration lane's
// question, and answering it twice with a hand-rolled SQL interpreter here
// would be a second, weaker copy of a check that already exists.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// fakeRuntime is one invocation's Runtime: a secret namespace and a single
// transaction the handler is handed.
type fakeRuntime struct {
	secrets *fakeSecrets
	tx      *fakeTx
	// txErr stands in for the core refusing to open a transaction at all — an
	// expired Runtime, an unwired role — which every handler must propagate
	// rather than answer over.
	txErr error
}

func newRuntime() *fakeRuntime {
	return &fakeRuntime{secrets: &fakeSecrets{stored: map[string][]byte{}}, tx: &fakeTx{}}
}

func (r *fakeRuntime) Secrets() extension.Secrets { return r.secrets }

func (r *fakeRuntime) Tx(ctx context.Context, fn func(context.Context, extension.Tx) error) error {
	if r.txErr != nil {
		return r.txErr
	}
	return fn(ctx, r.tx)
}

// fakeTx records what a handler asked the database to do and answers with what
// the test scripted.
type fakeTx struct {
	statements []string
	args       [][]any

	rows     [][]any // what Query hands back, one slice per row
	row      []any   // what QueryRow scans into dest
	affected int64
	err      error

	// lastRows is the cursor Query handed the handler, kept so a test can ask
	// whether the handler closed it.
	lastRows *fakeRows
}

func (t *fakeTx) record(sql string, args []any) {
	t.statements = append(t.statements, sql)
	t.args = append(t.args, args)
}

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	t.record(sql, args)
	return t.affected, t.err
}

func (t *fakeTx) Query(_ context.Context, sql string, args ...any) (extension.Rows, error) {
	t.record(sql, args)
	if t.err != nil {
		return nil, t.err
	}
	t.lastRows = &fakeRows{rows: t.rows}
	return t.lastRows, nil
}

func (t *fakeTx) QueryRow(_ context.Context, sql string, args ...any) extension.Row {
	t.record(sql, args)
	return fakeRow{values: t.row, err: t.err}
}

// only returns the single statement the handler issued, failing when it issued
// a different number — a handler that quietly took two round trips where one
// was claimed is the thing worth catching.
func (t *fakeTx) only(tb testing.TB) string {
	tb.Helper()
	if len(t.statements) != 1 {
		tb.Fatalf("the handler issued %d statements, want exactly one:\n%s", len(t.statements), strings.Join(t.statements, "\n---\n"))
	}
	return t.statements[0]
}

type fakeRows struct {
	rows    [][]any
	current []any
	err     error
	closed  bool
}

func (r *fakeRows) Next() bool {
	if len(r.rows) == 0 {
		return false
	}
	r.current, r.rows = r.rows[0], r.rows[1:]
	return true
}

func (r *fakeRows) Scan(dest ...any) error { return scanInto(r.current, dest) }
func (r *fakeRows) Err() error             { return r.err }
func (r *fakeRows) Close()                 { r.closed = true }

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.values == nil {
		return extension.ErrNoRows
	}
	return scanInto(r.values, dest)
}

// scanInto copies a scripted row into a handler's destinations. Only the three
// column types this unit selects are handled; anything else is a test-fixture
// mistake and says so rather than scanning a zero value.
func scanInto(values []any, dest []any) error {
	if len(values) != len(dest) {
		return errors.New("fake: the scripted row has a different width from the scan destinations")
	}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			s, ok := value.(string)
			if !ok {
				return errors.New("fake: scripted a non-string into a *string")
			}
			*target = s
		case *time.Time:
			ts, ok := value.(time.Time)
			if !ok {
				return errors.New("fake: scripted a non-time into a *time.Time")
			}
			*target = ts
		default:
			return errors.New("fake: no scan support for this destination type")
		}
	}
	return nil
}

// fakeSecrets is the unit's own namespace. It models the two facts the handlers
// depend on: a miss is ErrSecretNotFound, and a Put replaces.
type fakeSecrets struct {
	stored map[string][]byte
	err    error
}

func (s *fakeSecrets) Get(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	value, ok := s.stored[key]
	if !ok {
		return nil, extension.ErrSecretNotFound
	}
	return value, nil
}

func (s *fakeSecrets) Put(_ context.Context, key string, secret []byte) error {
	if s.err != nil {
		return s.err
	}
	s.stored[key] = secret
	return nil
}

func (s *fakeSecrets) Delete(_ context.Context, key string) error {
	if s.err != nil {
		return s.err
	}
	if _, ok := s.stored[key]; !ok {
		return extension.ErrSecretNotFound
	}
	delete(s.stored, key)
	return nil
}

// The user-scoped half is unreachable from this unit — it declares one
// workspace-scoped key and calls none of these — so they refuse rather than
// pretend, which would make a handler that started using them pass silently.
var errUserScopeUnused = errors.New("fake: crm-demo declares no user-scoped secret")

func (s *fakeSecrets) GetUser(context.Context, extension.UserID, string) ([]byte, error) {
	return nil, errUserScopeUnused
}

func (s *fakeSecrets) PutUser(context.Context, extension.UserID, string, []byte) error {
	return errUserScopeUnused
}

func (s *fakeSecrets) DeleteUser(context.Context, extension.UserID, string) error {
	return errUserScopeUnused
}
