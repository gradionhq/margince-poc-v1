// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Batch identity resolution over a real migrated Postgres. The translation
// rules are unit-tested against the ladder's own result types (resolve_test.go);
// what only a database can show is that the ladder is actually being ASKED the
// right question — that an address the caller sent reaches the exact tier, that
// an organization is found by a domain nobody typed as one, and that the two
// halves of a mixed batch line up with the candidates that produced them.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestResolveFindsAPersonByAClaimedAddress(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person, _ := e.seedEmployedPerson(ctx, t, "Anna Weber", "anna@acme.example", "Acme GmbH", "acme.example")

	out, err := e.store.Resolve(ctx, []ResolveCandidate{
		{Kind: ResolvePerson, Name: "A. Weber", Emails: []string{"ANNA@acme.example"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(out) != 1 || out[0].Verdict != VerdictExact {
		t.Fatalf("got %+v, want one exact answer", out)
	}
	if out[0].Refs[0].ID != person.UUID {
		t.Errorf("resolved to %s, want the seeded person %s", out[0].Refs[0].ID, person.UUID)
	}
	if out[0].Refs[0].MatchedOn != laneEmail {
		t.Errorf("matched on %q, want the address lane", out[0].Refs[0].MatchedOn)
	}
}

// The domain nobody typed. A caller holding a business card has an address, and
// the organization tier is keyed on domain — so the derivation is what makes the
// difference between an exact hit and a name guess.
func TestResolveFindsAnOrganizationByTheDomainInsideAnAddress(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, org := e.seedEmployedPerson(ctx, t, "Anna Weber", "anna@acme.example", "Acme GmbH", "acme.example")

	out, err := e.store.Resolve(ctx, []ResolveCandidate{
		{Kind: ResolveOrganization, Name: "Something Else Entirely", Emails: []string{"info@acme.example"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(out) != 1 || out[0].Verdict != VerdictExact {
		t.Fatalf("got %+v, want the domain to have resolved exactly", out)
	}
	if out[0].Refs[0].ID != org.UUID {
		t.Errorf("resolved to %s, want the seeded organization %s", out[0].Refs[0].ID, org.UUID)
	}
}

// A consumer-mail address contributes NO domain. Without this, every private
// address would collide onto whichever company first claimed that provider.
func TestResolveIgnoresAConsumerMailDomainOnAnOrganization(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	e.seedEmployedPerson(ctx, t, "Anna Weber", "anna@gmail.com", "Gmail Holdings", "gmail.com")

	out, err := e.store.Resolve(ctx, []ResolveCandidate{
		{Kind: ResolveOrganization, Name: "Kärcher", Emails: []string{"someone@gmail.com"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	for _, ref := range out[0].Refs {
		if ref.MatchedOn == axisDomain {
			t.Errorf("a consumer-mail domain reached the exact tier and matched %s", ref.ID)
		}
	}
}

// A mixed batch answers in order, one answer per candidate — the alignment every
// caller depends on to know which answer belongs to which payload.
func TestResolveAnswersAMixedBatchInOrder(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person, org := e.seedEmployedPerson(ctx, t, "Anna Weber", "anna@acme.example", "Acme GmbH", "acme.example")

	out, err := e.store.Resolve(ctx, []ResolveCandidate{
		{Kind: ResolveOrganization, Domains: []string{"acme.example"}},
		{Kind: ResolvePerson, Emails: []string{"nobody@nowhere.example"}},
		{Kind: ResolvePerson, Emails: []string{"anna@acme.example"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(out) != 3 {
		t.Fatalf("got %d answers for 3 candidates", len(out))
	}
	if out[0].Verdict != VerdictExact || out[0].Refs[0].ID != org.UUID {
		t.Errorf("answer 0 = %+v, want the organization", out[0])
	}
	if out[1].Verdict != VerdictNone {
		t.Errorf("answer 1 = %+v, want no match for an address nobody holds", out[1])
	}
	if out[2].Verdict != VerdictExact || out[2].Refs[0].ID != person.UUID {
		t.Errorf("answer 2 = %+v, want the person", out[2])
	}
}

// A caller who may not read organizations is refused BEFORE the person half
// runs — otherwise the refusal would arrive after the batch had already told
// them which addresses exist.
func TestResolveRefusesTheWholeBatchOnAMissingGrant(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	e.seedEmployedPerson(ctx, t, "Anna Weber", "anna@acme.example", "Acme GmbH", "acme.example")

	peopleOnly := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{entityPerson: {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})

	out, err := e.store.Resolve(peopleOnly, []ResolveCandidate{
		{Kind: ResolvePerson, Emails: []string{"anna@acme.example"}},
		{Kind: ResolveOrganization, Domains: []string{"acme.example"}},
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("err = %v, want a permission denial", err)
	}
	if out != nil {
		t.Errorf("the person half was answered anyway: %+v", out)
	}
}
