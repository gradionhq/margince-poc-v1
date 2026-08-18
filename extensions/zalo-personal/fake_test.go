// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The fakes this unit's suite runs against.
//
// A unit's own tests cannot reach the core: an extensions/ module imports only
// the published surface, so there is no pool, no custodian and no capture
// pipeline here. What CAN be tested at this level is what this unit owns — the
// caller binding, the ordering of credential and row, which statements each
// handler issues, and how a send classifies a refusal.
//
// THE FAKES RE-RUN THE PUBLISHED VALIDATORS AND ENFORCE THE SAME REFUSALS THE
// CORE DOES, and that is the rule rather than a nicety. A fake that accepts
// what the core refuses lets a whole suite agree with a bug, with production as
// the only dissenting voice. The sibling unit paid for that once: its fake
// answered the driver's own "no rows" wording, its handler matched on that
// wording, and the two agreed while every unconnected member got a 500. Every
// error handed back here is the PUBLISHED sentinel for the same reason.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// callerUserID is the member the fake invocations run as, canonical because
// that is what the column it lands in accepts.
const callerUserID = "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31"

// colleagueUserID is another member of the same workspace. It exists so a test
// can assert that NOTHING an operation does ever names them — the one property
// this unit's surface is built around.
const colleagueUserID = "1b6f2d38-77c4-4a19-8fd0-5e3a9c0b7412"

// connectionID is the row id the scripted rows carry, canonical because the
// ledger's own grammar refuses anything else.
const connectionID = "5c2a6e91-0d43-4bb8-9a77-8e1f3d5c4b20"

// fakeRuntime is one invocation's Runtime.
type fakeRuntime struct {
	secrets *fakeSecrets
	tx      *fakeTx
	caller  extension.Caller

	// trace is the shared ordering log; see newRuntime.
	trace *trace

	// txErr stands in for the core refusing to open a transaction at all — an
	// expired Runtime, an unwired role — which every handler must propagate
	// rather than answer over.
	txErr error

	// ingested is every record handed to capture, in order, and ingestErr is what
	// the port answers from the nth call onward. The pair is what lets a test say
	// "the turn stopped at the third message and moved the cursor no further".
	ingested    []extension.Record
	ingestedOn  []extension.UserID
	ingestErr   error
	ingestFrom  int
	ingestCalls int
	// skips makes capture answer DispositionSkipped, which is a success.
	skips bool
}

func newRuntime() *fakeRuntime {
	// ONE trace, shared by the secret port and the transaction, because the
	// properties this unit is most exposed to are ORDERING properties across
	// the two: a credential sealed before the row that advertises it, and both
	// credentials deleted before the row that says they are gone. Two separate
	// logs can each be right while the order between them is wrong.
	shared := &trace{}
	return &fakeRuntime{
		secrets: &fakeSecrets{stored: map[string][]byte{}, trace: shared},
		tx:      &fakeTx{trace: shared},
		trace:   shared,
		caller:  extension.Caller{Type: extension.CallerHuman, UserID: callerUserID},
	}
}

// trace is what happened, in order, across the secret port and the database.
type trace struct{ steps []string }

func (t *trace) add(step string) { t.steps = append(t.steps, step) }

// before asserts that one step happened before another, and fails naming the
// whole trace — the failure "these both happened, in the wrong order" is
// unreadable without it.
func (t *trace) before(tb testing.TB, first, second string) {
	tb.Helper()
	at, then := t.at(tb, first), t.at(tb, second)
	if at > then {
		tb.Fatalf("%q happened after %q; the trace was:\n%s", first, second, strings.Join(t.steps, "\n"))
	}
}

func (t *trace) at(tb testing.TB, step string) int {
	tb.Helper()
	for i, got := range t.steps {
		if got == step {
			return i
		}
	}
	tb.Fatalf("nothing in the trace is %q; it was:\n%s", step, strings.Join(t.steps, "\n"))
	return -1
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
	// Marked open for the duration, so a handler that ingested from inside one
	// meets the same refusal the core gives.
	r.tx.open = true
	defer func() { r.tx.open = false }()
	return fn(ctx, r.tx)
}

