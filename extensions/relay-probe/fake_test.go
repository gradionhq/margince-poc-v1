// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The fakes this unit's suite runs against.
//
// A unit's own tests cannot reach the core: an extensions/ module imports only
// the published surface, so there is no pool, no custodian and no capture
// pipeline here. What CAN be tested at this level is what this unit owns — the
// argument decoding, the refusals, which statements each handler issues, what
// it hands the ingress port, and the cursor arithmetic. Whether those
// statements are correct SQL against a real schema, and whether the port
// accepts what is handed to it, are the migration gate's and the integration
// lane's questions.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// callerUserID is the member the fake invocations run as, canonical because
// that is what the column it lands in accepts.
const callerUserID = "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31"

// fakeRuntime is one invocation's Runtime.
type fakeRuntime struct {
	secrets *fakeSecrets
	tx      *fakeTx
	caller  extension.Caller

	// txErr stands in for the core refusing to open a transaction at all — an
	// expired Runtime, an unwired role — which every handler must propagate
	// rather than answer over.
	txErr error

	// ingested is every record handed to the port, in order, and ingestErr is
	// what the port answers from the nth call onward. The pair is what lets a
	// test say "the tick stopped at the third message and moved nothing".
	ingested    []extension.Record
	ingestedOn  []extension.UserID
	ingestErr   error
	ingestFrom  int
	ingestCalls int
}

func newRuntime() *fakeRuntime {
	return &fakeRuntime{
		secrets: &fakeSecrets{stored: map[string][]byte{}},
		tx:      &fakeTx{},
		caller:  extension.Caller{Type: extension.CallerHuman, UserID: callerUserID},
	}
}

// unattended is the Runtime a job tick holds: nobody behind it, which is what
// the core answers for a scheduled run.
func (r *fakeRuntime) unattended() *fakeRuntime {
	r.caller = extension.Caller{Type: extension.CallerSystem}
	return r
}

func (r *fakeRuntime) Secrets() extension.Secrets { return r.secrets }

func (r *fakeRuntime) Caller() extension.Caller { return r.caller }

func (r *fakeRuntime) Tx(ctx context.Context, fn func(context.Context, extension.Tx) error) error {
	if r.txErr != nil {
		return r.txErr
	}
	// The transaction is marked open for the duration, so a handler that
	// ingested from inside one meets the same refusal the core gives — which
	// is the defect this unit's poll is shaped to avoid, and a fake that
	// allowed it would let that shape rot silently.
	r.tx.open = true
	defer func() { r.tx.open = false }()
	return fn(ctx, r.tx)
}

// Ingest records what the unit handed the core, applies the same record grammar
// the real port applies, and refuses from inside a transaction exactly as it
// does.
//
// The grammar is here for the reason the nesting refusal is, and it is the
// lesson this unit already paid for once: a fake that accepts what the core
// refuses lets a unit's whole suite agree with a bug, and the only thing that
// disagrees is production. `Record.Validate` is published, so this costs one
// call rather than a second copy of the rules.
func (r *fakeRuntime) Ingest(_ context.Context, on extension.UserID, rec extension.Record) (extension.Result, error) {
	if r.tx.open {
		return extension.Result{}, extension.ErrNestedIngest
	}
	r.ingestCalls++
	// Recorded BEFORE any refusal: what the unit HANDED the core is the thing
	// under test, and a record dropped here is one a failing test cannot show.
	r.ingested = append(r.ingested, rec)
	r.ingestedOn = append(r.ingestedOn, on)
	if err := rec.Validate(); err != nil {
		return extension.Result{}, fmt.Errorf("%w: %s", extension.ErrInvalid, err)
	}
	if r.ingestErr != nil && r.ingestCalls >= r.ingestFrom {
		return extension.Result{}, r.ingestErr
	}
	return extension.Result{Disposition: extension.DispositionAccepted}, nil
}

// fakeSecrets is the unit's namespace, user scope only — the installation scope
// is unused by this unit and a fake that served it would invite a handler to
// start using one.
type fakeSecrets struct {
	stored  map[string][]byte
	putErr  error
	getErr  error
	deleted []string
}

