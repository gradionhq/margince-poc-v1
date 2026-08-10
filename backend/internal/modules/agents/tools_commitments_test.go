// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What review_commitments must say about a promise, and what it must never
// say.
//
// The whole tool is one judgement made three ways — undated, overdue,
// upcoming — and each of them is a claim a rep will act on. The tests below
// pin the boundary of each state against a FIXED instant, which is possible
// only because the tool takes its instant from the seam rather than from a
// clock: the same reason there is no time.Sleep anywhere near them.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sweptAt is the instant every commitment fixture is judged against.
func sweptAt() time.Time { return time.Date(2026, time.June, 10, 12, 0, 0, 0, time.UTC) }

func at(offset time.Duration) *time.Time {
	moment := sweptAt().Add(offset)
	return &moment
}

// commitmentSweepOf builds a seam that answers one fixed sweep.
func commitmentSweepOf(sweep CommitmentSweep) CommitmentLister {
	return func(context.Context, CommitmentQuery) (CommitmentSweep, error) { return sweep, nil }
}

func TestAPromiseIsJudgedAgainstTheInstantItWasSweptAt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dueAt *time.Time
		want  string
	}{
		{"a promise nobody dated is undated, not late", nil, commitmentUndated},
		{"a promise due tomorrow is upcoming", at(24 * time.Hour), commitmentUpcoming},
		{"a promise due one second from now is still upcoming", at(time.Second), commitmentUpcoming},
		{
			"a promise due exactly now is overdue: the moment arrived and it is not done",
			at(0), commitmentOverdue,
		},
		{"a promise due yesterday is overdue", at(-24 * time.Hour), commitmentOverdue},
	} {
		if got := commitmentState(tc.dueAt, sweptAt()); got != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Whole days ELAPSED, not calendar days crossed — the second needs a timezone
// this build does not store anywhere.
func TestDaysOverdueCountsWholeElapsedDays(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dueAt       *time.Time
		wantDays    int
		wantOverdue bool
	}{
		{"an undated promise is overdue by nothing", nil, 0, false},
		{"an upcoming promise is overdue by nothing", at(time.Hour), 0, false},
		{
			"hours late is late by no whole days, which is not the same as not late",
			at(-2 * time.Hour), 0, true,
		},
		{"one day and a bit late is one day late", at(-25 * time.Hour), 1, true},
		{"a week late is seven days late", at(-7 * 24 * time.Hour), 7, true},
	} {
		days, overdue := daysOverdue(tc.dueAt, sweptAt())
		if overdue != tc.wantOverdue || days != tc.wantDays {
			t.Errorf("%s: daysOverdue = (%d, %v), want (%d, %v)",
				tc.name, days, overdue, tc.wantDays, tc.wantOverdue)
		}
	}
}

// An overdue item carries the count; an upcoming one carries no count at all
// rather than a zero a reader would print as "0 days overdue".
func TestOnlyAnOverduePromiseCarriesADayCount(t *testing.T) {
	overdue := OpenCommitment{TaskID: ids.NewV7(), DueAt: at(-25 * time.Hour)}.wire(sweptAt())
	if overdue.DaysOverdue == nil || *overdue.DaysOverdue != 1 {
		t.Errorf("an overdue promise carries days_overdue = %v, want 1", overdue.DaysOverdue)
	}
	upcoming := OpenCommitment{TaskID: ids.NewV7(), DueAt: at(time.Hour)}.wire(sweptAt())
	if upcoming.DaysOverdue != nil {
		t.Errorf("an upcoming promise carries days_overdue = %v, want absent", *upcoming.DaysOverdue)
	}
}

// An unowned promise is the state this tool exists to surface, so the two
// assignee members are absent TOGETHER — a name beside no id would be a
// promise attributed to nobody in particular.
func TestAnUnownedPromiseCarriesNeitherIdNorName(t *testing.T) {
	item := OpenCommitment{TaskID: ids.NewV7(), AssigneeName: "leaked"}.wire(sweptAt())
	if item.AssigneeID != nil || item.AssigneeName != "" {
		t.Errorf("an unassigned promise reports assignee (%v, %q), want both absent",
			item.AssigneeID, item.AssigneeName)
	}
}

// A promise that names no record answers an empty list, never null: a model
// handed null reads it as "unknown" where an empty array says "none".
func TestAPromiseAboutNothingAnswersAnEmptyListNotNull(t *testing.T) {
	raw, err := json.Marshal(OpenCommitment{TaskID: ids.NewV7()}.wire(sweptAt()))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["about"]) != "[]" {
		t.Errorf("about = %s, want []", decoded["about"])
	}
}

func TestTheAnswerCarriesTheInstantItsStatesWereJudgedAgainst(t *testing.T) {
	tool := reviewCommitments{list: commitmentSweepOf(CommitmentSweep{
		AsOf:        sweptAt(),
		Commitments: []OpenCommitment{{TaskID: ids.NewV7(), Subject: "Send the SOW", DueAt: at(-48 * time.Hour)}},
	})}

	raw, err := tool.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out ReviewCommitmentsResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.AsOf.Equal(sweptAt()) {
		t.Errorf("as_of = %v, want the sweep's own instant %v — a state with no instant "+
			"beside it cannot be told from a stale one", out.AsOf, sweptAt())
	}
	if len(out.Commitments) != 1 || out.Commitments[0].State != commitmentOverdue {
		t.Fatalf("got %+v, want one overdue commitment", out.Commitments)
	}
	if out.Commitments[0].DaysOverdue == nil || *out.Commitments[0].DaysOverdue != 2 {
		t.Errorf("days_overdue = %v, want 2", out.Commitments[0].DaysOverdue)
	}
}

// A bounded sweep says so. The same claim the other capped reads on this
// surface make, and it matters most here: the question is whether anything is
// being dropped, and a silently truncated answer says no.
func TestABoundedCommitmentSweepSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		truncated bool
	}{
		{"a sweep that hit its bound warns", true},
		{"a complete sweep claims no bound", false},
	} {
		tool := reviewCommitments{list: commitmentSweepOf(CommitmentSweep{
			AsOf:        sweptAt(),
			Commitments: []OpenCommitment{{TaskID: ids.NewV7()}},
			Truncated:   tc.truncated,
		})}
		registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
		registry.Register(tool)

		out, err := registry.Invoke(scopedAgentCtx(principal.ScopeRead),
			"review_commitments", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		env := sealedEnvelope(t, out)
		if _, warned := warningNamed(env, warningSweepTruncated); warned != tc.truncated {
			t.Errorf("%s: truncation warning = %v, want %v (%v)", tc.name, warned, tc.truncated, env.Warnings)
		}
	}
}

// The schema forbids a limit outside 1..50; this is what holds for a client
// that did not read it.
func TestALimitOutsideTheServedRangeIsRefusedByName(t *testing.T) {
	tool := reviewCommitments{list: commitmentSweepOf(CommitmentSweep{AsOf: sweptAt()})}
	for _, limit := range []string{"-1", "51"} {
		_, err := tool.Handle(context.Background(), json.RawMessage(`{"limit":`+limit+`}`))
		var badArgs *BadArgsError
		if !errors.As(err, &badArgs) {
			t.Errorf("limit %s → %v, want a BadArgsError naming the bound", limit, err)
		}
	}
	if _, err := tool.Handle(context.Background(), json.RawMessage(`{"limit":50}`)); err != nil {
		t.Errorf("the ceiling itself is refused: %v", err)
	}
}
