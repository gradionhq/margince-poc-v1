// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func stringPointer(value string) *string { return &value }

func TestValidateOnboardingInputAcceptsPartialWizardChoices(t *testing.T) {
	in := PutOnboardingStateInput{
		Step:       OnboardingStepRead,
		SourceMode: stringPointer(OnboardingSourceWebsite),
		CompanyDraft: OnboardingCompanyDraft{
			DisplayName: stringPointer(" Acme "),
		},
	}

	draft, err := validateOnboardingInput(&in)
	if err != nil {
		t.Fatalf("validate partial website choice: %v", err)
	}
	if string(draft) != `{"display_name":" Acme "}` {
		t.Fatalf("draft = %s, want the resumable human text", draft)
	}
}

func TestValidateOnboardingInputRejectsInvalidCombinations(t *testing.T) {
	readID := ids.MustParse("018f3a1b-0000-7000-8000-0000000000b2")
	tests := []struct {
		name  string
		input PutOnboardingStateInput
		field string
	}{
		{
			name:  "negative version",
			input: PutOnboardingStateInput{ExpectedVersion: -1, Step: OnboardingStepRead},
			field: "expected_version",
		},
		{
			name:  "unknown step",
			input: PutOnboardingStateInput{Step: "invented"},
			field: "step",
		},
		{
			name: "invalid website URL",
			input: PutOnboardingStateInput{
				Step: OnboardingStepRead, SourceMode: stringPointer(OnboardingSourceWebsite),
				WebsiteURL: stringPointer("not a URL"),
			},
			field: "website_url",
		},
		{
			name: "site read on manual path",
			input: PutOnboardingStateInput{
				Step: OnboardingStepConfirm, SourceMode: stringPointer(OnboardingSourceManual),
				SiteReadID: &readID,
			},
			field: "site_read_id",
		},
		{
			name: "duplicate fact",
			input: PutOnboardingStateInput{
				Step: OnboardingStepConfirm, SelectedFactKeys: []string{"service:crm", " service:crm "},
			},
			field: "selected_fact_keys",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateOnboardingInput(&test.input)
			var invalid *InvalidOnboardingStateError
			if !errors.As(err, &invalid) || invalid.Field != test.field {
				t.Fatalf("error = %v, want invalid %s", err, test.field)
			}
		})
	}
}

func TestValidateOnboardingAdvancePinsCreatorAndMemberPaths(t *testing.T) {
	if err := validateOnboardingAdvance(OnboardingPathCreator, OnboardingStepVoice, false); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("incomplete creator advance = %v, want conflict", err)
	}
	if err := validateOnboardingAdvance(OnboardingPathCreator, OnboardingStepVoice, true); err != nil {
		t.Fatalf("complete creator advance: %v", err)
	}
	if err := validateOnboardingAdvance(OnboardingPathMember, OnboardingStepConfirm, true); err == nil {
		t.Fatal("member was allowed into the creator-only confirm step")
	}
	if err := validateOnboardingAdvance(OnboardingPathMember, OnboardingStepConnect, true); err != nil {
		t.Fatalf("member connect advance: %v", err)
	}
}

func TestOnboardingAuditImageExcludesDraftBusinessTruth(t *testing.T) {
	state := OnboardingState{
		Path: OnboardingPathCreator, Step: OnboardingStepConfirm, Version: 3,
		CompanyDraft: OnboardingCompanyDraft{DisplayName: stringPointer("Secret draft")},
	}

	image := onboardingAuditImage(state)
	if _, leaked := image["company_draft"]; leaked {
		t.Fatal("audit image leaked the resumable company draft")
	}
	if image["step"] != OnboardingStepConfirm || image["version"] != int64(3) {
		t.Fatalf("audit image = %#v, want operational state only", image)
	}
}