func (s *fakeSecrets) Get(context.Context, string) ([]byte, error) {
	return nil, extension.ErrSecretNotFound
}
func (s *fakeSecrets) Put(context.Context, string, []byte) error { return nil }
func (s *fakeSecrets) Delete(context.Context, string) error      { return nil }
func (s *fakeSecrets) userKey(user extension.UserID, key string) string {
	return string(user) + "/" + key
}

func (s *fakeSecrets) GetUser(_ context.Context, user extension.UserID, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	secret, ok := s.stored[s.userKey(user, key)]
	if !ok {
		return nil, extension.ErrSecretNotFound
	}
	return secret, nil
}

func (s *fakeSecrets) PutUser(_ context.Context, user extension.UserID, key string, secret []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.stored[s.userKey(user, key)] = secret
	return nil
}

func (s *fakeSecrets) DeleteUser(_ context.Context, user extension.UserID, key string) error {
	s.deleted = append(s.deleted, s.userKey(user, key))
	delete(s.stored, s.userKey(user, key))
	return nil
}

// fakeTx records what a handler asked the database to do and answers with what
// the test scripted.
type fakeTx struct {
	statements []string
	args       [][]any
	open       bool

	// singleRows is what successive QueryRow calls answer, one entry each and
	// CONSUMED in order. A handler that reads a row and then writes one issues
	// two, and a fake that answered the same values to both could not tell a
	// read-then-write from a write alone.
	singleRows [][]any
	// noRows makes the nth QueryRow (1-based) answer the driver's empty
	// result, which every handler here treats as "there is no such connection"
	// rather than as a failure.
	noRows map[int]bool
	// queryRows is what the next Query hands back.
	queryRows [][]any

	err      error
	failFrom int

	audited   []extension.Change
	published []extension.Event
	rowCalls  int
}

func (t *fakeTx) record(sql string, args []any) {
	t.statements = append(t.statements, sql)
	t.args = append(t.args, args)
}

func (t *fakeTx) failure() error {
	if t.err == nil || len(t.statements) < t.failFrom {
		return nil
	}
	return t.err
}

// Core is nil: this unit files nothing through the governed core port, and a
// fake that handed one back would let a handler start using it unnoticed.
func (t *fakeTx) Core() extension.Core { return nil }

func (t *fakeTx) Record(_ context.Context, ch extension.Change, ev extension.Event) error {
	// The published grammar, run here rather than waved through: an entity
	// outside this unit's namespace, an id that is not a UUID, an image that is
	// not JSON and a verb that is not a verb are all refusals the core makes,
	// and a fake that accepted them would let a handler ship a call the real
	// port rejects.
	if err := ch.Validate(); err != nil {
		return err
	}
	if err := ev.Validate(); err != nil {
		return err
	}
	t.audited = append(t.audited, ch)
	t.published = append(t.published, ev)
	return nil
}

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	t.record(sql, args)
	return 0, t.failure()
}

func (t *fakeTx) Query(_ context.Context, sql string, args ...any) (extension.Rows, error) {
	t.record(sql, args)
	if err := t.failure(); err != nil {
		return nil, err
	}
	rows := &fakeRows{rows: t.queryRows}
	t.queryRows = nil
	return rows, nil
}

func (t *fakeTx) QueryRow(_ context.Context, sql string, args ...any) extension.Row {
	t.record(sql, args)
	t.rowCalls++
	if t.noRows[t.rowCalls] {
		return fakeRow{err: errNoRows}
	}
	var values []any
	if len(t.singleRows) > 0 {
		values, t.singleRows = t.singleRows[0], t.singleRows[1:]
	}
	return fakeRow{values: values, err: t.failure()}
}

// errNoRows is what the core hands a unit for a single-row read that matched
// nothing: the PUBLISHED sentinel, not the driver's own wording.
//
// The distinction is not academic — it is the defect a UAT found. This fake
// used to answer the driver's text, the handler matched on that text, and the
// two agreed with each other while production answered a 500 for the ordinary
// case of a member who has not connected yet. A fake that invents an error the
// core never returns is a suite testing itself.
var errNoRows = extension.ErrNoRows

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.values == nil {
		return errNoRows
	}
	return scanInto(dest, r.values)
}

