// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

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
	"github.com/gradionhq/margince/backend/pkg/extension/crm"
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

	// caller is who the invocation runs as. The default is a HUMAN rather than
	// the zero Caller: a tool invocation always has a principal, and defaulting
	// to CallerSystem would make every author assertion in the suite pass over
	// the one path — the job's — that legitimately has none.
	caller extension.Caller
}

func newRuntime() *fakeRuntime {
	return &fakeRuntime{
		secrets: &fakeSecrets{stored: map[string][]byte{}},
		tx:      &fakeTx{},
		caller:  extension.Caller{Type: extension.CallerHuman, UserID: callerUserID},
	}
}

// callerUserID is the human the fake invocations run as, and it is a canonical
// UUID because that is what the column it lands in accepts.
const callerUserID = "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31"

func (r *fakeRuntime) Secrets() extension.Secrets { return r.secrets }

func (r *fakeRuntime) Caller() extension.Caller { return r.caller }

func (r *fakeRuntime) Tx(ctx context.Context, fn func(context.Context, extension.Tx) error) error {
	if r.txErr != nil {
		return r.txErr
	}
	return fn(ctx, r.tx)
}

// noteRow scripts one row of noteColumns, in that order.
//
// It exists so that a column added to the projection is ONE edit in the
// fixtures rather than one per scripted row — and, more usefully, so that the
// width the handler scans and the width the tests script cannot silently
// diverge into a "the scripted row has a different width" failure in every
// test at once.
//
// An empty authorUserID scripts BOTH author columns as NULL, which is the
// tick's row and the shape the both-or-neither CHECK admits. isAgent is then
// ignored, deliberately: a fixture cannot script the half-written author the
// database refuses, so no test can assert behaviour for a row that cannot exist.
func noteRow(id string, kind noteKind, body, authorUserID string, isAgent bool, at time.Time) []any {
	return filedNoteRow(id, kind, body, authorUserID, isAgent, "", at)
}

// filedNoteRow is noteRow for a note that reached the timeline: filedActivityID
// empty means the ordinary case, a note nobody filed, which is what the column
// holds for every row the other tests script.
func filedNoteRow(id string, kind noteKind, body, authorUserID string, isAgent bool, filedActivityID string, at time.Time) []any {
	var (
		userID       *string
		isAgentValue *bool
		filed        *string
	)
	if authorUserID != "" {
		userID, isAgentValue = &authorUserID, &isAgent
	}
	if filedActivityID != "" {
		filed = &filedActivityID
	}
	return []any{id, string(kind), body, userID, isAgentValue, filed, at}
}

// fakeTx records what a handler asked the database to do and answers with what
// the test scripted.
type fakeTx struct {
	statements []string
	args       [][]any

	core     *fakeCore
	rows     [][]any // what Query hands back, one slice per row
	row      []any   // what QueryRow scans into dest
	affected int64
	err      error
	// failFrom is the 1-based statement the error starts at; 0 fails every
	// statement. A handler issuing two statements has two distinct failure
	// modes, and a fake that could only fail the first would let a test named
	// for the second pass without ever reaching it.
	failFrom int

	// lastRows is the cursor Query handed the handler, kept so a test can ask
	// whether the handler closed it.
	lastRows *fakeRows

	// audited and published are what the handler recorded about its own write.
	// recordErr is their own error script rather than failFrom's, because a
	// ledger write is not a statement this fake counts — and because "the
	// record failed" and "the third statement failed" are different failures a
	// handler must propagate from different places.
	audited   []extension.Change
	published []extension.Event
	recordErr error
}

func (t *fakeTx) record(sql string, args []any) {
	t.statements = append(t.statements, sql)
	t.args = append(t.args, args)
}

// Core hands back the scripted port. It is nil for every test that does not
// file, which is the accurate answer for those: a handler that reached for the
// core port when the test did not expect one panics rather than passing.
func (t *fakeTx) Core() extension.Core { return t.core }

