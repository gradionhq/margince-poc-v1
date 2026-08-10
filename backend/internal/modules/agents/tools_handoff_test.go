// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What prepare_handoff must report as missing, and — the half that is easier
// to get wrong — what it must NOT report as missing.
//
// A gap that fires when the field is present is worse than no gap at all: it
// teaches the reader to skim the list, which is the one thing the list cannot
// survive. So every gap below is asserted in both directions, from a fixture
// that is complete and then has exactly one thing taken away.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// wholeHandoff is a project with nothing missing: an owner, a target date, a
// titled contact, a priced won deal, and one promise that is not yet due.
// Every test below removes exactly one of those.
func wholeHandoff() HandoffFacts {
	owner := ids.NewV7()
	target := sweptAt().Add(90 * 24 * time.Hour)
	amount := int64(250_000)
	return HandoffFacts{
		AsOf: sweptAt(),
		Project: HandoffProject{
			ProjectID: ids.NewV7(), Name: "Acme ERP rollout", Key: "ERP", Phase: "delivering",
			OrganizationID: ids.NewV7(), OwnerID: &owner, TargetEndDate: &target,
		},
		Deals: []HandoffDeal{{
			DealID: ids.NewV7(), Name: "Acme ERP licence", Status: "won", AmountMinor: &amount,
		}},
		Stakeholders: []HandoffStakeholder{{PersonID: ids.NewV7(), Role: "Sponsor"}},
		OpenCommitments: []OpenCommitment{{
			TaskID: ids.NewV7(), Subject: "Book the kickoff", DueAt: at(48 * time.Hour),
		}},
	}
}

// prepared runs the tool over one fixture and returns the answer it serves.
func prepared(t *testing.T, facts HandoffFacts) PreparedHandoff {
	t.Helper()
	tool := prepareHandoff{read: func(context.Context, ids.UUID) (HandoffFacts, error) {
		return facts, nil
	}}
	raw, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+facts.Project.ProjectID.String()+`"}`))
	if err != nil {
		t.Fatalf("prepare_handoff: %v", err)
	}
	var out PreparedHandoff
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unreadable answer: %v", err)
	}
	return out
}

// gapNamed answers the gap with one code, and whether it was raised at all.
func gapNamed(h PreparedHandoff, code string) (HandoffGap, bool) {
	for _, gap := range h.Gaps {
		if gap.Code == code {
			return gap, true
		}
	}
	return HandoffGap{}, false
}

// A complete handover raises nothing. This is the assertion the others rest
// on: without it, a gap that always fires would pass every test below.
func TestACompleteHandoverReportsNoGaps(t *testing.T) {
	out := prepared(t, wholeHandoff())
	if len(out.Gaps) != 0 {
		t.Errorf("a handover with nothing missing raised %d gaps: %+v", len(out.Gaps), out.Gaps)
	}
}

func TestEachMissingFactRaisesItsOwnGapAndNamesTheFieldItWasReadOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   string
		source string
		strip  func(*HandoffFacts)
	}{
		{
			"nobody owns the work", gapNoDeliveryOwner, "project.owner_id",
			func(f *HandoffFacts) { f.Project.OwnerID = nil },
		},
		{
			"there is nothing to deliver against", gapNoTargetEndDate, "project.target_end_date",
			func(f *HandoffFacts) { f.Project.TargetEndDate = nil },
		},
		{
			"nobody is named on the client side", gapNoStakeholder, "relationship.project_stakeholder",
			func(f *HandoffFacts) { f.Stakeholders = nil },
		},
		{
			"a named contact has no recorded part", gapStakeholderRoleUnset, "relationship.role",
			func(f *HandoffFacts) { f.Stakeholders[0].Role = "" },
		},
		{
			"nothing sold is recorded here", gapNoWonDeal, "deal.status",
			func(f *HandoffFacts) { f.Deals[0].Status = "open" },
		},
		{
			"the won deal carries no amount", gapUnpricedWonDeal, "deal.amount_minor",
			func(f *HandoffFacts) { f.Deals[0].AmountMinor = nil },
		},
		{
			"a promise is already late at handover", gapOverdueCommitment, "activity.due_at",
			func(f *HandoffFacts) { f.OpenCommitments[0].DueAt = at(-72 * time.Hour) },
		},
	} {
		facts := wholeHandoff()
		tc.strip(&facts)
		out := prepared(t, facts)

		gap, raised := gapNamed(out, tc.code)
		if !raised {
			t.Errorf("%s: no %q gap was raised (%+v)", tc.name, tc.code, out.Gaps)
			continue
		}
		if gap.Source != tc.source {
			t.Errorf("%s: gap %q names source %q, want %q", tc.name, tc.code, gap.Source, tc.source)
		}
		if gap.Message == "" {
			t.Errorf("%s: gap %q carries no message", tc.name, tc.code)
		}
	}
}