type fakeRows struct {
	rows   [][]any
	cursor int
	closed bool
	err    error
}

func (r *fakeRows) Next() bool {
	if r.cursor >= len(r.rows) {
		return false
	}
	r.cursor++
	return true
}

func (r *fakeRows) Scan(dest ...any) error { return scanInto(dest, r.rows[r.cursor-1]) }
func (r *fakeRows) Close()                 { r.closed = true }
func (r *fakeRows) Err() error             { return r.err }

// scanInto copies scripted values into a handler's scan destinations. It knows
// only the types this unit's projection actually scans; a new column type is a
// compile-time-shaped failure here rather than a silent zero value.
func scanInto(dest, values []any) error {
	if len(dest) != len(values) {
		return errWidth{want: len(dest), got: len(values)}
	}
	for i, value := range values {
		// Every assertion is CHECKED. An unchecked one answers the handler with
		// a zero value instead of the mistake — a fixture that scripts an int
		// where the projection scans a string reads back "" and the assertion
		// under test passes for a reason the test never states.
		switch target := dest[i].(type) {
		case *string:
			got, ok := value.(string)
			if !ok {
				return errScripted{at: i, want: "string", got: value}
			}
			*target = got
		case *int:
			got, ok := value.(int)
			if !ok {
				return errScripted{at: i, want: "int", got: value}
			}
			*target = got
		case *int64:
			got, ok := value.(int64)
			if !ok {
				return errScripted{at: i, want: "int64", got: value}
			}
			*target = got
		case **time.Time:
			// A TIME, because the column is timestamptz and the driver refuses
			// to scan one into a string. The fake takes the same type the
			// handler asks for, so a projection that goes back to text fails
			// here rather than in production — which is where the first
			// version of it failed.
			if value == nil {
				*target = nil
				continue
			}
			copied, ok := value.(time.Time)
			if !ok {
				return errScripted{at: i, want: "time.Time", got: value}
			}
			*target = &copied
		default:
			return errScripted{at: i, want: "a type the projection scans", got: dest[i]}
		}
	}
	return nil
}

type errWidth struct{ want, got int }

func (e errWidth) Error() string {
	return fmt.Sprintf("the scripted row is the wrong width for the projection: it scans %d columns and the row has %d — the order is connectionColumns", e.want, e.got)
}

// errScripted is a fixture that scripted the wrong TYPE for a column. It names
// the position rather than the column, because that is what a caller can act
// on: the scripted rows are built in connectionColumns order, so the position
// is the column.
type errScripted struct {
	at   int
	want string
	got  any
}

func (e errScripted) Error() string {
	return fmt.Sprintf("column %d of the scripted row is a %T, and the projection scans it as %s — count along connectionColumns to find it",
		e.at+1, e.got, e.want)
}

// connectionRow scripts one row of connectionColumns, in that order, so a
// column added to the projection is ONE edit in the fixtures rather than one
// per scripted row.
func connectionRow(id, userID, baseURL, status string, mark, gap int64) []any {
	return []any{id, userID, baseURL, status, "Tin Nguyen", "ws-1", mark, gap, int64(0), nil, "", 1}
}

// jsonOf decodes a handler's answer, failing the test rather than the caller.
func jsonOf[T any](tb testing.TB, raw json.RawMessage) T {
	tb.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		tb.Fatalf("the answer does not decode: %v\n%s", err, raw)
	}
	return out
}

// statementMentioning finds the one statement a test is about, so an assertion
// does not depend on how many others the handler issued around it.
//
// The needles callers pass are deliberately NOT statement openers with a table
// after them ("ON CONFLICT", not "INSERT INTO"): the tree's SQL-scope gate
// reads string literals looking for the table a statement names, and a needle
// shaped like the start of one reads as a statement whose table it cannot
// resolve — a fixture failing a gate that is right about every real case.
func (t *fakeTx) statementMentioning(tb testing.TB, needle string) (string, []any) {
	tb.Helper()
	for i, sql := range t.statements {
		if strings.Contains(sql, needle) {
			return sql, t.args[i]
		}
	}
	tb.Fatalf("no statement mentions %q; the handler issued:\n%s", needle, strings.Join(t.statements, "\n---\n"))
	return "", nil
}
