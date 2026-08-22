// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The bulk project owner handover over real rows: which projects move, what
// each move writes, and the write-authority rule that keeps a rep from
// handing over a colleague's project through the bulk door when the single
// update door would have refused it.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// projectOwner reads a project's owner column through the store, as the
// admin — the harness's unbounded seat, so the read itself never filters.
func projectOwner(t *testing.T, e *Env, id ids.ProjectID) *ids.UUID {
	t.Helper()
	p, err := e.Deals.GetProject(e.Admin(), id, storekit.IncludeArchived)
	if err != nil {
		t.Fatalf("read project %s: %v", id, err)
	}
	if p.OwnerId == nil {
		return nil
	}
	owner := ids.UUID(*p.OwnerId)
	return &owner
}

func assertOwner(t *testing.T, e *Env, id ids.ProjectID, want ids.UUID, why string) {
	t.Helper()
	got := projectOwner(t, e, id)
	if got == nil || *got != want {
		t.Errorf("%s: owner = %v, want %s", why, got, want)
	}
}

// Two of Rep1's live projects move; Rep1's archived project and Rep3's
// project are left alone. Each move is its own audit row with the owner_id
// images and its own outbox event, exactly what two single updates write.
func TestTransferProjectOwnershipMovesEveryLiveProjectTheFromOwnerHolds(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	first := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, &e.Rep1)
	second := seedProject(e.Admin(), t, e, "Warehouse rollout", strPtr("WHR"), org, &e.Rep1)
	retired := seedProject(e.Admin(), t, e, "Old intranet", strPtr("INTRA"), org, &e.Rep1)
	foreign := seedProject(e.Admin(), t, e, "Rep3's project", strPtr("R3"), org, &e.Rep3)
	if _, err := e.Deals.ArchiveProject(e.Admin(), retired.ID, nil); err != nil {
		t.Fatalf("archive project: %v", err)
	}

	moved, err := e.Deals.TransferProjectOwnership(e.Admin(), deals.TransferProjectOwnershipInput{
		FromOwnerID: ids.From[ids.UserKind](e.Rep1), ToOwnerID: ids.From[ids.UserKind](e.Rep2),
	})
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if moved != 2 {
		t.Errorf("transferred = %d, want 2 (the two live projects Rep1 owned)", moved)
	}
	assertOwner(t, e, first.ID, e.Rep2, "a live project of the from-owner")
	assertOwner(t, e, second.ID, e.Rep2, "the from-owner's other live project")
	assertOwner(t, e, retired.ID, e.Rep1, "an archived project is skipped")
	assertOwner(t, e, foreign.ID, e.Rep3, "another owner's project is untouched")

	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'project' AND action = 'update'
		    AND before->>'owner_id' = $1 AND after->>'owner_id' = $2`,
		e.Rep1.String(), e.Rep2.String()); n != 2 {
		t.Errorf("audit rows carrying the owner_id move = %d, want 2 — one per moved project", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'project.updated'
		    AND (envelope->'entity'->>'id')::uuid = ANY($1)`,
		[]ids.UUID{first.ID.UUID, second.ID.UUID}); n != 2 {
		t.Errorf("project.updated events = %d, want 2 — one per moved project", n)
	}

	// The move is readable where a single update's would be: the moved
	// project's field history names the owner that was and the owner that is.
	history, err := privacy.ListFieldHistory(e.Admin(), e.DB(), privacy.FieldHistoryFilter{
		EntityType: "project", EntityID: first.ID.UUID, Field: strPtr("owner_id"),
	})
	if err != nil {
		t.Fatalf("field history of a moved project: %v", err)
	}
	if len(history.Entries) != 1 || history.Entries[0].OldValue == nil || history.Entries[0].NewValue == nil ||
		*history.Entries[0].OldValue != e.Rep1.String() || *history.Entries[0].NewValue != e.Rep2.String() {
		t.Errorf("owner_id history = %+v, want one entry %s → %s", history.Entries, e.Rep1, e.Rep2)
	}
}