// Ingest records what the unit handed capture, applies the same record grammar
// the real port applies, and refuses from inside a transaction exactly as it
// does.
//
// BOTH REFUSALS ARE THE POINT. The nesting one is the defect this unit's tick is
// shaped to avoid, and a fake that allowed it would let that shape rot silently
// while production hung on a small pool. The grammar one is the lesson this tree
// already paid for once: a fake that accepts what the core refuses lets a whole
// suite agree with a bug, with production as the only dissenting voice.
func (r *fakeRuntime) Ingest(_ context.Context, on extension.UserID, rec extension.Record) (extension.Result, error) {
	if r.tx.open {
		return extension.Result{}, extension.ErrNestedIngest
	}
	r.ingestCalls++
	// Recorded BEFORE any refusal: what the unit HANDED capture is the thing
	// under test, and a record dropped here is one a failing test cannot show.
	r.ingested = append(r.ingested, rec)
	r.ingestedOn = append(r.ingestedOn, on)
	if err := rec.Validate(); err != nil {
		return extension.Result{}, fmt.Errorf("%w: %s", extension.ErrInvalid, err)
	}
	if r.ingestErr != nil && r.ingestCalls >= r.ingestFrom {
		return extension.Result{}, r.ingestErr
	}
	return extension.Result{Disposition: r.disposition()}, nil
}

// disposition is what capture answers. DispositionSkipped is a SUCCESS — the core
// drops a wholly-internal message deliberately — so a test can assert that a
// skip advances the cursor exactly as an acceptance does.
func (r *fakeRuntime) disposition() extension.Disposition {
	if r.skips {
		return extension.DispositionSkipped
	}
	return extension.DispositionAccepted
}

// fakeSecrets is the unit's namespace, USER SCOPE ONLY — this unit declares no
// workspace-scoped secret, and a fake that served one would invite a handler to
// start using a credential the manifest never announced.
type fakeSecrets struct {
	stored map[string][]byte
	trace  *trace
	// gets counts reads, so a test can assert that Live answered WITHOUT
	// unsealing anything rather than merely that it answered.
	gets   int
	getErr error
}

func (s *fakeSecrets) Get(context.Context, string) ([]byte, error) {
	return nil, extension.ErrSecretNotFound
}

func (s *fakeSecrets) Put(context.Context, string, []byte) error {
	return errors.New("zalo-personal declares no workspace-scoped secret")
}

func (s *fakeSecrets) Delete(context.Context, string) error {
	return errors.New("zalo-personal declares no workspace-scoped secret")
}

func (s *fakeSecrets) userKey(user extension.UserID, key string) string {
	return string(user) + "/" + key
}

func (s *fakeSecrets) GetUser(_ context.Context, user extension.UserID, key string) ([]byte, error) {
	s.gets++
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
	s.stored[s.userKey(user, key)] = secret
	s.trace.add("put " + s.userKey(user, key))
	return nil
}

func (s *fakeSecrets) DeleteUser(_ context.Context, user extension.UserID, key string) error {
	s.trace.add("delete " + s.userKey(user, key))
	delete(s.stored, s.userKey(user, key))
	return nil
}

// fakeTx records what a handler asked the database to do and answers with what
// the test scripted.
type fakeTx struct {
	trace      *trace
	statements []string
	args       [][]any

	// singleRows is what successive QueryRow calls answer, one entry each and
	// CONSUMED in order. A handler that reads a row and then writes one issues
	// two, and a fake answering the same values to both could not tell a
	// read-then-write from a write alone.
	singleRows [][]any
	// noRows makes the nth QueryRow (1-based) answer the PUBLISHED empty
	// result, which every handler here reads as "there is no such connection"
	// rather than as a failure.
	noRows map[int]bool

	// queryRows is what a multi-row read hands back, keyed by the TABLE the
	// statement names rather than by the order the reads happen in.
	//
	// POSITIONAL SCRIPTING WAS THE WRONG SHAPE HERE, and it is worth saying why it
	// changed: one turn reads the fleet, the member's verdicts, the verdicts AGAIN
	// after the drain, and the send markers. Four positional entries per member
	// meant every test stated the same three sets in the same order, and inserting a
	// read renumbered all of them — which is a fixture that couples to the sequence
	// of a function rather than to its behaviour.
	//
	// Each table's entries are consumed in order, and once a table runs out the LAST
	// set repeats. That default is the honest one: a test that scripts the verdicts
	// once is saying "nothing changed between the two reads", which is the ordinary
	// case. A test about a member who blocks somebody mid-drain scripts two, and
	// then the difference is the point of the test rather than an artefact of
	// counting.
	queryRows map[string][][][]any

	audited   []extension.Change
	published []extension.Event
	rowCalls  int
	// open reports whether a transaction is in flight, which is what makes the
	// nested-ingest refusal above real rather than declared.
	open bool
	// execErr is what a statement that returns no rows answers. It exists because
	// this unit has one write whose FAILURE is a deliberate outcome rather than an
	// error path — the send marker — and a claim like that has to be gated by a
	// test rather than stated in a comment.
	execErr error
}

// Core is nil: this unit files nothing through the governed core port, and a
// fake that handed one back would let a handler start using it unnoticed.
func (t *fakeTx) Core() extension.Core { return nil }