// A project with no won deal is missing the sale, not the price of one — two
// gaps about the same absence would read as two separate problems.
func TestAProjectWithNoWonDealIsNotAlsoReportedAsUnpriced(t *testing.T) {
	facts := wholeHandoff()
	facts.Deals[0].Status = "open"
	facts.Deals[0].AmountMinor = nil
	out := prepared(t, facts)

	if _, raised := gapNamed(out, gapUnpricedWonDeal); raised {
		t.Errorf("an unwon deal is reported as an unpriced won one: %+v", out.Gaps)
	}
	if _, raised := gapNamed(out, gapNoWonDeal); !raised {
		t.Errorf("a project with no won deal raises no %q gap: %+v", gapNoWonDeal, out.Gaps)
	}
}

// An open deal beside a won one is not a missing sale: the roll-up carries
// both, and only the won ones answer "what was sold".
func TestAnOpenDealBesideAWonOneIsNotAMissingSale(t *testing.T) {
	facts := wholeHandoff()
	facts.Deals = append(facts.Deals, HandoffDeal{
		DealID: ids.NewV7(), Name: "Acme phase two", Status: "open",
	})
	out := prepared(t, facts)

	if _, raised := gapNamed(out, gapNoWonDeal); raised {
		t.Errorf("a won deal beside an open one still reports no sale: %+v", out.Gaps)
	}
	// The open deal carries no amount either, and that must not be read as an
	// unpriced SALE — only a won deal is a sale.
	if _, raised := gapNamed(out, gapUnpricedWonDeal); raised {
		t.Errorf("an unpriced OPEN deal is reported as an unpriced sale: %+v", out.Gaps)
	}
}

func TestTheOverdueGapCountsOnlyThePromisesThatArePastDue(t *testing.T) {
	facts := wholeHandoff()
	facts.OpenCommitments = []OpenCommitment{
		{TaskID: ids.NewV7(), DueAt: at(-72 * time.Hour)},
		{TaskID: ids.NewV7(), DueAt: at(-24 * time.Hour)},
		{TaskID: ids.NewV7(), DueAt: at(48 * time.Hour)},
		{TaskID: ids.NewV7()},
	}
	out := prepared(t, facts)

	gap, raised := gapNamed(out, gapOverdueCommitment)
	if !raised {
		t.Fatalf("two late promises raised no overdue gap: %+v", out.Gaps)
	}
	if want := "2 commitments are"; !strings.Contains(gap.Message, want) {
		t.Errorf("overdue gap message = %q, want it to count %q — the undated and the "+
			"upcoming promise must not be counted as late", gap.Message, want)
	}
}

// The message reads as a sentence for one as well as for many.
func TestASingleGapReadsAsASentence(t *testing.T) {
	facts := wholeHandoff()
	facts.OpenCommitments[0].DueAt = at(-24 * time.Hour)
	gap, raised := gapNamed(prepared(t, facts), gapOverdueCommitment)
	if !raised {
		t.Fatal("one late promise raised no overdue gap")
	}
	if want := "1 commitment is"; !strings.Contains(gap.Message, want) {
		t.Errorf("overdue gap message = %q, want it to read %q", gap.Message, want)
	}
}

