// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Lifecycle and relationship types (ADR-0079/A124): the replace-set rides the
// organization's own write shape exactly as the domain set does, and the
// partner invariant — an org IS a partner iff it has the extension row AND a
// live 'partner' type — is enforced in both directions rather than described.
//
// ADR-0032 bound partnerhood to a classification value and nothing enforced
// it, so a plain patch could take the label off a company the partner APIs
// still returned. That is the hole these tests close.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asAdmin widens the shared dedupe principal to the two verbs this file needs
// beyond the base rep: promoting a partner and archiving an account. The
// harness's default grant set is deliberately narrow, so a test that needs
// more asks for it rather than the harness handing every test everything.
func asAdmin(e *dedupeEnv) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"person":       {Create: true, Read: true, Update: true, Delete: true},
				"organization": {Create: true, Read: true, Update: true, Delete: true},
				"relationship": {Create: true, Read: true},
				"partner":      {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

func liveTypesOf(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		types, err := readLiveRelationshipTypes(ctx, tx, orgID)
		if err != nil {
			return err
		}
		for _, ty := range types {
			out[ty] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOrganizationStartsAtUnknownRatherThanClaimingToBeAProspect(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Unassessed GmbH", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The retired classification defaulted to 'prospect' and had no writer, so
	// every untouched account rendered that default as though someone had
	// judged it. 'unknown' is the honest answer to a question nobody asked.
	if org.Lifecycle == nil || *org.Lifecycle != crmcontracts.OrganizationLifecycleUnknown {
		t.Errorf("lifecycle at birth = %v, want unknown", org.Lifecycle)
	}
	if org.RelationshipTypes == nil || len(*org.RelationshipTypes) != 0 {
		t.Errorf("relationship types at birth = %v, want an empty set", org.RelationshipTypes)
	}
}

func TestUpdateOrganizationRelationshipTypesReplaceSet(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Scale Commerce GmbH", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	lifecycle := string(crmcontracts.OrganizationLifecycleFormerCustomer)
	types := []string{"customer", "supplier"}
	updated, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{
		Lifecycle:         &lifecycle,
		RelationshipTypes: &types,
	})
	if err != nil {
		t.Fatalf("first replace-set: %v", err)
	}
	// The account the whole arc is named for: its contract ended AND it is
	// still several things to us. The retired enum could hold one of these.
	if updated.Lifecycle == nil || *updated.Lifecycle != crmcontracts.OrganizationLifecycleFormerCustomer {
		t.Errorf("lifecycle = %v, want former_customer", updated.Lifecycle)
	}
	if live := liveTypesOf(ctx, t, e, orgID); !live["customer"] || !live["supplier"] || len(live) != 2 {
		t.Fatalf("types after first set = %+v, want {customer, supplier}", live)
	}

	// Replace-set: supplier goes, competitor arrives, customer is untouched.
	next := []string{"customer", "competitor"}
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{RelationshipTypes: &next}); err != nil {
		t.Fatalf("second replace-set: %v", err)
	}
	live := liveTypesOf(ctx, t, e, orgID)
	if !live["customer"] || !live["competitor"] || live["supplier"] || len(live) != 2 {
		t.Fatalf("types after second set = %+v, want {customer, competitor}", live)
	}

	// An empty set clears them; nothing here is a partner, so nothing refuses.
	empty := []string{}
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{RelationshipTypes: &empty}); err != nil {
		t.Fatalf("clearing set: %v", err)
	}
	if live := liveTypesOf(ctx, t, e, orgID); len(live) != 0 {
		t.Fatalf("types after clear = %+v, want none", live)
	}
}

func TestRelationshipTypesRefuseAValueOutsideTheVocabulary(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Vocabulary GmbH", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	bad := []string{"customer", "frenemy"}
	// Refused in Go, so the caller gets a 422 naming the field rather than a
	// CHECK violation surfacing as a 500.
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{RelationshipTypes: &bad}); err == nil {
		t.Fatal("a value outside the vocabulary was accepted")
	}
	if live := liveTypesOf(ctx, t, e, orgID); len(live) != 0 {
		t.Fatalf("a refused set still wrote %+v — the whole set must land or none of it", live)
	}
}

func TestPartnerPromotionWritesTheTypeAndThePatchCannotTakeItAway(t *testing.T) {
	e := setupDedupe(t)
	ctx := asAdmin(e)
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Channel Partner GmbH", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	if _, err := e.store.UpsertPartner(ctx, UpsertPartnerInput{
		OrganizationID: orgID, PartnerRole: "hosting",
	}); err != nil {
		t.Fatalf("promote to partner: %v", err)
	}
	// Half the invariant: the extension row implies the type, written in the
	// same transaction so the two can never disagree.
	if live := liveTypesOf(ctx, t, e, orgID); !live["partner"] {
		t.Fatalf("types after promotion = %+v, want partner among them", live)
	}

	// The other half: the patch cannot strip the type while the programme
	// exists. Under ADR-0032 this was a comment, and a plain patch could take
	// the label off a company listPartners still returned.
	without := []string{"customer"}
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{RelationshipTypes: &without}); err == nil {
		t.Fatal("dropping the partner type while the partner row lives was accepted")
	}
	if live := liveTypesOf(ctx, t, e, orgID); !live["partner"] {
		t.Fatalf("types after the refused patch = %+v — the refusal must change nothing", live)
	}
}

func TestArchivingAnAccountRetiresItsRelationshipTypes(t *testing.T) {
	e := setupDedupe(t)
	ctx := asAdmin(e)
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Gone Away GmbH", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))
	types := []string{"customer"}
	if _, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{RelationshipTypes: &types}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.store.ArchiveOrganization(ctx, orgID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// An archived account holding a live type row would keep answering the
	// filters, which read live rows only.
	if live := liveTypesOf(ctx, t, e, orgID); len(live) != 0 {
		t.Fatalf("types after archive = %+v, want none — they retire with their parent", live)
	}
}
