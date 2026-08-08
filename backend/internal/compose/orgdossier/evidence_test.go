// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The receipt held to its one rule: it never invents. A field this provenance
// kind owes and cannot fill is NAMED, because an unrecorded canonical URL and a
// recorded empty one read identically otherwise — and only one of them leaves
// the reader with nowhere to go.

import (
	"context"
	"errors"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func ptr[T any](v T) *T { return &v }

func siteReadField() crmcontracts.CompanyProfileField {
	return crmcontracts.CompanyProfileField{
		Id:              rowID(),
		Field:           crmcontracts.CompanyProfileFieldFieldOfferSummary,
		Value:           "Load-shifting software",
		Source:          crmcontracts.CompanyProfileFieldSourceSiteRead,
		CapturedBy:      ptr("site_read:crawler"),
		EvidenceSnippet: ptr("We build load-shifting software for industry."),
		SourceUrl:       ptr("https://voltaq.example/about"),
		Confidence:      ptr(float32(0.9)),
		UpdatedAt:       assessedAt,
	}
}

func receiptFor(t *testing.T, field crmcontracts.CompanyProfileField) crmcontracts.ClaimEvidence {
	t.Helper()
	in := Input{OrganizationID: "o-1", ProfileFields: []crmcontracts.CompanyProfileField{field}}
	got, err := profileFieldEvidence(in, *field.Id)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	return got
}

// A site read owes the reader somewhere to go and something to compare against.
func TestASiteReadReceiptCarriesTheURLAndTheSpanItWasReadFrom(t *testing.T) {
	got := receiptFor(t, siteReadField())

	if got.SourceKind != crmcontracts.ClaimEvidenceSourceKindSiteRead {
		t.Errorf("source kind = %q, want site_read", got.SourceKind)
	}
	if got.Identity == nil || (*got.Identity)["source_url"] != "https://voltaq.example/about" {
		t.Errorf("identity = %v, want the canonical URL the value was read from", got.Identity)
	}
	if got.Gaps != nil {
		t.Errorf("gaps = %v, want none — this row carries everything its kind owes", *got.Gaps)
	}
	if got.Confidence == nil {
		t.Error("a machine-read value carries the model's confidence and it is missing")
	}
}

// The gap list is the point of the whole receipt. A claim the reader was told
// is checkable, with no URL to check it against, must say so.
func TestAReceiptNamesTheFieldsItsKindOwesAndCannotFill(t *testing.T) {
	bare := siteReadField()
	bare.SourceUrl = nil
	bare.EvidenceSnippet = ptr("   ")

	got := receiptFor(t, bare)

	if got.Gaps == nil {
		t.Fatal("no gaps: the receipt rendered a missing URL and a blank excerpt as though present")
	}
	named := map[string]bool{}
	for _, gap := range *got.Gaps {
		named[gap] = true
	}
	if !named["source_url"] || !named["excerpt"] {
		t.Errorf("gaps = %v, want both source_url and excerpt named", *got.Gaps)
	}
	if got.Identity != nil {
		if _, present := (*got.Identity)["source_url"]; present {
			t.Error("a missing URL was rendered into identity as an empty value")
		}
	}
}

// DOSS-AC-16: a person's assertion and an imported row carry no model
// confidence, and printing one would fabricate a number nobody computed.
func TestOnlyAMachineReadValueCarriesAModelConfidence(t *testing.T) {
	for name, tc := range map[string]struct {
		source   crmcontracts.CompanyProfileFieldSource
		wantKind crmcontracts.ClaimEvidenceSourceKind
	}{
		"a person's own answer": {
			crmcontracts.CompanyProfileFieldSourceHuman, crmcontracts.ClaimEvidenceSourceKindHuman,
		},
		"a connector record": {
			crmcontracts.CompanyProfileFieldSourceConnector, crmcontracts.ClaimEvidenceSourceKindConnector,
		},
		"an imported row": {
			crmcontracts.CompanyProfileFieldSourceMigration, crmcontracts.ClaimEvidenceSourceKindMigration,
		},
	} {
		t.Run(name, func(t *testing.T) {
			field := siteReadField()
			field.Source = tc.source
			// The row still HOLDS a confidence — the point is that the receipt
			// must not report it for a kind that cannot have one.
			got := receiptFor(t, field)

			if got.SourceKind != tc.wantKind {
				t.Errorf("source kind = %q, want %q", got.SourceKind, tc.wantKind)
			}
			if got.Confidence != nil {
				t.Errorf("confidence = %v, want absent — %s carries no model confidence",
					*got.Confidence, name)
			}
		})
	}
}

// Read and confirmed are different claims, and a receipt that collapsed them
// would let a machine re-read pass for a person's approval.
func TestAReceiptKeepsWhenItWasReadApartFromWhenAPersonConfirmedIt(t *testing.T) {
	confirmed := assessedAt.Add(-time.Hour)
	field := siteReadField()
	field.RetrievedAt = ptr(assessedAt.Add(-48 * time.Hour))
	field.VerifiedAt = ptr(confirmed)

	got := receiptFor(t, field)

	if got.RetrievedAt == nil || got.LastVerifiedAt == nil {
		t.Fatal("the receipt dropped one of the two timestamps")
	}
	if got.RetrievedAt.Equal(*got.LastVerifiedAt) {
		t.Error("read and confirmed were reported as the same moment")
	}
}

// A human-entered value nobody has since confirmed owes that gap, because
// "typed once" and "checked recently" are different assurances.
func TestAHumanValueNobodyConfirmedNamesThatGap(t *testing.T) {
	field := siteReadField()
	field.Source = crmcontracts.CompanyProfileFieldSourceHuman
	field.CapturedBy = ptr("human:ada")
	field.VerifiedAt = nil

	got := receiptFor(t, field)

	if got.Identity == nil || (*got.Identity)["actor"] != "human:ada" {
		t.Errorf("identity = %v, want the person who said so", got.Identity)
	}
	if got.Gaps == nil {
		t.Fatal("no gaps: an unconfirmed human value claimed to be confirmed")
	}
	found := false
	for _, gap := range *got.Gaps {
		if gap == "verified_at" {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v, want verified_at named", *got.Gaps)
	}
}

// A record outside the caller's own input is absent, whether it does not exist
// or they may not see it. Both answer alike: the existence of a record they
// cannot open is itself a disclosure (DOSS-AC-11).
func TestARecordTheReaderCannotSeeIsIndistinguishableFromOneThatDoesNotExist(t *testing.T) {
	in := Input{OrganizationID: "o-1", ProfileFields: []crmcontracts.CompanyProfileField{siteReadField()}}
	// A well-formed id this caller was never handed.
	stranger := openapi_types.UUID(ids.NewV7())

	// Specifically NOT-FOUND rather than a permission denial: a 403 would
	// confirm the record exists to a reader who may not see it.
	if _, err := profileFieldEvidence(in, stranger); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound — existence is what row scoping protects", err)
	}
}

// A fact is the other half of what these surfaces cite, and every case above
// goes through the profile-field path.
func TestAFactReceiptCarriesItsOwnProvenance(t *testing.T) {
	id := rowID()
	in := Input{OrganizationID: "o-1", Facts: []crmcontracts.OrganizationFact{{
		Id:              id,
		Field:           crmcontracts.OrganizationFactFieldTechnology,
		Value:           "SAP S/4HANA",
		Source:          crmcontracts.OrganizationFactSourceSiteRead,
		CapturedBy:      ptr("site_read:crawler"),
		EvidenceSnippet: ptr("We run SAP S/4HANA across the group."),
		SourceUrl:       ptr("https://voltaq.example/tech"),
		Confidence:      ptr(float32(0.8)),
		UpdatedAt:       assessedAt,
	}}}

	got, err := factEvidence(in, *id)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if got.EntityType != crmcontracts.ClaimEvidenceEntityTypeFact {
		t.Errorf("entity type = %q, want fact", got.EntityType)
	}
	if got.Label == nil || *got.Label == "" {
		t.Error("the receipt does not say which field this value is")
	}
	if got.Identity == nil || (*got.Identity)["source_url"] != "https://voltaq.example/tech" {
		t.Errorf("identity = %v, want the URL this fact was read from", got.Identity)
	}
}

// The dispatch itself: a kind with no receipt to write, and one this contract
// does not know, both answer alike rather than returning an empty body.
func TestAKindWithNoReceiptAnswersNotFound(t *testing.T) {
	facts := stubFacts{in: Input{OrganizationID: "o-1"}}
	for _, kind := range []string{citeOrganization, "activity", "deal", ""} {
		t.Run(kind, func(t *testing.T) {
			_, err := EvidenceFor(context.Background(), facts,
				ids.From[ids.OrganizationKind](ids.NewV7()), kind, openapi_types.UUID(ids.NewV7()))
			if !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound for %q", err, kind)
			}
		})
	}
}

// stubFacts serves one prepared input, so the dispatch can be proven without a
// database — the row-scoped reads it wraps are proven in the integration lane.
type stubFacts struct{ in Input }

func (s stubFacts) ListOrganizationProfileFields(
	context.Context, ids.OrganizationID,
) ([]crmcontracts.CompanyProfileField, error) {
	return s.in.ProfileFields, nil
}

func (s stubFacts) ListOrganizationFacts(
	context.Context, ids.OrganizationID,
) ([]crmcontracts.OrganizationFact, error) {
	return s.in.Facts, nil
}
