// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The distinction the surface was missing.
//
// An assistant asked to assign work searched `person` — the customer contacts
// — found two people with the right first name, and reported that neither was
// "listed under sales". The seat it wanted was the one the human was signed in
// as, and no tool could name it. assignee_id and owner_id take these ids.
func TestListColleaguesAnswersSeatsWithWhatAssignmentNeeds(t *testing.T) {
	lena := ids.NewV7()
	out, err := listColleagues{list: func(_ context.Context, q string) ([]Colleague, bool, error) {
		if q != "lena" {
			t.Errorf("the filter reached the roster as %q, want it forwarded", q)
		}
		return []Colleague{{
			UserID: lena, DisplayName: "Lena Fischer", Email: "lena.fischer@demo.test",
			SeatType: "full",
		}}, false, nil
	}}.Handle(context.Background(), json.RawMessage(`{"q":"lena"}`))
	if err != nil {
		t.Fatalf("listing colleagues answered %v, want the roster", err)
	}
	var got ListColleaguesResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(got.Colleagues) != 1 || got.Colleagues[0].UserID != lena {
		t.Fatalf("roster = %+v, want the seat that id names", got.Colleagues)
	}
	if got.Truncated {
		t.Error("a one-seat roster reports truncated, want it complete")
	}
}

// A filter matching nobody is an answer, not an error, and it serializes as an
// empty list rather than null — a caller iterating the result must not have to
// guard for both.
func TestListColleaguesAnswersAnEmptyRosterAsAList(t *testing.T) {
	out, err := listColleagues{list: func(context.Context, string) ([]Colleague, bool, error) {
		return nil, false, nil
	}}.Handle(context.Background(), json.RawMessage(`{"q":"nobody"}`))
	if err != nil {
		t.Fatalf("answered %v, want an empty roster", err)
	}
	if got := string(out); got != `{"colleagues":[]}` {
		t.Errorf("empty roster serialized as %s, want an empty array", got)
	}
}

// A capped roster says so. Told nothing, a caller reads 200 seats as the whole
// company and reports that a colleague does not work here.
func TestListColleaguesSaysWhenTheRosterIsLongerThanTheAnswer(t *testing.T) {
	out, err := listColleagues{list: func(context.Context, string) ([]Colleague, bool, error) {
		return []Colleague{{UserID: ids.NewV7(), DisplayName: "A"}}, true, nil
	}}.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("answered %v, want a roster", err)
	}
	var got ListColleaguesResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !got.Truncated {
		t.Error("a capped roster did not report truncated")
	}
}
