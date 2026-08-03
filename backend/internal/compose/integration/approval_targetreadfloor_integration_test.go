// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The object-READ half of a staged target's read rule, over a real record.
//
// A role document granting `tag.delete` with `tag.read` false is a valid
// document: the four CRUD booleans are independent and merge independently. Such
// a seat holds exactly the grant archive_record resolves from the target's entity
// type, so the decision-grant half admits them — and the target's own store
// refuses them every tag. The inbox is the surface that discloses the most staged
// detail (the summary and the proposed change), so it is the one that may least
// afford to show a record its owning store would not: the approval surface may
// never disclose more than the record itself.
//
// A tag is the sharpest case because it carries no owner_id at all — nothing but
// the object grant governs it — so this is the row-scope-free half of the rule,
// asserted where nothing else can stand in for it.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// tagPerms is one seat's whole capability over the workspace-shared tag
// vocabulary. The widest row scope is bound deliberately: tags have no owner
// column, so the row tier decides nothing here and the object grant is the only
// thing that can answer.
func tagPerms(g principal.ObjectGrant) principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"custom"},
		Objects:  map[string]principal.ObjectGrant{"tag": g},
		RowScope: principal.RowScopeAll,
	}
}

func TestAStagedTagArchiveNeedsTagReadAndNotOnlyTagDelete(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	tags := collections.NewStore(e.Pool)

	author := e.As(e.Rep1, []ids.UUID{e.Team1},
		tagPerms(principal.ObjectGrant{Create: true, Read: true, Delete: true}))
	tag, err := tags.CreateTag(author, "Champion", nil)
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	approvalID := stageFor(t, svc, e, "archive_record", "tag", tag.ID.UUID)

	// The seat the rule is about: it satisfies archive_record's decision grant and
	// holds no read. Its own tag read is refused…
	deleter := e.As(e.Rep2, []ids.UUID{e.Team1}, tagPerms(principal.ObjectGrant{Delete: true}))
	if _, _, err := tags.ListTags(deleter, storekit.LiveOnly); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("tag list without tag.read → %v, want ErrPermissionDenied — the case only means something if "+
			"the direct read really refuses this seat", err)
	}
	// …so the inbox must not hand them the same tag through the staged change
	// instead, and must not let them decide it in either direction.
	assertCannotDecideStagedApproval(deleter, t, svc, "a seat holding tag.delete and not tag.read", approvalID)

	// The floor narrows the surface; it does not strand the row. A seat holding
	// both grants sees the staging and decides it.
	decider := e.As(e.Rep3, []ids.UUID{e.Team2}, tagPerms(principal.ObjectGrant{Read: true, Delete: true}))
	pending, _, err := svc.List(decider, approvals.ListInput{Status: strPtr("pending"), Limit: 50})
	if err != nil {
		t.Fatalf("decider list: %v", err)
	}
	listed := false
	for _, a := range pending {
		if a.ID == approvalID {
			listed = true
		}
	}
	if !listed {
		t.Error("a seat holding tag.read AND tag.delete cannot see the archive staged against a live tag — the " +
			"row is stranded where nobody can release or reject it")
	}
	if _, err := svc.Get(decider, approvalID); err != nil {
		t.Errorf("decider Get → %v, want ok", err)
	}
	if _, err := svc.Decide(decider, approvalID, false, strPtr("keeping it")); err != nil {
		t.Errorf("decider reject → %v, want ok — seeing it and deciding it are one predicate", err)
	}
}
