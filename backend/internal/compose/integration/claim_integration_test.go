// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A record somebody creates by hand is theirs: the create stamps the caller
// as owner when none is named. An ownerless row — a connector's, or one whose
// owner left — is every seat's to SEE and nobody's to CHANGE until a seat
// claims it; a row owned by a colleague is not claimable without write
// authority. The claim is one audited write, and a lost race answers conflict.
func TestAManualCreateIsOwnedByItsCreatorAndAnOwnerlessRecordMustBeClaimed(t *testing.T) {
	e := Setup(t)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	stranger := e.As(e.Rep3, []ids.UUID{e.Team2}, activityLifecyclePerms)

	// The create stamps the creator.
	mine, err := e.People.CreatePerson(rep, people.CreatePersonInput{FullName: "Mine Byhand", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if mine.OwnerId == nil || ids.UUID(*mine.OwnerId) != e.Rep1 {
		t.Fatalf("a person created by Rep1 without naming an owner is owned by %v, want Rep1", mine.OwnerId)
	}

	// An ownerless row: readable by the stranger, not writable, claimable.
	orphan := e.SeedPerson(t, "Orphan Contact", nil)
	e.WsExec(t, `UPDATE person SET owner_id = NULL WHERE id = $1`, orphan)
	orphanID := ids.From[ids.PersonKind](orphan)
	if _, err := e.People.GetPerson(stranger, orphanID, 0); err != nil {
		t.Fatalf("an ownerless person is not readable by a stranger: %v", err)
	}
	title := "CTO"
	if _, err := e.People.UpdatePerson(stranger, orphanID, people.UpdatePersonInput{Title: &title}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("editing an ownerless person without claiming it → %v, want ErrPermissionDenied", err)
	}
	claim, err := e.People.ClaimRecord(stranger, "person", orphan, nil)
	if err != nil {
		t.Fatalf("claiming an ownerless person: %v", err)
	}
	if claim.OwnerID != e.Rep3 {
		t.Errorf("claimed owner = %v, want Rep3", claim.OwnerID)
	}
	if _, err := e.People.UpdatePerson(stranger, orphanID, people.UpdatePersonInput{Title: &title}); err != nil {
		t.Errorf("editing a person one just claimed → %v, want allowed", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE action = 'assign' AND entity_id = $1`, orphan); n != 1 {
		t.Errorf("%d assign audit rows for the claim, want 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'person.updated' AND envelope->'entity'->>'id' = $1::text`, orphan); n < 1 {
		t.Errorf("no person.updated event for the claim")
	}

	// Claiming again is a no-op; claiming a colleague's record is refused;
	// a write grant makes it claimable (a reassignment to oneself).
	if _, err := e.People.ClaimRecord(stranger, "person", orphan, nil); err != nil {
		t.Errorf("re-claiming one's own record → %v, want a no-op", err)
	}
	if _, err := e.People.ClaimRecord(rep, "person", orphan, nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("claiming a colleague's record → %v, want ErrPermissionDenied", err)
	}
	e.WsExec(t, `INSERT INTO record_grant (record_type, record_id, subject_type, subject_id, access, granted_by)
		VALUES ('person', $1, 'user', $2, 'write', $3)`, orphan, e.Rep1, e.Rep3)
	if _, err := e.People.ClaimRecord(rep, "person", orphan, nil); err != nil {
		t.Errorf("claiming a record one holds a write share on → %v, want allowed", err)
	}

	// An agent cannot claim; a version mismatch is skew; an unknown type is refused.
	if _, err := e.People.ClaimRecord(e.AgentCtx(), "person", orphan, nil); err == nil {
		t.Error("an agent claimed a record")
	}
	stale := int64(1)
	if _, err := e.People.ClaimRecord(rep, "person", orphan, &stale); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Errorf("claim with a stale If-Match → %v, want ErrVersionSkew", err)
	}
	var unknown *people.InvalidRecordTypeError
	if _, err := e.People.ClaimRecord(rep, "project", orphan, nil); !errors.As(err, &unknown) {
		t.Errorf("claiming a project through the people door → %v, want InvalidRecordTypeError", err)
	}
}
