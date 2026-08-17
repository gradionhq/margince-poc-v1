// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The fakes this unit's suite runs against.
//
// A unit's own tests cannot reach the core: an extensions/ module imports only
// the published surface, so there is no pool, no custodian and no capture
// pipeline here. What CAN be tested at this level is what this unit owns — the
// argument decoding, the tier gate, the refusal classes, the renewal's
// serialization, which statements each handler issues, what it hands the ingress
// port, and the cursor arithmetic. Whether those statements are correct SQL
// against a real schema, and whether the port accepts what is handed to it, are
// the migration gate's and the integration lane's questions.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// adminUserID is the member the fake invocations run as, canonical because that
// is what the column it lands in accepts.
const adminUserID = "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31"

// The fixture's account and connection ids, spelled once.
const (
	fixtureOAID         = "4033837145949898046"
	fixtureConnectionID = "3d5f8a10-7c42-4e19-9b03-1f6a2d8c5e74"
)

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
		caller:  extension.Caller{Type: extension.CallerHuman, UserID: adminUserID},
	}
}

// unattended is the Runtime a job tick holds: nobody behind it, which is what the
// core answers for a scheduled run.
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
	// ingested from inside one meets the same refusal the core gives — which is
	// the defect this unit's poll is shaped to avoid, and a fake that allowed it
	// would let that shape rot silently.
	r.tx.open = true
	defer func() { r.tx.open = false }()
	return fn(ctx, r.tx)
}

// Ingest records what the unit handed the core, applies the same record grammar
// the real port applies, and refuses from inside a transaction exactly as it
// does.
//
// The grammar is here rather than waved through because a fake that accepts what
// the core refuses lets a unit's whole suite agree with a bug, and the only thing
// that disagrees is production. `Record.Validate` is published, so this costs one
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

// fakeSecrets is the unit's namespace, in BOTH scopes — this unit uses both, and
// which key lives in which is the custody decision the whole design rests on, so
// a fake that collapsed them would let that decision rot.
type fakeSecrets struct {
	stored map[string][]byte

	putErr        error
	putUserErr    error
	getErr        error
	deleted       []string
	workspaceKeys []string
}

func (s *fakeSecrets) key(scope, user, key string) string { return scope + "/" + user + "/" + key }

func (s *fakeSecrets) Get(_ context.Context, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	secret, ok := s.stored[s.key("workspace", "", key)]
	if !ok {
		return nil, extension.ErrSecretNotFound
	}
	return secret, nil
}

func (s *fakeSecrets) Put(_ context.Context, key string, secret []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.workspaceKeys = append(s.workspaceKeys, key)
	s.stored[s.key("workspace", "", key)] = secret
	return nil
}

func (s *fakeSecrets) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, s.key("workspace", "", key))
	delete(s.stored, s.key("workspace", "", key))
	return nil
}

func (s *fakeSecrets) GetUser(_ context.Context, user extension.UserID, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	secret, ok := s.stored[s.key("user", string(user), key)]
	if !ok {
		return nil, extension.ErrSecretNotFound
	}
	return secret, nil
}

func (s *fakeSecrets) PutUser(_ context.Context, user extension.UserID, key string, secret []byte) error {
	if s.putUserErr != nil {
		return s.putUserErr
	}
	s.stored[s.key("user", string(user), key)] = secret
	return nil
}

func (s *fakeSecrets) DeleteUser(_ context.Context, user extension.UserID, key string) error {
	s.deleted = append(s.deleted, s.key("user", string(user), key))
	delete(s.stored, s.key("user", string(user), key))
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
	// noRows makes the nth QueryRow (1-based) answer the published empty result,
	// which every handler here treats as "there is no such row" rather than as a
	// failure.
	noRows map[int]bool

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
	// not JSON and a verb that is not a verb are all refusals the core makes, and
	// a fake that accepted them would let a handler ship a call the real port
	// rejects.
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
	return &fakeRows{}, nil
}

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
	return fakeRow{values: values, err: t.failure()}
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
		// The PUBLISHED sentinel, not a driver's wording: the core translates the
		// driver's own text into this precisely so a unit does not match on it,
		// and a fake inventing an error the core never returns is a suite testing
		// itself.
		return extension.ErrNoRows
	}
	return scanInto(dest, r.values)
}

