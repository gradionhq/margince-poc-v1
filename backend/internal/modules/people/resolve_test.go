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

// The fuzzy tier is never marked Exact, whatever it scored — DEDUPE_FUZZY_AUTOMERGE
// is pinned *never*, and this read must not be the surface that undoes it.
func TestAFuzzyHitIsNeverExactHoweverHighItScored(t *testing.T) {
	got := personOutcome(PersonResolution{
		Decision: DecisionFuzzyReview, PersonID: ids.PersonID{UUID: ids.NewV7()}, Confidence: 0.99,
	})

	if len(got.Refs) != 1 {
		t.Fatalf("got %+v, want the scored candidate", got)
	}
	if got.Refs[0].Exact {
		t.Error("a 0.99 name similarity was marked as a key hit, which makes it actionable")
	}
	if got.Refs[0].Confidence != 0.99 || got.Refs[0].MatchedOn != axisFullName {
		t.Errorf("ref = %+v, want the score and the axis it was scored on", got.Refs[0])
	}
}

func TestNoMatchResolvesToNothing(t *testing.T) {
	if got := personOutcome(PersonResolution{Decision: DecisionNoMatch}); len(got.Refs) != 0 {
		t.Errorf("got %+v, want nothing", got)
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

	if len(got.Refs) != 2 {
		t.Fatalf("got %+v, want both ranked rivals", got)
	}
	if got.Refs[1].MatchedOn != "legal_name" {
		t.Errorf("the axis a pair was scored on was lost: %+v", got.Refs[1])
	}
	for _, ref := range got.Refs {
		if ref.Exact {
			t.Errorf("a name similarity was marked as a key hit: %+v", ref)
		}
	}
}

func TestTheOrganizationFuzzyTranslationIgnoresANonFuzzyDecision(t *testing.T) {
	// The exact tier is answered before this is reached (exactOrganizationOwners),
	// so a collision arriving here would be a second, quieter exact path.
	org := ids.OrganizationID{UUID: ids.NewV7()}
	if got := organizationOutcome(OrganizationMatch{Decision: DecisionExactCollision, OrganizationID: org}); len(got.Refs) != 0 {
		t.Errorf("got %+v, want nothing: the exact tier does not come through here", got)
	}
	if got := organizationOutcome(OrganizationMatch{Decision: DecisionNoMatch}); len(got.Refs) != 0 {
		t.Errorf("got %+v, want nothing", got)
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
		// A URL, a bare name and a subdomain: what a model actually passes when
		// asked for "company domains".
		Domains: []string{"https://www.Acme.example/careers", " acme.example "},
		Emails:  []string{"anna@acme.example", "anna@gmail.com", "not-an-address"},
	}, freemail.New(nil, nil))

	if !slices.Contains(got, "acme.example") {
		t.Errorf("domains = %v, want the company domain", got)
	}
	if slices.Contains(got, "gmail.com") {
		t.Errorf("domains = %v, want no consumer-mail domain — it matches every private address", got)
	}
	if len(got) != 1 {
		t.Errorf("domains = %v, want one entry: a URL, a bare name and an address all name one key", got)
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