// Every list-shaped member answers [] rather than null, so a model reading the
// brief cannot mistake "none" for "unknown".
func TestAnEmptyHandoverAnswersEmptyListsNotNulls(t *testing.T) {
	owner := ids.NewV7()
	target := sweptAt()
	amount := int64(1)
	raw, err := json.Marshal(assembleHandoff(HandoffFacts{
		AsOf: sweptAt(),
		Project: HandoffProject{
			ProjectID: ids.NewV7(), Name: "Bare", OwnerID: &owner, TargetEndDate: &target,
		},
		Deals:        []HandoffDeal{{DealID: ids.NewV7(), Status: "won", AmountMinor: &amount}},
		Stakeholders: []HandoffStakeholder{{PersonID: ids.NewV7(), Role: "Sponsor"}},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"open_commitments", "gaps"} {
		if string(decoded[member]) != "[]" {
			t.Errorf("%s = %s, want []", member, decoded[member])
		}
	}
}

// A bound is not evidence of an absence.
//
// Two of the gaps are claims that something is NOT there, and neither can be
// read off a list that stopped at fifty rows. A project with fifty-one deals
// whose only won one sorted off the page would otherwise be told nothing was
// ever sold — a gap firing about a field that is present, which is the failure
// this whole set of gaps exists to prevent.
func TestABoundedListWithholdsTheGapsItCannotSupport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withheld string
		bound    func(*HandoffFacts)
	}{
		{
			"a truncated deal list cannot say nothing was sold", gapNoWonDeal,
			func(f *HandoffFacts) { f.Deals = nil; f.DealsTruncated = true },
		},
		{
			"a truncated stakeholder list cannot say nobody is named", gapNoStakeholder,
			func(f *HandoffFacts) { f.Stakeholders = nil; f.StakeholdersTruncated = true },
		},
	} {
		bounded := wholeHandoff()
		tc.bound(&bounded)
		if _, raised := gapNamed(prepared(t, bounded), tc.withheld); raised {
			t.Errorf("%s, but %q was raised anyway", tc.name, tc.withheld)
		}

		// And the same emptiness WITHOUT a bound still raises it — otherwise
		// this test would pass against a build that had simply deleted the gap.
		complete := wholeHandoff()
		tc.bound(&complete)
		complete.DealsTruncated, complete.StakeholdersTruncated = false, false
		if _, raised := gapNamed(prepared(t, complete), tc.withheld); !raised {
			t.Errorf("%s: a COMPLETE empty list raised no %q either, so the "+
				"withholding above proved nothing", tc.name, tc.withheld)
		}
	}
}

// The unpriced count is a statement about the deals that WERE read, so a bound
// does not withhold it — only the absence claim goes.
func TestABoundedDealListStillReportsWhatItRead(t *testing.T) {
	facts := wholeHandoff()
	facts.Deals[0].AmountMinor = nil
	facts.DealsTruncated = true

	if _, raised := gapNamed(prepared(t, facts), gapUnpricedWonDeal); !raised {
		t.Error("a bounded read withheld a count of what it actually read")
	}
}

// A bound changes what the answer MEANS, so the answer says so.
func TestABoundedHandoffSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bound func(*HandoffFacts)
		want  bool
	}{
		{"a truncated deal list warns", func(f *HandoffFacts) { f.DealsTruncated = true }, true},
		{"a truncated stakeholder list warns", func(f *HandoffFacts) { f.StakeholdersTruncated = true }, true},
		{"a truncated commitment sweep warns", func(f *HandoffFacts) { f.CommitmentsTruncated = true }, true},
		{"a complete briefing claims no bound", func(*HandoffFacts) {}, false},
	} {
		facts := wholeHandoff()
		tc.bound(&facts)
		tool := prepareHandoff{read: func(context.Context, ids.UUID) (HandoffFacts, error) {
			return facts, nil
		}}
		registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
		registry.Register(tool)

		out, err := registry.Invoke(scopedAgentCtx(principal.ScopeRead), "prepare_handoff",
			json.RawMessage(`{"project_id":"`+facts.Project.ProjectID.String()+`"}`))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		env := sealedEnvelope(t, out)
		if _, warned := warningNamed(env, warningSweepTruncated); warned != tc.want {
			t.Errorf("%s: truncation warning = %v, want %v (%v)", tc.name, warned, tc.want, env.Warnings)
		}
	}
}