type fakeRows struct{ closed bool }

func (r *fakeRows) Next() bool        { return false }
func (r *fakeRows) Scan(...any) error { return nil }
func (r *fakeRows) Close()            { r.closed = true }
func (r *fakeRows) Err() error        { return nil }

// scanInto copies scripted values into a handler's scan destinations. It knows
// only the types this unit's projection actually scans; a new column type is a
// loud failure here rather than a silent zero value.
func scanInto(dest, values []any) error {
	if len(dest) != len(values) {
		return errWidth{want: len(dest), got: len(values)}
	}
	for i, value := range values {
		// Every assertion is CHECKED. An unchecked one answers the handler with a
		// zero value instead of the mistake — a fixture that scripts an int where
		// the projection scans a string reads back "" and the assertion under test
		// passes for a reason the test never states.
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
			// A TIME, because the column is timestamptz and the driver refuses to
			// scan one into a string. The fake takes the same type the handler
			// asks for, so a projection that goes back to text fails here rather
			// than in production.
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
// the position rather than the column, because that is what a caller can act on:
// the scripted rows are built in connectionColumns order, so the position is the
// column.
type errScripted struct {
	at   int
	want string
	got  any
}

func (e errScripted) Error() string {
	return fmt.Sprintf("column %d of the scripted row is a %T, and the projection scans it as %s — count along connectionColumns to find it",
		e.at+1, e.got, e.want)
}

// connectionRow scripts one row of connectionColumns, in that order, so a column
// added to the projection is ONE edit in the fixtures rather than one per
// scripted row.
func connectionRow(status string, expiresAt *time.Time, at cursor) []any {
	row := []any{
		fixtureConnectionID, fixtureOAID, "app-1", "https://crm.example.com/zalo",
		adminUserID, status, "NFQ", "Tăng trưởng", "12/08/2027", nil, nil,
		at.floor, at.gap, at.top, at.offset, nil, "", 1,
	}
	if expiresAt != nil {
		// Dereferenced into the slot rather than through a helper, so the scanner's
		// nil-means-absent contract stays visible where the row is built.
		row[expiryColumn] = *expiresAt
	}
	return row
}

// expiryColumn is where access_token_expires_at sits in connectionColumns, named
// so a column inserted before it is one edit here rather than a silent off-by-one
// that scripts a timestamp into the wrong field.
const expiryColumn = 9

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
// after them ("ON CONFLICT", not "INSERT INTO"): the tree's SQL-scope gate reads
// string literals looking for the table a statement names, and a needle shaped
// like the start of one reads as a statement whose table it cannot resolve — a
// fixture failing a gate that is right about every real case.
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

// ---------------------------------------------------------------- the provider

// zaloFake serves the OpenAPI host's shapes over a loopback listener.
//
// It answers HTTP 200 for EVERYTHING, including its refusals, because that is
// what the real provider does — and it is the one behaviour a fake here must get
// right, since a client that classified on the status line would pass against a
// friendlier fake and read every revoked token as an empty page in production.
type zaloFake struct {
	server *httptest.Server

	// errorCode is the in-band code every endpoint answers with, zero for
	// success.
	errorCode int
	// chatPages is what listrecentchat answers, indexed by offset/10. An offset
	// past the end answers an empty page, which is how the real walk terminates.
	chatPages [][]map[string]any
	// sent is every message the send endpoint accepted.
	sent []map[string]any
	// calls counts each path, so a test can say what was and was not asked.
	calls map[string]int
}

func newZaloFake(t *testing.T) *zaloFake {
	t.Helper()
	fake := &zaloFake{calls: map[string]int{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

// client points this unit's own client at the fake. The production constructor
// is used and only its base is moved, so what the suite drives is the client a
// deployment runs rather than a second one assembled for the test.
func (z *zaloFake) client(token string) *client {
	api := newClient(token)
	api.base = z.server.URL
	return api
}

func (z *zaloFake) dial() clientFactory {
	return func(token string) *client { return z.client(token) }
}

func (z *zaloFake) serve(w http.ResponseWriter, r *http.Request) {
	z.calls[r.URL.Path]++
	if z.errorCode != 0 {
		z.answer(w, map[string]any{"error": z.errorCode, "message": "refused"})
		return
	}
	switch r.URL.Path {
	case "/v2.0/oa/getoa":
		z.answer(w, map[string]any{"error": 0, "message": "Success", "data": map[string]any{
			"oa_id": fixtureOAID, "name": "NFQ",
			"package_name": "Tăng trưởng", "package_valid_through_date": "12/08/2027",
		}})
	case "/v2.0/oa/listrecentchat":
		z.answer(w, map[string]any{"error": 0, "message": "Success", "data": z.pageFor(r)})
	case "/v3.0/oa/message/cs":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			z.answer(w, map[string]any{"error": -201, "message": "Data is not json format"})
			return
		}
		z.sent = append(z.sent, body)
		z.answer(w, map[string]any{"error": 0, "message": "Success", "data": map[string]any{
			"message_id": "sent-1", "sent_time": "1786689951020",
		}})
	default:
		// What the real host answers for a path it does not serve: a 200 whose
		// body says 404.
		z.answer(w, map[string]any{"error": 404, "message": "You are accessing an empty or invalid API"})
	}
}

// pageFor reads the offset out of the `data=` JSON query parameter, which is the
// grammar this provider uses instead of ordinary query arguments.
func (z *zaloFake) pageFor(r *http.Request) []map[string]any {
	var params struct {
		Offset int `json:"offset"`
		Count  int `json:"count"`
	}
	if err := json.Unmarshal([]byte(r.URL.Query().Get("data")), &params); err != nil {
		return nil
	}
	page := params.Offset / maxChatPage
	if page < 0 || page >= len(z.chatPages) {
		return []map[string]any{}
	}
	return z.chatPages[page]
}

func (z *zaloFake) answer(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	// ALWAYS 200. See the type's own comment: this is the provider's behaviour
	// and the reason this unit classifies on the body.
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic("zalo fake could not write its answer: " + err.Error())
	}
}

// message builds one scripted message row.
func message(id string, at int64, src int, text string) map[string]any {
	from, to := "user-1", fixtureOAID
	if src == srcOAToUser {
		from, to = fixtureOAID, "user-1"
	}
	return map[string]any{
		"src": src, "time": at, "message_id": id, "type": "text", "message": text,
		"from_id": from, "from_display_name": "Nguyễn An",
		"to_id": to, "to_display_name": "NFQ",
	}
}

// ------------------------------------------------------------- the token endpoint

// fakeGrants is the token endpoint: what it hands back, and how many times it
// was asked.
//
// The rotation counter is the point of it. A Zalo refresh token is single-use, so
// "how many times did this tick reach the token endpoint" is not a detail — it is
// the property the whole renewal design exists to keep at one.
type fakeGrants struct {
	redeemed  tokenPair
	redeemErr error

	rotated     tokenPair
	rotateErr   error
	rotations   int
	redemptions int

	spent []string
}

func (g *fakeGrants) Redeem(context.Context, string, string, string, string) (tokenPair, error) {
	g.redemptions++
	if g.redeemErr != nil {
		return tokenPair{}, g.redeemErr
	}
	return g.redeemed, nil
}

func (g *fakeGrants) Rotate(_ context.Context, _, _, refreshToken string) (tokenPair, error) {
	g.rotations++
	g.spent = append(g.spent, refreshToken)
	if g.rotateErr != nil {
		return tokenPair{}, g.rotateErr
	}
	return g.rotated, nil
}

// at is a fixed instant every clock-dependent test reads from, so nothing here
// depends on when the suite runs.
func at(offset time.Duration) time.Time {
	return time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC).Add(offset)
}

// frozen is the clock a test hands a poll or a send.
func frozen(now time.Time) clock { return func() time.Time { return now } }
