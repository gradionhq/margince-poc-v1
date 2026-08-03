// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The regressions for duplicates that actually reached a live workspace, plus
// the bypasses that let them in. Each test names the shape it prevents rather
// than the function it calls, because the function is not the point — a future
// refactor may move it, and the duplicate must stay impossible either way.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asLeadOwner is the shared dedupe context plus the two grants promotion
// needs. It is separate from as() because widening that one would hand lead
// rights to every test that uses it, and several of them exist to prove a
// caller WITHOUT a grant is refused.
func (e *dedupeEnv) asLeadOwner() context.Context {
	ctx := e.as()
	actor, _ := principal.Actor(ctx)
	actor.Permissions.Objects["lead"] = principal.ObjectGrant{
		Create: true, Read: true, Update: true,
	}
	return principal.WithActor(ctx, actor)
}

// orgCandidatesFor counts the review-queue pairs filed for an organization,
// in any disposition.
func (e *dedupeEnv) orgCandidatesFor(ctx context.Context, t *testing.T, org ids.OrganizationID) int {
	t.Helper()
	var n int
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM dedupe_candidate
			 WHERE entity_type = 'organization' AND (left_org_id = $1 OR right_org_id = $1)`,
			org).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count org dedupe candidates: %v", err)
	}
	return n
}

// TestARenamedOrganizationIsRescoredAgainstItsNeighbours is the Baqend
// regression. A company captured from a second domain is named after that
// domain, so at create time it resembles nothing. The signature sweep later
// renames it to the company's real name — the first moment the duplicate is
// visible, and the moment nothing used to look.
func TestARenamedOrganizationIsRescoredAgainstItsNeighbours(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	incumbent, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Baqend", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "baqend.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same company captured from its product's domain: named "Speedkit",
	// provisional, and no relation to "Baqend" that any name comparison could
	// see at this point.
	twin, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Speedkit", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "speedkit.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	twinID := ids.From[ids.OrganizationKind](ids.UUID(twin.Id))
	if got := e.orgCandidatesFor(ctx, t, twinID); got != 0 {
		t.Fatalf("dedupe candidates before the rename = %d, want 0 — the two names have nothing in common yet", got)
	}

	// The signature sweep promotes the name the company's own people sign with.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE organization SET name_source = 'domain' WHERE id = $1`, twinID); err != nil {
			return err
		}
		_, err := e.store.PromoteOrgNameTx(ctx, tx, twinID, "Baqend GmbH", "two employees")
		return err
	}); err != nil {
		t.Fatalf("promote org name: %v", err)
	}

	if got := e.orgCandidatesFor(ctx, t, twinID); got != 1 {
		t.Fatalf("dedupe candidates after the rename = %d, want 1 — "+
			"\"Baqend GmbH\" normalizes onto the live \"Baqend\" and the pair belongs on the queue", got)
	}
	// The rename itself stands: detection never blocks the write it observed.
	after, err := e.store.GetOrganization(ctx, twinID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after.DisplayName != "Baqend GmbH" {
		t.Fatalf("display_name = %q, want the promoted name — the re-check must not undo the rename", after.DisplayName)
	}
	if incumbent.Id == twin.Id {
		t.Fatal("the two organizations collapsed into one — fuzzy matching must never merge")
	}
}

// TestARenamedOrganizationDoesNotRefileAPairTheQueueAnswered keeps the
// re-check from re-asking. It runs on every rename, and a pair a human
// dismissed is answered for good.
func TestARenamedOrganizationDoesNotRefileAPairTheQueueAnswered(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	if _, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Northwind Logistics", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "northwind.test", IsPrimary: true}},
	}); err != nil {
		t.Fatal(err)
	}
	twin, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Provisional Name", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "northwind-eu.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	twinID := ids.From[ids.OrganizationKind](ids.UUID(twin.Id))

	rename := func(to string) {
		t.Helper()
		if err := e.store.tx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx,
				`UPDATE organization SET name_source = 'domain' WHERE id = $1`, twinID); err != nil {
				return err
			}
			_, err := e.store.PromoteOrgNameTx(ctx, tx, twinID, to, "corroborated")
			return err
		}); err != nil {
			t.Fatalf("promote org name to %q: %v", to, err)
		}
	}
	rename("Northwind Logistics GmbH")
	if got := e.orgCandidatesFor(ctx, t, twinID); got != 1 {
		t.Fatalf("candidates after the first rename = %d, want 1", got)
	}
	// A second rename that still resembles the same incumbent must not file the
	// pair again — the queue already holds it.
	rename("Northwind Logistics AG")
	if got := e.orgCandidatesFor(ctx, t, twinID); got != 1 {
		t.Fatalf("candidates after a second rename = %d, want 1 — the pair was already filed", got)
	}
}

