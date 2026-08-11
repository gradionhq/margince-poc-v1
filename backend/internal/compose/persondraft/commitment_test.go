// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

import (
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

var draftedAt = time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

func envelopeAt(lang textlang.Lang, band convstate.Band) draftfloor.Envelope {
	return draftfloor.Envelope{
		Language:          string(lang),
		ConversationState: string(band),
		Now:               draftedAt.Format(time.RFC3339),
	}
}

// claim builds a contract claim, which is the shape the fold really reads.
//
// Every case here goes through foldClaims rather than hand-building ClaimIn,
// and that is the lesson this file was rewritten for: the first version of
// these tests fabricated a claim kind ("commitment") the contract never emits,
// so they passed against a branch that could not fire on a single real record.
func claim(kind crmcontracts.ConversationClaimKind, body string,
	status crmcontracts.ConversationClaimStatus, due *time.Time,
) crmcontracts.ConversationClaim {
	return crmcontracts.ConversationClaim{
		Id:               openapi_types.UUID(ids.NewV7()),
		SourceActivityId: openapi_types.UUID(ids.NewV7()),
		Kind:             kind,
		Body:             body,
		Status:           status,
		DueAt:            due,
	}
}

func at(day int) *time.Time {
	t := time.Date(2026, 7, day, 0, 0, 0, 0, time.UTC)
	return &t
}

// foldedFrom runs the real fold, so a case describes a Person360 the product
// could actually assemble.
func foldedFrom(claims ...crmcontracts.ConversationClaim) Input {
	view := crmcontracts.Person360{Claims: &claims}
	in := Input{
		Envelope:  envelopeAt(textlang.English, convstate.BandWeeks),
		Recipient: RecipientIn{ID: "p1", FirstName: "Priya"},
	}
	foldClaims(&in, view, draftedAt)
	return in
}

// An overdue promise of OURS is the reason to write today, so it leads — over a
// question they asked, which is the kind that led before.
func TestAnOverduePromiseOfOursLeadsTheDraft(t *testing.T) {
	in := foldedFrom(
		claim(crmcontracts.OpenQuestion, "the API rate limits", crmcontracts.ConversationClaimStatusOpen, nil),
		claim(crmcontracts.CommitmentOurs, "the integration scope document",
			crmcontracts.ConversationClaimStatusOpen, at(25)),
	)

	body := Deterministic(in).Body
	if !strings.Contains(body, "integration scope document") {
		t.Errorf("the overdue promise should lead:\n%s", body)
	}
	if strings.Contains(body, "API rate limits") {
		t.Errorf("a question should not lead while a promise is outstanding:\n%s", body)
	}
}

// The cases that must NOT lead. Each is a different sentence, and treating any
// of them as an overdue promise of ours puts a false claim in front of a
// customer.
func TestOnlyAnOpenOverduePromiseOfOursLeads(t *testing.T) {
	question := claim(crmcontracts.OpenQuestion, "the API rate limits",
		crmcontracts.ConversationClaimStatusOpen, nil)

	cases := []struct {
		name  string
		claim crmcontracts.ConversationClaim
	}{
		{
			name: "a promise THEY made, past its date",
			claim: claim(crmcontracts.CommitmentTheirs, "the signed order form",
				crmcontracts.ConversationClaimStatusOpen, at(25)),
		},
		{
			name: "a promise of ours already kept",
			claim: claim(crmcontracts.CommitmentOurs, "the integration scope document",
				crmcontracts.ConversationClaimStatusDone, at(25)),
		},
		{
			name: "a promise of ours still within its date",
			claim: func() crmcontracts.ConversationClaim {
				later := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
				return claim(crmcontracts.CommitmentOurs, "the integration scope document",
					crmcontracts.ConversationClaimStatusOpen, &later)
			}(),
		},
		{
			name: "a promise of ours with no date at all",
			claim: claim(crmcontracts.CommitmentOurs, "looking into the integration",
				crmcontracts.ConversationClaimStatusOpen, nil),
		},
		{
			name: "a promise due at this very instant is not yet late",
			claim: claim(crmcontracts.CommitmentOurs, "the integration scope document",
				crmcontracts.ConversationClaimStatusOpen, &draftedAt),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := Deterministic(foldedFrom(question, c.claim)).Body
			if !strings.Contains(body, "API rate limits") {
				t.Errorf("this should not have displaced the open question:\n%s", body)
			}
		})
	}
}

// The claims arrive newest-first and the fold keeps only a handful, so on a
// busy record the longest-overdue promise — the one that most needs saying —
// would fall outside the window. It is hoisted before the cap, not ranked
// after it.
func TestTheLongestOverduePromiseSurvivesABusyRecord(t *testing.T) {
	var claims []crmcontracts.ConversationClaim
	for i := range draftInputClaims + 3 {
		claims = append(claims, claim(crmcontracts.OpenQuestion,
			"a question about topic "+string(rune('A'+i)),
			crmcontracts.ConversationClaimStatusOpen, nil))
	}
	// Oldest, so newest-first ordering puts it last of all.
	claims = append(claims, claim(crmcontracts.CommitmentOurs, "the integration scope document",
		crmcontracts.ConversationClaimStatusOpen, at(2)))

	body := Deterministic(foldedFrom(claims...)).Body
	if !strings.Contains(body, "integration scope document") {
		t.Errorf("the overdue promise fell outside the claim window:\n%s", body)
	}
}

// The negative gate: the DATE is grounding for the writer and must not reach
// the recipient as a raw timestamp. A person writes "last month", not
// "2026-07-25T00:00:00Z".
func TestTheDueDateNeverReachesTheBodyAsATimestamp(t *testing.T) {
	in := foldedFrom(claim(crmcontracts.CommitmentOurs, "das Angebot",
		crmcontracts.ConversationClaimStatusOpen, at(25)))
	in.Envelope = envelopeAt(textlang.German, convstate.BandWeeks)

	body := Deterministic(in).Body
	for _, leaked := range []string{"2026-07-25", "T00:00:00Z"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the body leaked the raw date %q:\n%s", leaked, body)
		}
	}
}

// The overdue reading is the ENVELOPE's clock, not a fresh one: a draft stamped
// at one instant and judging dates at another can call the same promise overdue
// on one line and pending on the next.
func TestOverdueIsReadFromTheEnvelopesOwnClock(t *testing.T) {
	if got := envelopeAt(textlang.English, convstate.BandWeeks).At(); !got.Equal(draftedAt) {
		t.Fatalf("the envelope's clock reads %s, want %s", got, draftedAt)
	}

	// An unstamped envelope answers the zero time, and every comparison against
	// it treats a due date as "not yet" rather than declaring everything overdue.
	unstamped := draftfloor.Envelope{}
	if !unstamped.At().IsZero() {
		t.Error("an unstamped envelope should answer the zero time")
	}
}