// Record records the ledger row and the event a handler asked for.
//
// It runs the PUBLISHED Validate on both halves rather than accepting whatever
// it is given, because that is the part of the real port a unit's own suite can
// and should feel: an entity outside this unit's namespace, an id that is not a
// UUID, an image that is not JSON, a verb that is not a verb are all refusals
// the core would make, and a fake that waved them through would let a handler
// ship a call the real port rejects. What it deliberately does NOT model is the
// core's half — the actor, the workspace, the attribution, the namespace the
// event type is built from — which is tested where it lives.
func (t *fakeTx) Record(_ context.Context, ch extension.Change, ev extension.Event) error {
	if err := ch.Validate(); err != nil {
		return err
	}
	if err := ev.Validate(); err != nil {
		return err
	}
	if t.recordErr != nil {
		return t.recordErr
	}
	t.audited = append(t.audited, ch)
	t.published = append(t.published, ev)
	return nil
}

// fakeCore is the governed port as a filing test needs it: it records what the
// unit asked the core to write, and answers with what the test scripted.
//
// A fake at the PUBLISHED seam and nothing below it. What the real port does —
// the RBAC gate, the row-scope check on the subject, the audit row, the outbox
// event — is the core's, tested where it lives; what these tests own is the
// unit's half: which request it builds, and what it does with the answer.
type fakeCore struct {
	requested []crm.CreateActivityRequest
	activity  crm.Activity
	err       error
}

func (c *fakeCore) Activities() extension.ActivityRepo { return c }

func (c *fakeCore) Create(_ context.Context, in crm.CreateActivityRequest) (crm.Activity, error) {
	c.requested = append(c.requested, in)
	if c.err != nil {
		return crm.Activity{}, c.err
	}
	return c.activity, nil
}

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	t.record(sql, args)
	return t.affected, t.failure()
}

// failure reports the scripted error for the statement just recorded.
func (t *fakeTx) failure() error {
	if t.err == nil || len(t.statements) < t.failFrom {
		return nil
	}
	return t.err
}

func (t *fakeTx) Query(_ context.Context, sql string, args ...any) (extension.Rows, error) {
	t.record(sql, args)
	if err := t.failure(); err != nil {
		return nil, err
	}
	t.lastRows = &fakeRows{rows: t.rows}
	return t.lastRows, nil
}

func (t *fakeTx) QueryRow(_ context.Context, sql string, args ...any) extension.Row {
	t.record(sql, args)
	return fakeRow{values: t.row, err: t.failure()}
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

// scanInto copies a scripted row into a handler's destinations. Only the column
// types this unit selects are handled; anything else is a test-fixture mistake
// and says so rather than scanning a zero value.
//
// The **string and **bool cases are the NULLABLE columns (author_user_id,
// author_is_agent), and they model the one behaviour the handler depends on: a
// scripted nil leaves the destination pointer nil, which is how a row with no
// author reaches scanNote. A fake that could not express NULL would let the
// tick's authorless rows go untested at exactly the layer that decides whether
// `author` appears in the response.
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
		case **string:
			s, ok := value.(*string)
			if !ok {
				return errors.New("fake: scripted something other than a *string into a nullable text column")
			}
			*target = s
		case **bool:
			b, ok := value.(*bool)
			if !ok {
				return errors.New("fake: scripted something other than a *bool into a nullable boolean column")
			}
			*target = b
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
var errUserScopeUnused = errors.New("fake: notes declares no user-scoped secret")

func (s *fakeSecrets) GetUser(context.Context, extension.UserID, string) ([]byte, error) {
	return nil, errUserScopeUnused
}

func (s *fakeSecrets) PutUser(context.Context, extension.UserID, string, []byte) error {
	return errUserScopeUnused
}

func (s *fakeSecrets) DeleteUser(context.Context, extension.UserID, string) error {
	return errUserScopeUnused
}