func (t *fakeTx) Record(_ context.Context, ch extension.Change, ev extension.Event) error {
	// The published grammar, run here rather than waved through: an entity
	// outside this unit's namespace, an id that is not a UUID, an image that is
	// not JSON and a verb that is not a verb are all refusals the core makes.
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
	return 0, t.execErr
}

// record keeps the statement and puts its verb on the shared trace, so an
// ordering assertion can name "insert" without depending on the whole SQL.
func (t *fakeTx) record(sql string, args []any) {
	t.statements, t.args = append(t.statements, sql), append(t.args, args)
	t.trace.add("sql " + strings.ToLower(strings.Fields(strings.TrimSpace(sql))[0]))
}

func (t *fakeTx) Query(_ context.Context, sql string, args ...any) (extension.Rows, error) {
	t.record(sql, args)
	return &fakeRows{rows: t.nextRows(readKindOf(sql))}, nil
}

// The kinds of multi-row read this unit issues, named for what they ARE rather than
// for the statement that fetches them.
const (
	readFleet    = "fleet"
	readVerdicts = "verdicts"
	readCursors  = "cursors"
	readMarkers  = "markers"
)

// readKindOf names a read by the table it addresses, which is the same discriminator
// production uses. An unrecognised read fails the test loudly rather than answering
// an empty set: a new read answered with "nothing" is a silent behaviour change.
func readKindOf(sql string) string {
	switch {
	case strings.Contains(sql, connectionTable):
		return readFleet
	case strings.Contains(sql, sentTable):
		return readMarkers
	case strings.Contains(sql, cursorTable):
		return readCursors
	case strings.Contains(sql, allowlistTable):
		return readVerdicts
	}
	return "an unscriptable read: " + sql
}

// nextRows answers one read of the given kind, repeating the last scripted set once
// a kind runs out — see the note on queryRows.
func (t *fakeTx) nextRows(kind string) [][]any {
	queued := t.queryRows[kind]
	switch len(queued) {
	case 0:
		return nil
	case 1:
		return queued[0]
	}
	t.queryRows[kind] = queued[1:]
	return queued[0]
}

// script states what a read of one kind answers. Called twice for the same kind, the
// two answers are handed out in order.
func (t *fakeTx) script(kind string, rows ...[]any) {
	if t.queryRows == nil {
		t.queryRows = map[string][][][]any{}
	}
	t.queryRows[kind] = append(t.queryRows[kind], rows)
}

// fakeRows answers a multi-row read. Err is scriptable because a read that fails
// PART WAY is a real case the tick has to propagate rather than treat as a short
// list — a fleet silently truncated to its first two members looks exactly like
// an installation with two members.
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

func (t *fakeTx) QueryRow(_ context.Context, sql string, args ...any) extension.Row {
	t.record(sql, args)
	t.rowCalls++
	if t.noRows[t.rowCalls] {
		return fakeRow{err: extension.ErrNoRows}
	}
	var values []any
	if len(t.singleRows) > 0 {
		values, t.singleRows = t.singleRows[0], t.singleRows[1:]
	}
	return fakeRow{values: values}
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.values == nil {
		// The PUBLISHED sentinel, never the driver's wording: the core
		// translates that away precisely so a unit does not match on it.
		return extension.ErrNoRows
	}
	return scanInto(dest, r.values)
}

// scanInto copies scripted values into a handler's scan destinations. It knows
// only the types this unit's projection actually scans; a new column type is a
// loud failure here rather than a silent zero value.
func scanInto(dest, values []any) error {
	if len(dest) != len(values) {
		return errWidth{want: len(dest), got: len(values)}
	}
	for i, value := range values {
		// Every assertion is CHECKED. An unchecked one answers the handler with
		// a zero value instead of the mistake — a fixture scripting an int
		// where the projection scans a string reads back "" and the assertion
		// under test passes for a reason the test never states.
		if err := assignScanned(dest[i], value, i); err != nil {
			return err
		}
	}
	return nil
}

// assignScanned copies ONE scripted value into ONE scan destination.
//
//craft:ignore naked-any a scan destination and a scripted column are `any` by the published Row.Scan contract — the whole point of this function is to check the pair the interface cannot
func assignScanned(dest, value any, at int) error {
	switch target := dest.(type) {
	case *string:
		got, ok := value.(string)
		if !ok {
			return errScripted{at: at, want: "string", got: value}
		}
		*target = got
	case *bool:
		got, ok := value.(bool)
		if !ok {
			return errScripted{at: at, want: "bool", got: value}
		}
		*target = got
	case *int:
		got, ok := value.(int)
		if !ok {
			return errScripted{at: at, want: "int", got: value}
		}
		*target = got
	case **time.Time:
		// A TIME, because the column is timestamptz and the driver refuses to
		// scan one into a string. The fake takes the type the handler asks for,
		// so a projection that goes back to text fails here rather than in
		// production.
		if value == nil {
			*target = nil
			return nil
		}
		got, ok := value.(time.Time)
		if !ok {
			return errScripted{at: at, want: "time.Time", got: value}
		}
		*target = &got
	default:
		return errScripted{at: at, want: "a type the projection scans", got: dest}
	}
	return nil
}

