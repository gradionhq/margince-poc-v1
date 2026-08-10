// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// A field the contract declares REQUIRED is answered, or the API is lying in a
// way no caller can detect.
//
// `workspace_id` was declared required and never mapped, so every approval this
// module has ever returned carried the zero uuid — a well-formed value in the
// right place that happens to name no workspace. Nothing failed: not the
// contract gate, which checks shape rather than content; not the handler tests,
// which assert the fields they were written to assert; and not any client,
// because a uuid of zeroes deserializes perfectly.
//
// So the obligation is derived from the contract type itself rather than kept
// as a list of fields somebody remembers to extend. A REQUIRED field in OpenAPI
// 3.1 generates as a NON-POINTER on the Go struct, and an optional one as a
// pointer — which makes "required" mechanically readable here, and makes a
// field the contract promotes to required tomorrow covered on the day the
// generator runs.

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The floor that keeps this from certifying nothing: Approval really does
// declare several required fields, and a reflection walk that suddenly finds
// none has broken rather than been satisfied.
const requiredApprovalFieldFloor = 5

func TestEveryRequiredApprovalFieldIsAnswered(t *testing.T) {
	// A fixed instant, through the fixture and the mapper alike: nothing here
	// asks what time it is, and a test that reads the wall clock can only
	// answer differently on a slow machine.
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	source := fullyPopulatedRow(t, now)
	wired := reflect.ValueOf(wire(source, now))

	// Non-zero is not enough for the two uuid-valued required fields, because
	// they are the pair a mapper can confuse: WorkspaceId taken from the
	// approval's own id would satisfy every zero-check here and name the wrong
	// thing everywhere. So these two are checked by VALUE, against the row they
	// came from.
	if got, want := wired.Interface().(crmcontracts.Approval).WorkspaceId, openapi_types.UUID(source.WorkspaceID); got != want {
		t.Errorf("Approval.WorkspaceId = %s, want the row's own workspace %s", got, want)
	}
	if got, want := wired.Interface().(crmcontracts.Approval).Id, openapi_types.UUID(source.ID.UUID); got != want {
		t.Errorf("Approval.Id = %s, want the row's own id %s", got, want)
	}

	required := 0
	for i := range wired.NumField() {
		field := wired.Type().Field(i)
		// A pointer field is an OPTIONAL one, and nil is a legitimate answer for
		// it — a pending approval has no decided_by. Only the required fields
		// are the subject here.
		if field.Type.Kind() == reflect.Pointer {
			continue
		}
		required++
		if !wired.Field(i).IsZero() {
			continue
		}
		t.Errorf("Approval.%s is declared required by the contract and comes back as its zero "+
			"value from a row where every column is set. Map it in wire(), and read its column "+
			"in the store if it is not there yet — a required field nobody populates is a "+
			"well-formed answer that names nothing, which no client can tell from a real one.",
			field.Name)
	}
	if required < requiredApprovalFieldFloor {
		t.Fatalf("found %d required fields on crmcontracts.Approval, expected at least %d — the "+
			"reflection walk broke rather than the subject", required, requiredApprovalFieldFloor)
	}
}

// fullyPopulatedRow is a store row with every field set to something a real one
// could carry, so a zero on the OTHER side of wire() can only mean the mapper
// dropped it.
func fullyPopulatedRow(t *testing.T, now time.Time) row {
	t.Helper()
	targetType, summary, version := tableOrganization, "Rename Acme GmbH", int64(7)
	targetID, bundleID := ids.NewV7(), ids.NewV7()
	onBehalfOf := ids.New[ids.UserKind]()
	passportID := ids.New[ids.PassportKind]()
	decidedBy := ids.New[ids.UserKind]()
	decidedAt := now.Add(-time.Minute)
	consumedAt := now
	change, err := json.Marshal(map[string]string{"display_name": "Acme GmbH"})
	if err != nil {
		t.Fatalf("building the proposed change: %v", err)
	}
	return row{
		ID:             ids.New[ids.ApprovalKind](),
		WorkspaceID:    ids.NewV7(),
		Kind:           "orgname",
		Status:         statusPending,
		ProposedBy:     "agent:test",
		OnBehalfOf:     &onBehalfOf,
		PassportID:     &passportID,
		TargetType:     &targetType,
		TargetID:       &targetID,
		TargetVersion:  &version,
		Summary:        &summary,
		ProposedChange: change,
		DiffHash:       "a-diff-hash",
		ExpiresAt:      now.Add(time.Hour),
		DecidedBy:      &decidedBy,
		DecidedAt:      &decidedAt,
		ConsumedAt:     &consumedAt,
		CreatedAt:      now.Add(-time.Hour),
		BundleID:       &bundleID,
	}
}
