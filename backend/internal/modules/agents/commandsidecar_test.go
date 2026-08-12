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
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Each sidecar command stages against the ORGANIZATION it routes through,
// with the operand carried into the summary — the property that keeps two
// facts, or two profile fields, on one organization from rendering as one
// indistinguishable approval.
func TestSidecarCommandsStageTheOrganizationWithTheOperandInTheSummary(t *testing.T) {
	orgID := ids.NewV7()
	cases := []struct {
		name        string
		call        GovernedCall
		wantOperand string
	}{
		{
			"confirm_fact",
			NewConfirmFactCall(unreadableProvider{}, ConfirmFactCommand{ID: orgID, FactKey: "named_customer:acme-inc"}),
			"named_customer:acme-inc",
		},
		{
			"update_fact",
			NewUpdateFactCall(unreadableProvider{}, UpdateFactCommand{ID: orgID, FactKey: "named_customer:acme-inc"}),
			"named_customer:acme-inc",
		},
		{
			"confirm_profile_field",
			NewConfirmProfileFieldCall(unreadableProvider{}, ConfirmProfileFieldCommand{ID: orgID, Field: "icp"}),
			"icp",
		},
		{
			"update_profile_field",
			NewUpdateProfileFieldCall(unreadableProvider{}, UpdateProfileFieldCommand{ID: orgID, Field: "icp"}),
			"icp",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// call.Subject directly, not StageSubject: these fixtures answer every
			// Read with not-found, so a Subject that (wrongly) tried to read the
			// organization for a label would fail here — calling Subject alone
			// (skipping Guards, unlike StageSubject) proves it needs no read to
			// name the operand.
			info, err := c.call.Subject(context.Background())
			if err != nil {
				t.Fatalf("naming the subject answered %v, want no error — Subject reads nothing", err)
			}
			if info.TargetType != "organization" || info.TargetID != orgID {
				t.Errorf("staged target = (%s,%s), want (organization,%s)", info.TargetType, info.TargetID, orgID)
			}
			if !strings.Contains(info.Summary, c.wantOperand) {
				t.Errorf("summary %q does not name the operand %q — a human triaging the inbox cannot tell "+
					"which fact or field they are deciding about", info.Summary, c.wantOperand)
			}
		})
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
		{"update_fact", NewUpdateFactCall(unreadableProvider{}, UpdateFactCommand{ID: id, FactKey: "k"})},
		{"confirm_profile_field", NewConfirmProfileFieldCall(unreadableProvider{}, ConfirmProfileFieldCommand{ID: id, Field: "icp"})},
		{"update_profile_field", NewUpdateProfileFieldCall(unreadableProvider{}, UpdateProfileFieldCommand{ID: id, Field: "icp"})},
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
		{"update_fact", NewUpdateFactCall(elsewhereProvider{}, UpdateFactCommand{ID: id, FactKey: "k"})},
		{"confirm_profile_field", NewConfirmProfileFieldCall(elsewhereProvider{}, ConfirmProfileFieldCommand{ID: id, Field: "icp"})},
		{"update_profile_field", NewUpdateProfileFieldCall(elsewhereProvider{}, UpdateProfileFieldCommand{ID: id, Field: "icp"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call.Guards(context.Background()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
				t.Errorf("guarding a mirrored organization answered %v, want the unsupported-by-SoR refusal", err)
			}
		})
	}
}

// A served, readable organization is admitted rather than refused — Guards'
// counterpart to the two refusal tests above, proving the happy path through
// the same seam rather than only its failure modes.
func TestSidecarCommandsAdmitAReadableOrganization(t *testing.T) {
	id := ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityOrganization, id, true)}
	call := NewConfirmFactCall(provider, ConfirmFactCommand{ID: id, FactKey: "k"})
	if err := call.Guards(context.Background()); err != nil {
		t.Fatalf("guarding a readable, authoritative organization answered %v, want it admitted", err)
	}
}