// A rep moves only the from-owner's projects they hold WRITE authority over.
// Rep3 (team 2) owns two projects; Rep1 (team 1) is handed a `write` share of
// one and a `read` share of the other. Every seat READS every project, so the
// list shows Rep1 both — but the bulk door moves only the one the single
// update door would have let them change.
func TestTransferProjectOwnershipMovesOnlyWhatTheCallerCouldWrite(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	shared := seedProject(e.Admin(), t, e, "Shared for writing", strPtr("SW"), org, &e.Rep3)
	readOnly := seedProject(e.Admin(), t, e, "Shared for reading", strPtr("SR"), org, &e.Rep3)

	granter := e.As(e.Rep3, []ids.UUID{e.Team2}, principal.Permissions{
		Objects:  map[string]principal.ObjectGrant{"project": {Read: true, Update: true}},
		RowScope: principal.RowScopeOwn,
	})
	svc := identity.NewService(e.Pool)
	for _, grant := range []struct {
		project ids.ProjectID
		access  string
	}{{shared.ID, "write"}, {readOnly.ID, "read"}} {
		if _, err := svc.CreateRecordGrant(granter, identity.CreateGrantInput{
			RecordType: "project", RecordID: grant.project.UUID,
			SubjectType: "user", SubjectID: e.Rep1, Access: grant.access,
		}); err != nil {
			t.Fatalf("share project at %s: %v", grant.access, err)
		}
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  map[string]principal.ObjectGrant{"project": {Read: true, Update: true}},
		RowScope: principal.RowScopeTeam,
	})
	moved, err := e.Deals.TransferProjectOwnership(rep, deals.TransferProjectOwnershipInput{
		FromOwnerID: ids.From[ids.UserKind](e.Rep3), ToOwnerID: ids.From[ids.UserKind](e.Rep2),
	})
	if err != nil {
		t.Fatalf("transfer as a rep: %v", err)
	}
	if moved != 1 {
		t.Errorf("transferred = %d, want 1 — only the project shared at write", moved)
	}
	assertOwner(t, e, shared.ID, e.Rep2, "the project shared at write")
	assertOwner(t, e, readOnly.ID, e.Rep3, "a read share is not a licence to hand the project over")
}

// The receiving user must be able to own a project: a deactivated member
// and the from-owner themselves are both refused as 422 field faults, and
// nothing moves.
func TestTransferProjectOwnershipRefusesAReceiverWhoCannotOwn(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	project := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, &e.Rep1)

	admin := identity.Identity{
		UserID: ids.From[ids.UserKind](e.AdminUser), WorkspaceID: ids.From[ids.WorkspaceKind](e.WS),
		Roles: []string{"admin"},
	}
	if err := identity.NewService(e.Pool).DeactivateUser(e.Admin(), admin,
		identity.DeactivateUserInput{UserID: ids.From[ids.UserKind](e.Rep3)}); err != nil {
		t.Fatalf("deactivate Rep3: %v", err)
	}

	var notActive *deals.OwnerNotActiveError
	_, err := e.Deals.TransferProjectOwnership(e.Admin(), deals.TransferProjectOwnershipInput{
		FromOwnerID: ids.From[ids.UserKind](e.Rep1), ToOwnerID: ids.From[ids.UserKind](e.Rep3),
	})
	if !errors.As(err, &notActive) {
		t.Errorf("transfer to a deactivated user → %v, want OwnerNotActiveError", err)
	}

	var sameOwner *deals.SameOwnerError
	_, err = e.Deals.TransferProjectOwnership(e.Admin(), deals.TransferProjectOwnershipInput{
		FromOwnerID: ids.From[ids.UserKind](e.Rep1), ToOwnerID: ids.From[ids.UserKind](e.Rep1),
	})
	if !errors.As(err, &sameOwner) {
		t.Errorf("transfer to the same user → %v, want SameOwnerError", err)
	}
	assertOwner(t, e, project.ID, e.Rep1, "a refused transfer moves nothing")
}
