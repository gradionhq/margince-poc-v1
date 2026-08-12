// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The remaining four commands' resolvers (commandaction.go): retire and
// update-options target `custom_field`, a type the record seam has never
// served, so their Guards always stands down; set/remove stakeholder target
// `project`, which the seam serves like any other record, so they refuse
// the same two ways patchResolver's own target does.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// retire and update-options stage OUTSIDE the record seam — the id alone
// names the target, the same shape TestArchiveStagesATypeTheRecordSeamDoesNotServe
// proves for an archive of a record-seam-unserved type. Guards is asked
// against a provider that fails EVERY read, so a resolver that consulted the
// seam anyway fails here rather than passing on a lenient stub.
func TestCustomFieldCommandsStageAndAdmitOutsideTheRecordSeam(t *testing.T) {
	id := ids.NewV7()
	cases := []struct {
		name string
		call GovernedCall
	}{
		{"retire", NewRetireCustomFieldCall(unreadableProvider{}, RetireCustomFieldCommand{ID: id})},
		{"update_options", NewUpdateCustomFieldOptionsCall(unreadableProvider{}, UpdateCustomFieldOptionsCommand{ID: id})},
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
			if !strings.Contains(info.Summary, id.String()) {
				t.Errorf("summary %q does not name the id — the seam has no better label to give it", info.Summary)
			}
		})
	}
}

// setStakeholder and removeStakeholder stage against the PROJECT. Only
// removeStakeholder carries a path operand (PersonID) into the summary —
// setStakeholder's person/role arrive in the body, which the inbox shows
// beside the summary line (proposed_change), the same reasoning patchResolver
// gives for not repeating a patch's values.
func TestStakeholderCommandsStageTheProject(t *testing.T) {
	projectID := ids.NewV7()
	personID := ids.NewV7()
	// project IS served (unlike custom_field), so staging it needs a readable
	// provider — an unreadable one would fail at Guards before Subject ever ran.
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityProject, projectID, true)}

	setInfo, err := StageSubject(context.Background(), NewSetStakeholderCall(provider, SetStakeholderCommand{ID: projectID}))
	if err != nil {
		t.Fatalf("staging a set-stakeholder answered %v, want it staged", err)
	}
	if setInfo.TargetType != "project" || setInfo.TargetID != projectID {
		t.Errorf("staged target = (%s,%s), want (project,%s)", setInfo.TargetType, setInfo.TargetID, projectID)
	}

	removeInfo, err := StageSubject(context.Background(),
		NewRemoveStakeholderCall(provider, RemoveStakeholderCommand{ID: projectID, PersonID: personID}))
	if err != nil {
		t.Fatalf("staging a remove-stakeholder answered %v, want it staged", err)
	}
	if removeInfo.TargetType != "project" || removeInfo.TargetID != projectID {
		t.Errorf("staged target = (%s,%s), want (project,%s)", removeInfo.TargetType, removeInfo.TargetID, projectID)
	}
	if !strings.Contains(removeInfo.Summary, personID.String()) {
		t.Errorf("remove-stakeholder summary %q does not name the person being detached — two detaches from "+
			"the same project must not render as the same inbox line", removeInfo.Summary)
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
		{"set", NewSetStakeholderCall(unreadableProvider{}, SetStakeholderCommand{ID: id})},
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
		{"set", NewSetStakeholderCall(elsewhereProvider{}, SetStakeholderCommand{ID: id})},
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

// A served, readable project is admitted rather than refused.
func TestStakeholderCommandsAdmitAReadableProject(t *testing.T) {
	id, personID := ids.NewV7(), ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityProject, id, true)}
	if err := NewSetStakeholderCall(provider, SetStakeholderCommand{ID: id}).Guards(context.Background()); err != nil {
		t.Fatalf("guarding a readable, authoritative project (set) answered %v, want it admitted", err)
	}
	if err := NewRemoveStakeholderCall(provider, RemoveStakeholderCommand{ID: id, PersonID: personID}).Guards(context.Background()); err != nil {
		t.Fatalf("guarding a readable, authoritative project (remove) answered %v, want it admitted", err)
	}
}
