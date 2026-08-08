// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A single exact lane is the one answer that resolves itself.
func TestAnUncontestedExactLaneResolves(t *testing.T) {
	person := ids.PersonID{UUID: ids.NewV7()}
	got := personOutcome(PersonResolution{
		Decision: DecisionExactCollision, PersonID: person, MatchedLane: laneEmail,
	})

	if got.Verdict != VerdictExact {
		t.Fatalf("verdict = %q, want exact", got.Verdict)
	}
	if len(got.Refs) != 1 || got.Refs[0].ID != person.UUID {
		t.Fatalf("refs = %+v, want the one person the address named", got.Refs)
	}
	if got.Refs[0].Confidence != 1 || got.Refs[0].MatchedOn != laneEmail {
		t.Errorf("ref = %+v, want certainty 1 on the lane that matched", got.Refs[0])
	}
}

// A lane conflict is AMBIGUITY here, even though the ladder routes it.
//
// The ladder must route: an inbound message with nowhere to land is worse than
// one on the record whose binding was established first. A read has no message
// to land, so passing the routed id along alone would hand a caller one answer
// while hiding that another key names someone else.
func TestALaneConflictIsReportedAsAmbiguityRatherThanRouted(t *testing.T) {
	routed, rival := ids.PersonID{UUID: ids.NewV7()}, ids.PersonID{UUID: ids.NewV7()}
	got := personOutcome(PersonResolution{
		Decision: DecisionExactCollision, PersonID: routed, MatchedLane: laneEmail,
		Conflict: &LaneConflict{
			RoutedTo: routed, Rival: rival, RoutedLane: laneEmail, RivalLane: lanePhone,
		},
	})

	if got.Verdict != VerdictAmbiguous {
		t.Fatalf("verdict = %q, want ambiguous — two keys named two people", got.Verdict)
	}
	if len(got.Refs) != 2 {
		t.Fatalf("refs = %+v, want both sides of the disagreement", got.Refs)
	}
	if got.Refs[0].ID != routed.UUID || got.Refs[1].ID != rival.UUID {
		t.Errorf("refs = %+v, want the routed record first", got.Refs)
	}
	if got.Refs[1].MatchedOn != lanePhone {
		t.Errorf("the rival's own lane was lost: %+v", got.Refs[1])
	}
}

// A fuzzy hit is never exact, whatever it scored — DEDUPE_FUZZY_AUTOMERGE is
// pinned *never*, and this read must not be the surface that undoes that.
func TestAFuzzyHitIsNeverExactHoweverHighItScored(t *testing.T) {
	got := personOutcome(PersonResolution{
		Decision: DecisionFuzzyReview, PersonID: ids.PersonID{UUID: ids.NewV7()}, Confidence: 0.99,
	})

	if got.Verdict != VerdictAmbiguous {
		t.Errorf("verdict = %q for a 0.99 fuzzy match, want ambiguous", got.Verdict)
	}
	if len(got.Refs) != 1 || got.Refs[0].Confidence != 0.99 {
		t.Errorf("refs = %+v, want the scored candidate with its score", got.Refs)
	}
}

func TestNoMatchResolvesToNothing(t *testing.T) {
	got := personOutcome(PersonResolution{Decision: DecisionNoMatch})
	if got.Verdict != VerdictNone || len(got.Refs) != 0 {
		t.Errorf("got %+v, want an empty `none`", got)
	}
}

// Every rival the organization ladder ranked comes back, not just the best one.
// A single winner would let one dismissed pair hide a genuine duplicate behind
// it — the same reason OrganizationMatch carries a list at all.
func TestEveryRankedOrganizationRivalSurvivesTranslation(t *testing.T) {
	first, second := ids.OrganizationID{UUID: ids.NewV7()}, ids.OrganizationID{UUID: ids.NewV7()}
	got := organizationOutcome(OrganizationMatch{
		Decision: DecisionFuzzyReview,
		Ranked: []OrganizationCandidateScore{
			{OrganizationID: first, Confidence: 0.91, MatchedField: "display_name"},
			{OrganizationID: second, Confidence: 0.78, MatchedField: "legal_name"},
		},
	})

	if got.Verdict != VerdictAmbiguous || len(got.Refs) != 2 {
		t.Fatalf("got %+v, want both ranked rivals", got)
	}
	if got.Refs[1].MatchedOn != "legal_name" {
		t.Errorf("the axis a pair was scored on was lost: %+v", got.Refs[1])
	}
}

func TestOrganizationDomainHitAndMissTranslate(t *testing.T) {
	org := ids.OrganizationID{UUID: ids.NewV7()}
	hit := organizationOutcome(OrganizationMatch{Decision: DecisionExactCollision, OrganizationID: org})
	if hit.Verdict != VerdictExact || len(hit.Refs) != 1 || hit.Refs[0].MatchedOn != axisDomain {
		t.Errorf("got %+v, want an exact hit on the domain axis", hit)
	}
	if miss := organizationOutcome(OrganizationMatch{Decision: DecisionNoMatch}); miss.Verdict != VerdictNone {
		t.Errorf("got %+v, want an empty `none`", miss)
	}
}

// The object grant is taken for EVERY kind the batch asks about, and it is taken
// before any of it runs — a batch that resolved the person half and then refused
// on the organization half would already have told the caller which addresses
// exist.
func TestResolveRequiresTheGrantForEveryKindInTheBatch(t *testing.T) {
	peopleOnly := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:rep", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{entityPerson: {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})

	if err := requireResolveAuthority(peopleOnly, []ResolveCandidate{{Kind: ResolvePerson}}); err != nil {
		t.Fatalf("a person-only batch was refused for a caller who may read people: %v", err)
	}
	err := requireResolveAuthority(peopleOnly, []ResolveCandidate{
		{Kind: ResolvePerson}, {Kind: ResolveOrganization},
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("err = %v, want a permission denial for the kind the caller may not read", err)
	}
}

// An email contributes its domain, because the exact tier is keyed on domain and
// a caller holding a business card has an address. A CONSUMER domain never does:
// the ladder's contract forbids it, and one that got through would collide every
// private address onto whichever company first claimed that provider.
func TestCompanyDomainsDerivesFromEmailsAndDropsConsumerMail(t *testing.T) {
	got := companyDomains(ResolveCandidate{
		Domains: []string{"Acme.example", " acme.example "},
		Emails:  []string{"anna@acme.example", "anna@gmail.com", "not-an-address"},
	}, freemail.New(nil, nil))

	if !slices.Contains(got, "acme.example") {
		t.Errorf("domains = %v, want the company domain", got)
	}
	if slices.Contains(got, "gmail.com") {
		t.Errorf("domains = %v, want no consumer-mail domain — it matches every private address", got)
	}
	if len(got) != 1 {
		t.Errorf("domains = %v, want one entry: the same domain claimed twice is one key", got)
	}
}

// A carve-out an admin asserted travels with the candidate. A `never` entry says
// "this IS a company's domain, whatever the shipped list claims", and judging it
// by the baseline alone would keep dropping the agreement they made explicit.
func TestAnAdminCarveOutKeepsADomainTheBaselineWouldDrop(t *testing.T) {
	got := companyDomains(ResolveCandidate{Emails: []string{"anna@gmail.com"}},
		freemail.New(nil, []string{"gmail.com"}))

	if !slices.Contains(got, "gmail.com") {
		t.Errorf("domains = %v, want the domain the workspace carved out", got)
	}
}
