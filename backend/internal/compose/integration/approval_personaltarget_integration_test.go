// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A saved view is per-user state: its store serves it back on `id AND owner_id`
// and refuses every other seat, admin included. So the approval that stages an
// archive of one is decidable by its OWNER and by nobody else.
//
// This is the case that decides which probe the target arm may use. The row-scope
// clause every other owner-carrying table takes admits own/team/AND ALL — so an
// arm built on it would list a colleague's private view, its name and the change
// staged against it, in a teammate's and an admin's inbox, and let either of them
// decide a write the API refuses them the row itself for. The approval surface
// may never disclose more than the record would.
//
// It runs at the store seam rather than over HTTP because that is where this
// package can bind a SECOND real seat: the migrated-database harness seeds three
// humans across two teams, while the HTTP harness bootstraps exactly one admin.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestAStagedViewArchiveIsDecidableByItsOwnerAlone(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	views := collections.NewStore(e.Pool)

	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())
	view, err := views.CreateSavedView(owner, collections.CreateSavedViewInput{
		Resource: "people", Name: "My pipeline", Query: map[string]any{"columns": []any{"full_name"}},
	})
	if err != nil {
		t.Fatalf("create saved view: %v", err)
	}
	approvalID := stageFor(t, svc, e, "archive_record", "saved_view", view.ID.UUID)

	// Two seats holding every saved_view grant that would BOTH have passed a
	// row-scope arm: a teammate (rep1 and rep2 share team1) and an all-scope
	// admin, for whom the clause renders no predicate at all.
	allScope := collectionsPerms()
	allScope.RowScope = principal.RowScopeAll
	assertCannotDecideStagedApproval(e.As(e.Rep2, []ids.UUID{e.Team1}, collectionsPerms()),
		t, svc, "a teammate", approvalID)
	assertCannotDecideStagedApproval(e.As(e.Rep3, []ids.UUID{e.Team2}, allScope),
		t, svc, "an all-scope admin", approvalID)

	// The owner sees it and can decide it — the gate narrows to one seat, it does
	// not strand the row.
	pending, _, err := svc.List(owner, approvals.ListInput{Status: strPtr("pending"), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, a := range pending {
		if a.ID == approvalID {
			listed = true
		}
	}
	if !listed {
		t.Error("the view's own owner cannot see the archive staged against it — the row is stranded")
	}
	if _, err := svc.Get(owner, approvalID); err != nil {
		t.Errorf("owner Get → %v, want ok", err)
	}
	if _, err := svc.Decide(owner, approvalID, false, strPtr("keeping it")); err != nil {
		t.Errorf("owner reject → %v, want ok — seeing it and deciding it are one predicate", err)
	}
}

// assertCannotDecideStagedApproval holds one seat that could not read the staged
// target to the full existence-hiding contract: the row is absent from their
// inbox, and absent — not forbidden — from the single read and from BOTH
// directions of the decision. A reject is a decision too, so an approval a seat
// cannot see is one they cannot dismiss either.
func assertCannotDecideStagedApproval(ctx context.Context, t *testing.T, svc *approvals.Service, who string, approvalID ids.ApprovalID) {
	t.Helper()
	pending, _, err := svc.List(ctx, approvals.ListInput{Status: strPtr("pending"), Limit: 50})
	if err != nil {
		t.Fatalf("%s list: %v", who, err)
	}
	for _, a := range pending {
		if a.ID == approvalID {
			t.Errorf("%s sees a staged approval whose target their own read path refuses them", who)
		}
	}
	if _, err := svc.Get(ctx, approvalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("%s Get → %v, want ErrNotFound", who, err)
	}
	if _, err := svc.Decide(ctx, approvalID, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("%s approve → %v, want ErrNotFound", who, err)
	}
	if _, err := svc.Decide(ctx, approvalID, false, strPtr("no")); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("%s reject → %v, want ErrNotFound", who, err)
	}
}
