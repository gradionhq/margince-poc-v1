// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The remaining four commands' resolvers (commandaction.go): retire and
// update-options target `custom_field`, a type the record seam has never
// served, so they stage OUTSIDE it the same way an archive of a
// record-seam-unserved type does; set/remove stakeholder target `project`,
// which the seam serves like any other record, so they refuse the same two
// ways patchResolver's own target does.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// retire and update-options stage OUTSIDE the record seam — the id alone
// names the target, the same shape TestArchiveStagesATypeTheRecordSeamDoesNotServe
// proves for an archive of a record-seam-unserved type. Staged against a
// provider that fails EVERY read, so a resolver that consulted the seam
// anyway fails here rather than passing on a lenient stub.
func TestCustomFieldCommandsStageOutsideTheRecordSeam(t *testing.T) {
	id := ids.NewV7()
	cases := []struct {
		name        string
		call        GovernedCall
		wantOperand string
	}{
		{"retire", NewRetireCustomFieldCall(unreadableProvider{}, RetireCustomFieldCommand{ID: id}), id.String()},
		{
			"update_options",
			NewUpdateCustomFieldOptionsCall(unreadableProvider{}, UpdateCustomFieldOptionsCommand{ID: id, Fields: json.RawMessage(`{"options":["gold","silver"]}`)}),
			"gold",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := StageSubject(context.Background(), c.call)
			if err != nil {
				t.Fatalf("staging outside the record seam answered %v, want it staged", err)
			}
			if info.TargetType != "custom_field" || info.TargetID != id {
				t.Errorf("staged target = (%s,%s), want (custom_field,%s)", info.TargetType, info.TargetID, id)
			}
			if !strings.Contains(info.Summary, c.wantOperand) {
				t.Errorf("summary %q does not name %q", info.Summary, c.wantOperand)
			}
		})
	}
}

// An unparseable or empty options body still leaves the id naming the
// approval — Guards never depends on the decode, only the summary's
// richness does.
func TestUpdateOptionsSummaryFallsBackOnAnUnparseableBody(t *testing.T) {
	id := ids.NewV7()
	call := NewUpdateCustomFieldOptionsCall(unreadableProvider{}, UpdateCustomFieldOptionsCommand{ID: id, Fields: json.RawMessage(`not json`)})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging answered %v, want it staged despite the unparseable body", err)
	}
	if !strings.Contains(info.Summary, id.String()) {
		t.Errorf("summary %q does not fall back to naming the id", info.Summary)
	}
}

// setStakeholder and removeStakeholder stage against the PROJECT, naming it
// in words, with the operand — who is being attached or detached — carried
// into the summary.
func TestStakeholderCommandsStageTheProjectWithTheOperandInTheSummary(t *testing.T) {
	projectID := ids.NewV7()
	personID := ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityProject, projectID, true)}
	cases := []struct {
		name        string
		call        GovernedCall
		wantOperand string
	}{
		{
			"set",
			NewSetStakeholderCall(provider, SetStakeholderCommand{
				ID: projectID, Fields: json.RawMessage(`{"person_id":"` + personID.String() + `","role":"champion"}`),
			}),
			personID.String(),
		},
		{
			"remove",
			NewRemoveStakeholderCall(provider, RemoveStakeholderCommand{ID: projectID, PersonID: personID}),
			personID.String(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := StageSubject(context.Background(), c.call)
			if err != nil {
				t.Fatalf("staging answered %v, want it staged", err)
			}
			if info.TargetType != "project" || info.TargetID != projectID {
				t.Errorf("staged target = (%s,%s), want (project,%s)", info.TargetType, info.TargetID, projectID)
			}
			if !strings.Contains(info.Summary, c.wantOperand) {
				t.Errorf("summary %q does not name the person %q — two stakeholder changes on the same "+
					"project must not render as the same inbox line", info.Summary, c.wantOperand)
			}
			if !strings.Contains(info.Summary, "Acme") {
				t.Errorf("summary %q does not name the project", info.Summary)
			}
		})
	}
}

// A project the caller cannot see is refused before anything is staged, for
// both stakeholder commands.
func TestStakeholderCommandsRefuseAnUnreadableProject(t *testing.T) {
	id, personID := ids.NewV7(), ids.NewV7()
	cases := []struct {
		name string
		call GovernedCall
	}{
		{"set", NewSetStakeholderCall(unreadableProvider{}, SetStakeholderCommand{ID: id, Fields: json.RawMessage(`{"person_id":"x","role":"champion"}`)})},
		{"remove", NewRemoveStakeholderCall(unreadableProvider{}, RemoveStakeholderCommand{ID: id, PersonID: personID})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call.Guards(context.Background()); !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("guarding an unreadable project answered %v, want the row-scope miss", err)
			}
		})
	}
}

// A project held in another system of record is refused too.
func TestStakeholderCommandsRefuseAProjectHeldElsewhere(t *testing.T) {
	id, personID := ids.NewV7(), ids.NewV7()
	cases := []struct {
		name string
		call GovernedCall
	}{
		{"set", NewSetStakeholderCall(elsewhereProvider{}, SetStakeholderCommand{ID: id, Fields: json.RawMessage(`{"person_id":"x","role":"champion"}`)})},
		{"remove", NewRemoveStakeholderCall(elsewhereProvider{}, RemoveStakeholderCommand{ID: id, PersonID: personID})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call.Guards(context.Background()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
				t.Errorf("guarding a mirrored project answered %v, want the unsupported-by-SoR refusal", err)
			}
		})
	}
}

// Guards never depends on retire/options' body or on whether the named
// person is currently a stakeholder — only the routed record's own
// visibility and system of record. custom_field's Guards is proven to stand
// down entirely by TestCustomFieldCommandsStageOutsideTheRecordSeam already;
// this proves retire's bare id (no body at all) hits the same stand-down.
func TestRetireCustomFieldStagesWithNoBody(t *testing.T) {
	id := ids.NewV7()
	info, err := StageSubject(context.Background(), NewRetireCustomFieldCall(unreadableProvider{}, RetireCustomFieldCommand{ID: id}))
	if err != nil {
		t.Fatalf("staging a retire answered %v, want it staged", err)
	}
	if info.TargetType != "custom_field" || info.TargetID != id {
		t.Errorf("staged target = (%s,%s), want (custom_field,%s)", info.TargetType, info.TargetID, id)
	}
}
