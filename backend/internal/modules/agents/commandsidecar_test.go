// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The organization-sidecar resolvers (commandsidecar.go): the approval binds
// to the organization, refuses the same two ways patchResolver's own target
// does, and the summary names the operand — the fact key or the profile
// field — so two of either on the same organization never render as the
// same inbox line.

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

// Each sidecar command stages against the ORGANIZATION it routes through,
// naming it in words (recordLabel), with the operand carried into the
// summary — the property that keeps two facts, or two profile fields, on
// one organization from rendering as one indistinguishable approval.
func TestSidecarCommandsStageTheOrganizationWithTheOperandInTheSummary(t *testing.T) {
	orgID := ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityOrganization, orgID, true)}
	cases := []struct {
		name        string
		call        GovernedCall
		wantOperand string
	}{
		{
			"confirm_fact",
			NewConfirmFactCall(provider, ConfirmFactCommand{ID: orgID, FactKey: "named_customer:acme-inc"}),
			"named_customer:acme-inc",
		},
		{
			"update_fact",
			NewUpdateFactCall(provider, UpdateFactCommand{ID: orgID, FactKey: "named_customer:acme-inc", Fields: json.RawMessage(`{"value":"Acme Inc"}`)}),
			"named_customer:acme-inc",
		},
		{
			"confirm_profile_field",
			NewConfirmProfileFieldCall(provider, ConfirmProfileFieldCommand{ID: orgID, Field: "icp"}),
			"icp",
		},
		{
			"update_profile_field",
			NewUpdateProfileFieldCall(provider, UpdateProfileFieldCommand{ID: orgID, Field: "icp", Fields: json.RawMessage(`{"value":"Payments infra"}`)}),
			"icp",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := StageSubject(context.Background(), c.call)
			if err != nil {
				t.Fatalf("staging answered %v, want it staged", err)
			}
			if info.TargetType != "organization" || info.TargetID != orgID {
				t.Errorf("staged target = (%s,%s), want (organization,%s)", info.TargetType, info.TargetID, orgID)
			}
			if !strings.Contains(info.Summary, c.wantOperand) {
				t.Errorf("summary %q does not name the operand %q — a human triaging the inbox cannot tell "+
					"which fact or field they are deciding about", info.Summary, c.wantOperand)
			}
			if !strings.Contains(info.Summary, "Acme") {
				t.Errorf("summary %q does not name the organization — an id alone tells an approver nothing "+
					"about which organization is affected", info.Summary)
			}
		})
	}
}

// A correction's summary names the new value too, where the body parses —
// the value IS the effect a human is weighing, unlike a whole-record patch's
// arbitrary field set.
func TestSidecarUpdateCommandsNameTheCorrectedValue(t *testing.T) {
	orgID := ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityOrganization, orgID, true)}

	factInfo, err := StageSubject(context.Background(),
		NewUpdateFactCall(provider, UpdateFactCommand{ID: orgID, FactKey: "annual_revenue:2026", Fields: json.RawMessage(`{"value":"12000000"}`)}))
	if err != nil {
		t.Fatalf("staging a fact correction answered %v", err)
	}
	if !strings.Contains(factInfo.Summary, "12000000") {
		t.Errorf("fact correction summary %q does not name the corrected value", factInfo.Summary)
	}

	fieldInfo, err := StageSubject(context.Background(),
		NewUpdateProfileFieldCall(provider, UpdateProfileFieldCommand{ID: orgID, Field: "usp", Fields: json.RawMessage(`{"value":"Fastest onboarding"}`)}))
	if err != nil {
		t.Fatalf("staging a profile field correction answered %v", err)
	}
	if !strings.Contains(fieldInfo.Summary, "Fastest onboarding") {
		t.Errorf("profile field correction summary %q does not name the corrected value", fieldInfo.Summary)
	}
}

// An organization the caller cannot see is refused before anything is
// staged, for all four sidecar commands — the row-scope miss, not merely a
// generic error.
func TestSidecarCommandsRefuseAnUnreadableOrganization(t *testing.T) {
	id := ids.NewV7()
	cases := []struct {
		name string
		call GovernedCall
	}{
		{"confirm_fact", NewConfirmFactCall(unreadableProvider{}, ConfirmFactCommand{ID: id, FactKey: "k"})},
		{"update_fact", NewUpdateFactCall(unreadableProvider{}, UpdateFactCommand{ID: id, FactKey: "k", Fields: json.RawMessage(`{"value":"v"}`)})},
		{"confirm_profile_field", NewConfirmProfileFieldCall(unreadableProvider{}, ConfirmProfileFieldCommand{ID: id, Field: "icp"})},
		{"update_profile_field", NewUpdateProfileFieldCall(unreadableProvider{}, UpdateProfileFieldCommand{ID: id, Field: "icp", Fields: json.RawMessage(`{"value":"v"}`)})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call.Guards(context.Background()); !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("guarding an unreadable organization answered %v, want the row-scope miss", err)
			}
		})
	}
}

// An organization held in another system of record is refused too — the
// decidability probe and the version pin both read our own tables, which
// the organization has no row in.
func TestSidecarCommandsRefuseAnOrganizationHeldElsewhere(t *testing.T) {
	id := ids.NewV7()
	cases := []struct {
		name string
		call GovernedCall
	}{
		{"confirm_fact", NewConfirmFactCall(elsewhereProvider{}, ConfirmFactCommand{ID: id, FactKey: "k"})},
		{"update_fact", NewUpdateFactCall(elsewhereProvider{}, UpdateFactCommand{ID: id, FactKey: "k", Fields: json.RawMessage(`{"value":"v"}`)})},
		{"confirm_profile_field", NewConfirmProfileFieldCall(elsewhereProvider{}, ConfirmProfileFieldCommand{ID: id, Field: "icp"})},
		{"update_profile_field", NewUpdateProfileFieldCall(elsewhereProvider{}, UpdateProfileFieldCommand{ID: id, Field: "icp", Fields: json.RawMessage(`{"value":"v"}`)})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call.Guards(context.Background()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
				t.Errorf("guarding a mirrored organization answered %v, want the unsupported-by-SoR refusal", err)
			}
		})
	}
}

// Both Guards and Subject read the SAME row: a fact or profile-field
// correction asks two questions of the organization (its authority, its
// label), and answering them from two different reads risks a record that
// changed between them describing itself two ways.
func TestSidecarCommandsReadTheirTargetOnce(t *testing.T) {
	id := ids.NewV7()
	provider := &countingProvider{}
	call := NewUpdateFactCall(provider, UpdateFactCommand{ID: id, FactKey: "k", Fields: json.RawMessage(`{"value":"v"}`)})

	if _, err := StageSubject(context.Background(), call); err != nil {
		t.Fatalf("staging answered %v, want it staged", err)
	}
	if provider.reads != 1 {
		t.Errorf("the resolver read its target %d times, want 1", provider.reads)
	}
}