// TestOrganizationDedupeReadsTheLegalNameAxis covers the other half of the
// Baqend shape: two records whose display names differ but whose registered
// name is identical. Before this the fuzzy tier compared display names only,
// so the strongest signal on the row was invisible to it.
func TestOrganizationDedupeReadsTheLegalNameAxis(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	legal := "Contoso Handels GmbH"
	if _, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Contoso Shop", LegalName: &legal, Source: "manual",
		Domains: []OrgDomainInput{{Domain: "contoso-shop.test", IsPrimary: true}},
	}); err != nil {
		t.Fatal(err)
	}

	// A different marketing name, no shared domain — the two collide only on
	// the registered entity.
	m := e.dedupeOrgInTx(ctx, t, OrganizationCandidate{
		DisplayName: "Contoso Wholesale",
		LegalName:   "Contoso Handels GmbH",
		Domains:     []string{"contoso-wholesale.test"},
	})
	if m.Decision != DecisionFuzzyReview {
		t.Fatalf("decision = %s (confidence %.4f), want fuzzy_review on the legal-name axis", m.Decision, m.Confidence)
	}
	if m.Confidence != 1 {
		t.Fatalf("confidence = %.4f, want 1.0 — the registered names are identical", m.Confidence)
	}
}

// TestOrganizationDedupeExcludesItself guards the re-check's own precondition:
// an existing row scored against the workspace holds its own name and its own
// domains, and without self-exclusion it matches itself perfectly and hides
// every real rival behind that score.
func TestOrganizationDedupeExcludesItself(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	subject, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Umbrella Corp", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "umbrella.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	subjectID := ids.From[ids.OrganizationKind](ids.UUID(subject.Id))

	// Without the exclusion this is an exact domain collision with itself.
	m := e.dedupeOrgInTx(ctx, t, OrganizationCandidate{
		DisplayName: "Umbrella Corp",
		Domains:     []string{"umbrella.test"},
		ExcludeID:   &subjectID,
	})
	if m.Decision != DecisionNoMatch {
		t.Fatalf("decision = %s, want no_match — the row must not match itself", m.Decision)
	}
}

