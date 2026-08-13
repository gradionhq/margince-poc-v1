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
	"github.com/gradionhq/margince/backend/pkg/extension/crm"
)

// subjectID is the record a filed note is filed to.
const subjectID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"

// filedActivityID is what the scripted core port answers with — the receipt the
// note's own row then has to carry.
const filedActivityID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// filingRuntime is a runtime whose core port is scripted and whose note insert
// answers a row already carrying the receipt.
func filingRuntime() *fakeRuntime {
	rt := newRuntime()
	rt.tx.core = &fakeCore{activity: crm.Activity{Id: filedActivityID}}
	rt.tx.row = filedNoteRow("11111111-1111-4111-8111-111111111111", kindNote,
		"a filed note", callerUserID, false, filedActivityID, time.Now().UTC())
	return rt
}

func fileOne(t *testing.T, rt *fakeRuntime, body string) (note, error) {
	t.Helper()
	raw, err := fileNote(context.Background(), rt, json.RawMessage(
		`{"body":"`+body+`","subject_type":"person","subject_id":"`+subjectID+`"}`))
	if err != nil {
		return note{}, err
	}
	var filed note
	if unmarshalErr := json.Unmarshal(raw, &filed); unmarshalErr != nil {
		t.Fatalf("the answer does not decode as a note: %v", unmarshalErr)
	}
	return filed, nil
}

// A filing writes BOTH, and the timeline entry says what the note says.
func TestFilingWritesTheNoteAndTheActivityTogether(t *testing.T) {
	rt := filingRuntime()
	filed, err := fileOne(t, rt, "a filed note")
	if err != nil {
		t.Fatalf("fileNote: %v", err)
	}

	if len(rt.tx.core.requested) != 1 {
		t.Fatalf("the core port was called %d time(s), want once", len(rt.tx.core.requested))
	}
	request := rt.tx.core.requested[0]
	if request.Kind != crm.CreateActivityRequestKindNote {
		t.Errorf("activity kind = %q, want note", request.Kind)
	}
	if request.Body == nil || *request.Body != "a filed note" {
		t.Errorf("the activity does not carry the note's own text: %v", request.Body)
	}
	if request.Source != filingSource {
		t.Errorf("activity source = %q, want %q — the timeline is where a reader learns a unit filed this", request.Source, filingSource)
	}
	if request.Links == nil || len(*request.Links) != 1 {
		t.Fatalf("the activity names %v subjects, want the one the caller asked for", request.Links)
	}
	link := (*request.Links)[0]
	if link.EntityId != subjectID || string(link.EntityType) != "person" {
		t.Errorf("the activity is filed against %s/%s, want person/%s", link.EntityType, link.EntityId, subjectID)
	}

	// The unit's own row, and the receipt on it. The insert is the only
	// statement: a filing reads nothing back separately.
	if len(rt.tx.statements) != 1 || !strings.Contains(rt.tx.statements[0], "INSERT INTO "+noteTable) {
		t.Fatalf("the unit's own row was not written: %v", rt.tx.statements)
	}
	if got := rt.tx.args[0][4]; got != filedActivityID {
		t.Errorf("the note stores %v as its receipt, want the activity the port answered with (%s)", got, filedActivityID)
	}
	if filed.FiledActivityID == nil || *filed.FiledActivityID != filedActivityID {
		t.Errorf("the answer does not tell the caller which activity it filed: %v", filed.FiledActivityID)
	}
}

// TestAFilingRecordsBothHistories: the port wrote the ACTIVITY's ledger row —
// that record's history is the core's — and this asserts the other half, the
// notepad's own, which no core write could have made because no core code knows
// this table exists. The record the note reached rides in the evidence, because
// the note's own columns name the activity and not what it sits on.
func TestAFilingRecordsBothHistories(t *testing.T) {
	rt := filingRuntime()
	if _, err := fileOne(t, rt, "a filed note"); err != nil {
		t.Fatalf("fileNote: %v", err)
	}
	if len(rt.tx.audited) != 1 {
		t.Fatalf("the filing recorded %d ledger rows of its own, want 1", len(rt.tx.audited))
	}
	change := rt.tx.audited[0]
	if change.Action != extension.AuditCreate || change.Entity != noteEntity {
		t.Errorf("recorded %s on %s, want a create of %s", change.Action, change.Entity, noteEntity)
	}
	for _, want := range []string{"person", subjectID} {
		if !strings.Contains(string(change.Detail), want) {
			t.Errorf("the evidence does not name the record the note was filed to (%q): %s", want, change.Detail)
		}
	}
	if len(rt.tx.published) != 1 || rt.tx.published[0].Verb != eventNoteFiled {
		t.Errorf("published %v, want one %s", rt.tx.published, eventNoteFiled)
	}
}

