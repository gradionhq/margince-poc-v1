// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The company page's one-line description: written on create, edited in
// place, cleared to absent, and inherited by a merge survivor that has none.
// A column the page renders unconditionally has to survive every one of those
// paths, not only the create.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestOrganizationDescriptionRoundTripsThroughCreateAndUpdate(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	written := "Supplies architectural glazing and modular walls to builders."
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Glazed Frog GmbH", Source: "manual",
		Description: &written,
	})
	if err != nil {
		t.Fatal(err)
	}
	if org.Description == nil || *org.Description != written {
		t.Fatalf("create returned description %v, want %q", org.Description, written)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	// A re-read proves the value came back from the column, not from the
	// input struct the create path happened to be holding.
	reread, err := e.store.GetOrganization(ctx, orgID, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Description == nil || *reread.Description != written {
		t.Fatalf("re-read description = %v, want %q", reread.Description, written)
	}

	edited := "Supplies architectural glazing to builders and architects."
	updated, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{
		Description: &edited,
	})
	if err != nil {
		t.Fatalf("edit description: %v", err)
	}
	if updated.Description == nil || *updated.Description != edited {
		t.Fatalf("edited description = %v, want %q", updated.Description, edited)
	}

	// An empty string is the clear, the same spelling every other nullable
	// text column on this record uses.
	empty := ""
	cleared, err := e.store.UpdateOrganization(ctx, orgID, UpdateOrganizationInput{
		Description: &empty,
	})
	if err != nil {
		t.Fatalf("clear description: %v", err)
	}
	if cleared.Description != nil && *cleared.Description != "" {
		t.Fatalf("cleared description = %v, want empty or nil", cleared.Description)
	}
}

// A nil Description leaves the column untouched, so an edit that only moves
// the lifecycle cannot silently wipe the line someone wrote.
func TestOrganizationDescriptionSurvivesAnUnrelatedEdit(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	written := "Runs the regional glazing supply network."
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Untouched GmbH", Source: "manual", Description: &written,
	})
	if err != nil {
		t.Fatal(err)
	}
	industry := "Building products"
	updated, err := e.store.UpdateOrganization(ctx,
		ids.From[ids.OrganizationKind](ids.UUID(org.Id)),
		UpdateOrganizationInput{Industry: &industry})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description == nil || *updated.Description != written {
		t.Fatalf("description after an unrelated edit = %v, want %q", updated.Description, written)
	}
}

// The survivorship rule is fill-where-blank: a survivor with no description
// inherits one, a survivor that already has one keeps its own.
func TestOrganizationDescriptionFillsOnlyABlankMergeSurvivor(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	retiredLine := "Fabricates aluminium window systems."

	blankSurvivor, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Blank Survivor GmbH", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	retired, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Retired One GmbH", Source: "manual", Description: &retiredLine,
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := e.store.MergeOrganization(ctx,
		ids.From[ids.OrganizationKind](ids.UUID(retired.Id)),
		ids.From[ids.OrganizationKind](ids.UUID(blankSurvivor.Id)))
	if err != nil {
		t.Fatalf("merge into blank survivor: %v", err)
	}
	if merged.Description == nil || *merged.Description != retiredLine {
		t.Fatalf("blank survivor description = %v, want the retired record's %q", merged.Description, retiredLine)
	}

	ownLine := "Installs curtain walling on commercial builds."
	heldSurvivor, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Held Survivor GmbH", Source: "manual", Description: &ownLine,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRetired, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Retired Two GmbH", Source: "manual", Description: &retiredLine,
	})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := e.store.MergeOrganization(ctx,
		ids.From[ids.OrganizationKind](ids.UUID(secondRetired.Id)),
		ids.From[ids.OrganizationKind](ids.UUID(heldSurvivor.Id)))
	if err != nil {
		t.Fatalf("merge into held survivor: %v", err)
	}
	if kept.Description == nil || *kept.Description != ownLine {
		t.Fatalf("held survivor description = %v, want its own %q", kept.Description, ownLine)
	}
}

// The 500-character bound is the database's, so it holds for every writer and
// not only the ones that remember to check.
func TestOrganizationDescriptionRefusesAPastedParagraph(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	tooLong := strings.Repeat("a", 501)
	if _, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Overlong GmbH", Source: "manual", Description: &tooLong,
	}); err == nil {
		t.Fatal("a 501-character description was accepted; the column CHECK must refuse it")
	}
}