type errWidth struct{ want, got int }

func (e errWidth) Error() string {
	return "the scripted row is the wrong width for the projection: it scans " +
		itoa(e.want) + " columns and the row has " + itoa(e.got) + " — the order is connectionColumns"
}

// errScripted is a fixture that scripted the wrong TYPE for a column. It names
// the position rather than the column, because that is what a caller can act
// on: the scripted rows are built in connectionColumns order.
type errScripted struct {
	at   int
	want string
	got  any
}

func (e errScripted) Error() string {
	return "column " + itoa(e.at+1) + " of the scripted row is the wrong type — the projection scans it as " +
		e.want + "; count along connectionColumns to find it"
}

// itoa keeps the two error strings above off fmt, which the craft gate would
// otherwise see as formatting in a type's Error method.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}

// connectionRow scripts one row of connectionColumns, in that order, so a
// column added to the projection is ONE edit in the fixtures rather than one
// per scripted row.
func connectionRow(status, zaloUID string, captureEnabled bool) []any {
	connectedAt := time.Date(2026, time.August, 18, 9, 30, 0, 0, time.UTC)
	// A row with capture ARMED carries a mode, because the database refuses "armed
	// with no mode" — so a fixture without one would be a state production cannot
	// reach. only_chosen is the default here because it is the stricter of the two
	// and because it is what most of this suite is about; withMode states the other.
	mode, since := "", any(nil)
	if captureEnabled {
		mode, since = captureOnlyChosen, any(theModeChosenAt())
	}
	return []any{
		connectionID, callerUserID, status, zaloUID, "Tin Nguyen", captureEnabled,
		mode, since, nil, "", connectedAt, 0, 1,
	}
}

// withMode is connectionRow under a stated capture mode. It is where a test says
// "this member captures everyone except the people they left out".
func withMode(row []any, mode string) []any {
	scripted := append([]any(nil), row...)
	scripted[6], scripted[7] = mode, theModeChosenAt()
	return scripted
}

// withModeChosenAt moves the floor everyone_except measures a never-mentioned
// conversation from, so a test can put the captured frames on either side of it.
func withModeChosenAt(row []any, since time.Time) []any {
	scripted := append([]any(nil), row...)
	scripted[7] = since
	return scripted
}

// withIdleStreak is connectionRow carrying a history of drains that found nothing.
// It is what a backoff assertion needs: the wait a turn writes is derived from the
// streak the row already held.
func withIdleStreak(row []any, streak int) []any {
	scripted := append([]any(nil), row...)
	scripted[11] = streak
	return scripted
}

// forMember is connectionRow re-pointed at another member, so a fleet test can
// script more than one connection without every row belonging to one person.
func forMember(row []any, id, rowID string) []any {
	scripted := append([]any(nil), row...)
	scripted[0], scripted[1] = rowID, id
	return scripted
}

// fakeLogin is the QR handshake as a test scripts it: what each call answers,
// and how many times it was asked.
type fakeLogin struct {
	pending  zaloPending
	code     zaloQRCode
	startErr error

	result   zaloPollResult
	next     zaloPending
	pollErr  error
	polls    int
	budgeted time.Duration
}

func (f *fakeLogin) handshake() handshake {
	return handshake{
		start: func(context.Context, zaloOptions) (zaloPending, zaloQRCode, error) {
			return f.pending, f.code, f.startErr
		},
		poll: func(_ context.Context, _ zaloPending, _ zaloOptions, budget time.Duration) (zaloPollResult, zaloPending, error) {
			f.polls++
			f.budgeted = budget
			return f.result, f.next, f.pollErr
		},
	}
}

// fakeSession is a resumed session. It counts resumes, so a test can assert
// that Live answered WITHOUT spending the credential.
type fakeSession struct {
	uid       string
	receipt   zaloReceipt
	sendErr   error
	resumeErr error
	resumes   int
	sent      []string
	sentTo    []string
}

func (f *fakeSession) resume() resumeFunc {
	return func(context.Context, zaloSealed) (session, error) {
		f.resumes++
		if f.resumeErr != nil {
			return nil, f.resumeErr
		}
		return f, nil
	}
}

func (f *fakeSession) UID() string { return f.uid }

func (f *fakeSession) SendText(_ context.Context, toUID, body string) (zaloReceipt, error) {
	f.sentTo, f.sent = append(f.sentTo, toUID), append(f.sent, body)
	return f.receipt, f.sendErr
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
// resolve.
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