// TestPromotingALeadRunsTheFullPersonLadder closes the promotion bypass. The
// path used to probe person_email by hand: a lead whose address nobody held
// created a person outright, even when the workspace already had that human
// under a near-identical name.
func TestPromotingALeadRunsTheFullPersonLadder(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asLeadOwner()

	incumbent, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: "Jonathan Meier", Source: "manual",
		Emails: []PersonEmailInput{{Email: "j.meier@acme.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A different address, so the exact tier stays silent; the name is what
	// collides.
	lead, _, err := e.store.CreateLead(ctx, CreateLeadInput{
		FullName: ptr("Jonathan Meier"),
		Email:    ptr("jonathan.meier@acme.test"),
		Source:   "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	leadID := ids.From[ids.LeadKind](ids.UUID(lead.Id))
	person, merged, err := e.store.PromoteLead(ctx, leadID, PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil {
		t.Fatalf("promote lead: %v", err)
	}
	if merged {
		t.Fatal("promotion merged on a fuzzy name match — DEDUPE_FUZZY_AUTOMERGE is pinned never")
	}
	if person.Id == incumbent.Id {
		t.Fatal("promotion landed on the incumbent without an exact key")
	}

	var candidates int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM dedupe_candidate
			 WHERE entity_type = 'person' AND (left_person_id = $1 OR right_person_id = $1)`,
			ids.UUID(person.Id)).Scan(&candidates)
	}); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 {
		t.Fatalf("dedupe candidates for the promoted person = %d, want 1 — "+
			"a promotion that creates a near-twin must leave the pair on the queue", candidates)
	}
}

// TestPromotingALeadStillMergesOnAClaimedAddress pins the behaviour the
// rewrite had to preserve: an exact email hit merges into the incumbent rather
// than creating anything.
func TestPromotingALeadStillMergesOnAClaimedAddress(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asLeadOwner()
	incumbent, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: "Dana Fischer", Source: "manual",
		Emails: []PersonEmailInput{{Email: "dana@globex.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lead, _, err := e.store.CreateLead(ctx, CreateLeadInput{
		FullName: ptr("Dana Fischer"), Email: ptr("dana@globex.test"), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	person, merged, err := e.store.PromoteLead(ctx,
		ids.From[ids.LeadKind](ids.UUID(lead.Id)), PromoteLeadInput{Trigger: "human_qualify"})
	if err != nil {
		t.Fatalf("promote lead: %v", err)
	}
	if !merged {
		t.Fatal("promotion did not merge onto the person already holding the address")
	}
	if person.Id != incumbent.Id {
		t.Fatalf("merged into %s, want the incumbent %s", person.Id, incumbent.Id)
	}
}

// TestASecondLeadCannotClaimALinkedInProfile closes the key that had a probe
// nobody called: one profile URL names one human, so the second create is
// refused with the incumbent's id exactly as a claimed address is.
func TestASecondLeadCannotClaimALinkedInProfile(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asLeadOwner()
	first, _, err := e.store.CreateLead(ctx, CreateLeadInput{
		FullName:    ptr("Vera Vogel"),
		Email:       ptr("vera@vogel.test"),
		LinkedInURL: ptr("https://www.linkedin.com/in/vera-vogel"),
		Source:      "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A different address, and the same profile written with the other scheme,
	// an explicit port and a fragment — all noise the key normalizes away.
	_, _, err = e.store.CreateLead(ctx, CreateLeadInput{
		FullName:    ptr("V. Vogel"),
		Email:       ptr("v.vogel@other.test"),
		LinkedInURL: ptr("http://www.linkedin.com:443/in/vera-vogel#about"),
		Source:      "manual",
	})
	var dup *DuplicateLeadLinkedInError
	if !errors.As(err, &dup) {
		t.Fatalf("second create error = %v, want DuplicateLeadLinkedInError", err)
	}
	if dup.ExistingID.UUID != ids.UUID(first.Id) {
		t.Fatalf("conflict names %s, want the incumbent %s", dup.ExistingID, first.Id)
	}
}

// TestTheChokepointRefusesToMintOverAnExactCollision is the guard that makes
// the verdict argument load-bearing rather than decorative. No production
// caller reaches it — each returns on the exact tier first — so it is asserted
// directly: a future path that forgets is refused by the chokepoint itself.
func TestTheChokepointRefusesToMintOverAnExactCollision(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := createPerson(ctx, tx, PersonResolution{
			Decision: DecisionExactCollision, MatchedLane: laneEmail, PersonID: ids.New[ids.PersonKind](),
		}, PersonSpec{FullName: "Ghost", Source: "manual", CapturedBy: "human:test"})
		return err
	})
	if err == nil {
		t.Fatal("createPerson minted a person while the ladder held an exact email collision")
	}

	err = e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := createOrganization(ctx, tx, OrganizationMatch{
			Decision: DecisionExactCollision, OrganizationID: ids.New[ids.OrganizationKind](),
		}, OrgSpec{DisplayName: "Ghost Co", Source: "manual", CapturedBy: "human:test"})
		return err
	})
	if err == nil {
		t.Fatal("createOrganization minted an organization while the ladder held an exact domain collision")
	}
}
