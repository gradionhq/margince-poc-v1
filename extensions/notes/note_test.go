// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// stamp is one fixed instant, so a formatted timestamp can be asserted
// literally rather than reformatted by the test that is checking the format.
var stamp = time.Date(2026, 8, 9, 9, 14, 0, 0, time.UTC)

func TestListNotesReturnsTheRowsNewestFirst(t *testing.T) {
	rt := newRuntime()
	rt.tx.rows = [][]any{
		noteRow("11111111-1111-4111-8111-111111111111", kindNote, "hello from the demo extension", callerUserID, false, stamp),
		noteRow("22222222-2222-4222-8222-222222222222", kindNote, "an older note", callerUserID, false, stamp.Add(-time.Hour)),
	}

	out, err := listNotes(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Notes []note `json:"notes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notes) != 2 {
		t.Fatalf("notes = %+v, want both rows", got.Notes)
	}
	if got.Notes[0].Body != "hello from the demo extension" {
		t.Errorf("the first note is %q — the read must preserve the ORDER BY, not re-sort", got.Notes[0].Body)
	}
	if got.Notes[0].CreatedAt != "2026-08-09T09:14:00Z" {
		t.Errorf("created_at = %q, want the RFC 3339 spelling the contract declares", got.Notes[0].CreatedAt)
	}

	// The statement, not just the result: the table is schema-qualified because
	// ext is on no search_path the app connects with, and the read is bounded
	// because the screen renders the whole answer.
	sql := rt.tx.only(t)
	if !strings.Contains(sql, noteTable) {
		t.Errorf("the read does not name %s:\n%s", noteTable, sql)
	}
	if !strings.Contains(sql, "LIMIT $1") || rt.tx.args[0][0] != maxNotesPerRead {
		t.Errorf("the read is unbounded, or bounded by something other than maxNotesPerRead:\n%s", sql)
	}
	if !rt.tx.rows0Closed() {
		t.Error("the cursor was not closed — it is released with the transaction either way, but holding one open pins the connection until then")
	}
}

// rows0Closed reports whether the cursor the handler opened was closed. It
// lives here rather than on fakeTx because the fake hands the Rows to the
// handler and keeps no reference; the handler's own defer is the thing under
// test.
func (t *fakeTx) rows0Closed() bool { return t.lastRows == nil || t.lastRows.closed }

func TestListNotesAnswersAnEmptyArrayRatherThanNull(t *testing.T) {
	// A nil slice marshals to null, and the contract declares `notes` required
	// and an array — every client would then need a special case for the
	// emptiest possible state, which is also the first one anybody sees.
	out, err := listNotes(context.Background(), newRuntime(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `{"notes":[]}` {
		t.Errorf("an empty notepad answered %s", got)
	}
}

// TestListNotesHoldsItsDeclaredEmptyObject: the contract declares the list's
// arguments as an empty object with additionalProperties: false, and nothing on
// this seam validates a body against the schema. Ignoring the document would
// make the list the one operation of the three that accepts whatever it is
// sent, while its published schema says the opposite.
func TestListNotesHoldsItsDeclaredEmptyObject(t *testing.T) {
	for _, in := range []string{`{"limit":10}`, `{"Notes":1}`, `null`, `{} {}`} {
		rt := newRuntime()
		_, err := listNotes(context.Background(), rt, json.RawMessage(in))
		if err == nil {
			t.Errorf("%s: the list accepted arguments its schema declares it has none of", in)
		}
		if len(rt.tx.statements) != 0 {
			t.Errorf("%s: the refusal still reached the database: %v", in, rt.tx.statements)
		}
	}
	// And the shape a caller legitimately sends still works. The contract
	// declares the requestBody required, so `{}` is what a client sends and
	// what the generated caller sends; an absent document is the same refusal
	// the other two operations give it.
	if _, err := listNotes(context.Background(), newRuntime(), json.RawMessage(`{}`)); err != nil {
		t.Errorf("a list with no arguments must read: %v", err)
	}
}

func TestListNotesPropagatesTheReadFailure(t *testing.T) {
	rt := newRuntime()
	rt.tx.err = errors.New("connection reset")
	if _, err := listNotes(context.Background(), rt, json.RawMessage(`{}`)); err == nil {
		t.Fatal("a failed read answered a notepad")
	}
}

func TestAddNoteStoresTheBodyAndReturnsTheStoredRow(t *testing.T) {
	rt := newRuntime()
	rt.tx.row = noteRow("11111111-1111-4111-8111-111111111111", kindNote, "  hello  ", callerUserID, false, stamp)

	out, err := addNote(context.Background(), rt, json.RawMessage(`{"body":"  hello  "}`))
	if err != nil {
		t.Fatal(err)
	}
	var got note
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.CreatedAt != "2026-08-09T09:14:00Z" {
		t.Errorf("the result does not carry the row the database wrote: %+v", got)
	}
	// The body reaches SQL trimmed: leading and trailing whitespace is not
	// content, and a note of spaces would render as an empty row nobody can
	// find again.
	if rt.tx.args[0][1] != "hello" {
		t.Errorf("the insert argument is %q, want the trimmed body", rt.tx.args[0][1])
	}
	// And it writes the NOTE kind: a row the heartbeat's prune must never
	// select. The column is what separates the two, so the notes path stating
	// its own kind is the other half of that guarantee.
	if rt.tx.args[0][0] != string(kindNote) {
		t.Errorf("the insert writes kind %v, want %q", rt.tx.args[0][0], kindNote)
	}
	sql := rt.tx.only(t)
	if !strings.Contains(sql, callerWorkspace) {
		t.Errorf("the insert does not name the invocation's workspace, so the policy's WITH CHECK would refuse it:\n%s", sql)
	}
	if !strings.Contains(sql, "RETURNING") {
		t.Errorf("the insert reads its own row back in a second statement:\n%s", sql)
	}
}

// TestAddNoteStampsTheAuthorFromTheCallerAndNotTheBody is the security
// assertion of this file, not a mapping check.
//
// An author a client can supply is an author every client can forge, and a
// forged one is worse than an absent one: it is a signature on somebody else's
// note. So the insert must carry the invocation's caller, and the request must
// have no way to say otherwise. This unit is the template other units are
// copied from, which is why the rule is pinned here rather than assumed.
func TestAddNoteStampsTheAuthorFromTheCallerAndNotTheBody(t *testing.T) {
	rt := newRuntime()
	rt.caller = extension.Caller{Type: extension.CallerAgent, UserID: callerUserID, IsAgent: true}
	rt.tx.row = noteRow("11111111-1111-4111-8111-111111111111", kindNote, "hello", callerUserID, true, stamp)

	out, err := addNote(context.Background(), rt, json.RawMessage(`{"body":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	userID, ok := rt.tx.args[0][2].(*string)
	if !ok || userID == nil || *userID != callerUserID {
		t.Fatalf("the insert's author is %v, want the caller's user id", rt.tx.args[0][2])
	}
	isAgent, ok := rt.tx.args[0][3].(*bool)
	if !ok || isAgent == nil || !*isAgent {
		t.Fatalf("the insert's is_agent is %v — an agent's note must say an agent wrote it", rt.tx.args[0][3])
	}
	// For an agent the id is the HUMAN's, not a synthetic id for the agent:
	// attribution names the person accountable for the row, and `is_agent`
	// beside it says how the row arrived.
	var got note
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Author == nil || got.Author.UserID != callerUserID || !got.Author.IsAgent {
		t.Errorf("the response's author is %+v, want the caller's", got.Author)
	}

	// And there is NO WAY to send one. The request schema declares `body` and
	// nothing else, and decode matches declared names byte for byte — so every
	// spelling of an author field is a refusal that never reaches the database.
	for _, forged := range []string{
		`{"body":"x","author":{"user_id":"22222222-2222-4222-8222-222222222222","is_agent":false}}`,
		`{"body":"x","author_user_id":"22222222-2222-4222-8222-222222222222"}`,
		`{"body":"x","user_id":"22222222-2222-4222-8222-222222222222"}`,
	} {
		forger := newRuntime()
		if _, err := addNote(context.Background(), forger, json.RawMessage(forged)); err == nil {
			t.Errorf("%s: the add accepted an author from the request body", forged)
		}
		if len(forger.tx.statements) != 0 {
			t.Errorf("%s: the forged author reached the database: %v", forged, forger.tx.statements)
		}
	}
}

// TestAddNoteWritesNoHalfAuthor: the two author columns are one fact split
// across two nullable columns, and the table's CHECK admits both or neither. A
// caller with no user id — the job path's zero Caller, which no served
// operation produces but which the type permits — must therefore write NEITHER,
// not `is_agent` beside a null id. Getting this wrong is a constraint violation
// at the INSERT, so it is worth an assertion rather than a comment.
func TestAddNoteWritesNoHalfAuthor(t *testing.T) {
	rt := newRuntime()
	rt.caller = extension.Caller{} // the zero Caller: CallerSystem, no user
	rt.tx.row = noteRow("11111111-1111-4111-8111-111111111111", kindNote, "hello", "", false, stamp)

	out, err := addNote(context.Background(), rt, json.RawMessage(`{"body":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	userID, isAgent := rt.tx.args[0][2].(*string), rt.tx.args[0][3].(*bool)
	if userID != nil || isAgent != nil {
		t.Errorf("the insert writes a half-author (%v, %v), which the CHECK refuses", userID, isAgent)
	}
	// Omitted from the JSON, not rendered as an author with an empty id: a
	// client cannot tell the second from a stamping bug.
	if strings.Contains(string(out), "author") {
		t.Errorf("a note with no author still carries the member: %s", out)
	}
}

// TestNotesCarryTheirKind pins the enum in both directions: what the reads
// project, and what a value outside the declared pair does.
func TestNotesCarryTheirKind(t *testing.T) {
	rt := newRuntime()
	rt.tx.rows = [][]any{
		noteRow("11111111-1111-4111-8111-111111111111", kindNote, "a note", callerUserID, false, stamp),
		// The tick's row, which the list renders alongside a human's: it is
		// meant to be SEEN there, and `kind` is what tells a reader which is
		// which without parsing the body's leading glyph.
		noteRow("22222222-2222-4222-8222-222222222222", kindHeartbeat, "⟳ heartbeat — workspace …", "", false, stamp),
	}
	out, err := listNotes(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Notes []note `json:"notes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Notes[0].Kind != kindNote || got.Notes[1].Kind != kindHeartbeat {
		t.Fatalf("kinds = %q, %q — the read does not project the column", got.Notes[0].Kind, got.Notes[1].Kind)
	}
	// The tick's row has no author, and the response says so by ABSENCE.
	if got.Notes[1].Author != nil {
		t.Errorf("the heartbeat row carries an author %+v — a scheduled tick has no person behind it", got.Notes[1].Author)
	}
	if got.Notes[0].Author == nil || got.Notes[0].Author.UserID != callerUserID {
		t.Errorf("the human's row lost its author: %+v", got.Notes[0].Author)
	}
}

// TestListNotesRefusesAKindItCannotName: a kind outside the declared pair means
// a newer migration added one and THIS binary is reading its rows. Answering it
// through would put a value the contract's enum does not list into every
// generated client, which fails as a parse error three systems from the cause.
func TestListNotesRefusesAKindItCannotName(t *testing.T) {
	rt := newRuntime()
	rt.tx.rows = [][]any{
		noteRow("11111111-1111-4111-8111-111111111111", noteKind("reminder"), "from a newer schema", callerUserID, false, stamp),
	}
	_, err := listNotes(context.Background(), rt, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "older than the schema") {
		t.Fatalf("err = %v, want the unknown-kind refusal", err)
	}
}

func TestAddNoteRefusesWhatItCannotStoreHonestly(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"an empty body", `{"body":""}`, "needs a body"},
		{"a body of whitespace", `{"body":"   "}`, "needs a body"},
		{"an over-long body", `{"body":"` + strings.Repeat("x", maxNoteBody+1) + `"}`, "at most 500 characters"},
		// additionalProperties: false is declared in the contract and enforced
		// here: nothing between a client and this function checks a body
		// against the published schema, so a caller sending `bdy` would
		// otherwise write an empty note and be told it worked.
		{"an unknown field", `{"bdy":"typo"}`, "not the declared shape"},
		{"a malformed document", `{`, "not the declared shape"},
		// encoding/json matches field names case-insensitively, so
		// DisallowUnknownFields alone admits three spellings of one key — and
		// between two case-variants in one document the LAST one wins, which
		// is a way to change what a mutation writes past a reviewer reading
		// the first. A closed schema has to be closed byte for byte.
		{"a case-variant of a declared field", `{"Body":"typo"}`, "matched byte for byte"},
		{"a declared field and a case-variant of it", `{"body":"first","BODY":"second"}`, "matched byte for byte"},
		// A map collapses these two into one entry, so a check written over an
		// unmarshalled map sees nothing while encoding/json keeps the LAST —
		// which is a way to put a value past a reviewer reading the first.
		{"the same field twice", `{"body":"first","body":"second"}`, "appears twice"},
		// `null` unmarshals into a struct as a no-op, so an operation whose
		// schema requires an object would act on the zero value.
		{"a null document", `null`, "a JSON object is required"},
		// encoding/json decodes ONE value and stops.
		{"a second JSON value", `{"body":"x"} {"body":"y"}`, "second JSON value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime()
			_, err := addNote(context.Background(), rt, json.RawMessage(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if len(rt.tx.statements) != 0 {
				t.Errorf("the refusal still reached the database: %v", rt.tx.statements)
			}
		})
	}
}

func TestAddNotePropagatesTheWriteFailure(t *testing.T) {
	rt := newRuntime()
	rt.tx.err = errors.New("deadlock detected")
	if _, err := addNote(context.Background(), rt, json.RawMessage(`{"body":"x"}`)); err == nil {
		t.Fatal("a failed insert answered a stored note")
	}
}

func TestRemoveNoteReportsWhetherItRemovedAnything(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		want     string
	}{
		{"a note this workspace holds", 1, `{"removed":true}`},
		// Not an error: the policy makes another tenant's row invisible rather
		// than forbidden, so "no such note here" and "no such note anywhere"
		// are the same answer — and the only one this unit is entitled to give.
		{"an id this workspace does not hold", 0, `{"removed":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime()
			rt.tx.affected = tc.affected
			out, err := removeNote(context.Background(), rt,
				json.RawMessage(`{"id":"11111111-1111-4111-8111-111111111111"}`))
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("result = %s, want %s", out, tc.want)
			}
			if sql := rt.tx.only(t); !strings.Contains(sql, "$1::uuid") {
				t.Errorf("the delete does not cast the id, so an unparseable one would match nothing silently:\n%s", sql)
			}
		})
	}
}

func TestRemoveNoteRefusesAnEmptyID(t *testing.T) {
	rt := newRuntime()
	_, err := removeNote(context.Background(), rt, json.RawMessage(`{"id":"  "}`))
	if err == nil || !strings.Contains(err.Error(), "needs its id") {
		t.Fatalf("err = %v, want the missing-id refusal", err)
	}
	if len(rt.tx.statements) != 0 {
		t.Errorf("the refusal still reached the database: %v", rt.tx.statements)
	}
}

func TestRemoveNoteRejectsAnUnknownField(t *testing.T) {
	_, err := removeNote(context.Background(), newRuntime(), json.RawMessage(`{"note_id":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "not the declared shape") {
		t.Fatalf("err = %v, want the strict-decode refusal", err)
	}
}

// TestRemoveNoteRefusesAnIDTheContractCouldNotHaveMeant: the contract declares
// a UUID, and a value that is not one reaches PostgreSQL's ::uuid cast — a
// 22P02 the unit cannot express as a refusal class (issue #657), so the route
// answers 500 to input its own schema called valid. Checked before the
// transaction instead, so nothing reaches the database.
func TestRemoveNoteRefusesAnIDTheContractCouldNotHaveMeant(t *testing.T) {
	for _, id := range []string{
		"not-a-uuid",
		"11111111111141118111111111111111",              // unhyphenated
		"{11111111-1111-4111-8111-111111111111}",        // braced
		"11111111-1111-4111-8111-11111111111",           // one digit short
		"11111111-1111-4111-8111-11111111111g",          // not hex
		"urn:uuid:11111111-1111-4111-8111-111111111111", // prefixed
	} {
		rt := newRuntime()
		_, err := removeNote(context.Background(), rt, json.RawMessage(`{"id":"`+id+`"}`))
		if err == nil || !strings.Contains(err.Error(), "is not a note id") {
			t.Errorf("id %q: err = %v, want the shape refusal", id, err)
		}
		if len(rt.tx.statements) != 0 {
			t.Errorf("id %q: the refusal still reached the database: %v", id, rt.tx.statements)
		}
	}
}

func TestRemoveNotePropagatesTheDeleteFailure(t *testing.T) {
	rt := newRuntime()
	rt.tx.err = errors.New("deadlock detected")
	_, err := removeNote(context.Background(), rt, json.RawMessage(`{"id":"11111111-1111-4111-8111-111111111111"}`))
	if err == nil {
		t.Fatal("a failed delete reported removed:false, which is the answer for a note that simply is not there")
	}
}

// TestHandlersPropagateARefusedTransaction: a Runtime the core has released,
// or a role that bound no pool, refuses before opening anything. Every handler
// that takes a transaction must hand that back rather than answer over it.
func TestHandlersPropagateARefusedTransaction(t *testing.T) {
	refused := errors.New("compose: this role bound no pool")
	for name, call := range map[string]func(rt *fakeRuntime) error{
		"list": func(rt *fakeRuntime) error {
			_, err := listNotes(context.Background(), rt, json.RawMessage(`{}`))
			return err
		},
		"add": func(rt *fakeRuntime) error {
			_, err := addNote(context.Background(), rt, json.RawMessage(`{"body":"x"}`))
			return err
		},
		"remove": func(rt *fakeRuntime) error {
			// A well-formed id, because the shape check now runs BEFORE the
			// transaction: what this case asserts is that the runtime's own
			// refusal reaches the caller unwrapped, and an id the handler
			// rejects outright never gets as far as the transaction to be
			// refused by it.
			_, err := removeNote(context.Background(), rt, json.RawMessage(`{"id":"018f3a1b-0000-7000-8000-0000000000d0"}`))
			return err
		},
		"heartbeat": func(rt *fakeRuntime) error { return heartbeat(context.Background(), rt) },
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			rt.txErr = refused
			if err := call(rt); !errors.Is(err, refused) {
				t.Fatalf("err = %v, want the runtime's own refusal unwrapped", err)
			}
		})
	}
}