// The activity is written FIRST, so a refused core write leaves no note behind
// — the whole point of one transaction, asserted on the half that would
// otherwise be silent.
func TestARefusedCoreWriteWritesNoNote(t *testing.T) {
	rt := filingRuntime()
	rt.tx.core.err = extension.ErrForbidden

	_, err := fileOne(t, rt, "a filed note")
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want the port's refusal to survive", err)
	}
	if len(rt.tx.statements) != 0 {
		t.Errorf("the unit wrote its own row anyway: %v", rt.tx.statements)
	}
	if !strings.Contains(err.Error(), "activity permission") {
		t.Errorf("the refusal does not say what the seat is missing: %v", err)
	}
}

// Each refusal class keeps its sentinel through the unit's own wording, because
// a caller that switches on the class must still be able to.
func TestFilingRefusalsKeepTheirClass(t *testing.T) {
	for _, sentinel := range []error{
		extension.ErrForbidden,
		extension.ErrNotFound,
		extension.ErrOverlayUnsupported,
	} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			rt := filingRuntime()
			rt.tx.core.err = sentinel
			if _, err := fileOne(t, rt, "a filed note"); !errors.Is(err, sentinel) {
				t.Errorf("err = %v, want it to still be %v", err, sentinel)
			}
		})
	}
}

// A subject the contract's enum does not name, and an id that is not an id, are
// refused before anything opens a transaction.
func TestFilingRefusesASubjectItCannotName(t *testing.T) {
	for name, body := range map[string]string{
		"a record kind the timeline does not carry": `{"body":"x","subject_type":"invoice","subject_id":"` + subjectID + `"}`,
		"an id that is not a UUID":                  `{"body":"x","subject_type":"person","subject_id":"not-an-id"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := filingRuntime()
			if _, err := fileNote(context.Background(), rt, json.RawMessage(body)); err == nil {
				t.Fatal("the filing was accepted")
			}
			if len(rt.tx.core.requested) != 0 || len(rt.tx.statements) != 0 {
				t.Errorf("something was written before the refusal: core=%v sql=%v", rt.tx.core.requested, rt.tx.statements)
			}
		})
	}
}

// The body rule is the notepad's, whichever operation takes it.
func TestFilingHoldsTheBodyToTheSameRule(t *testing.T) {
	rt := filingRuntime()
	if _, err := fileOne(t, rt, "   "); err == nil {
		t.Fatal("a blank note was filed")
	}
	if len(rt.tx.core.requested) != 0 {
		t.Errorf("the core was asked to write a blank note: %v", rt.tx.core.requested)
	}
}

// Authorship comes from the caller on this path too — the argument for
// callerAuthor existing at all is that no operation may take it from the body.
func TestFilingStampsTheAuthorFromTheCaller(t *testing.T) {
	rt := filingRuntime()
	rt.caller = extension.Caller{Type: extension.CallerAgent, UserID: callerUserID, IsAgent: true}
	if _, err := fileOne(t, rt, "a filed note"); err != nil {
		t.Fatalf("fileNote: %v", err)
	}
	userID, isUser := rt.tx.args[0][2].(*string)
	isAgent, isFlag := rt.tx.args[0][3].(*bool)
	if !isUser || !isFlag || userID == nil || isAgent == nil {
		t.Fatalf("the insert's author columns are %v/%v, want the caller's pair", rt.tx.args[0][2], rt.tx.args[0][3])
	}
	if *userID != callerUserID {
		t.Errorf("author = %q, want the caller's own user id", *userID)
	}
	if !*isAgent {
		t.Error("is_agent = false, want the agent flag the caller arrived with")
	}
}
