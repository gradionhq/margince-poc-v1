// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// What the dossier is written FROM, and the set of records it may cite.
//
// Both halves are here because they are the same claim seen twice: a sentence
// may cite a record exactly when the assembler put that record in front of the
// writer, and the grounding filter checks the second against the first. Keeping
// them apart is how a filter ends up trusting a set nobody built.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Facts is the read seam over the company's factual sidecars. It is an
// interface so the writer can be proven without a database, and it is narrow on
// purpose: the dossier may see the sidecars and nothing else (DOSS-AC-4).
//
// Both reads run AS THE CALLER, inside the ordinary gates, so a field the
// reader may not see never enters an input and therefore cannot enter a
// sentence. Synthesis does not launder a mask (DOSS-AC-N-1) — the narrowing
// happens before assembly, not after it.
type Facts interface {
	ListOrganizationProfileFields(ctx context.Context, id ids.OrganizationID) ([]crmcontracts.CompanyProfileField, error)
	ListOrganizationFacts(ctx context.Context, id ids.OrganizationID) ([]crmcontracts.OrganizationFact, error)
}

// Input is the assembled factual picture of one company.
type Input struct {
	OrganizationID string
	ProfileFields  []crmcontracts.CompanyProfileField
	Facts          []crmcontracts.OrganizationFact
}

// The citable record kinds, DERIVED from the contract's own enum rather than
// re-spelled. A literal copy would let a contract rename leave the filter
// matching a type the wire no longer carries — a citation that silently stops
// grounding.
var (
	citeOrganization = string(crmcontracts.OrganizationBriefEvidenceEntityTypeOrganization)
	citeProfileField = string(crmcontracts.OrganizationBriefEvidenceEntityTypeProfileField)
	citeFact         = string(crmcontracts.OrganizationBriefEvidenceEntityTypeFact)
)

// BuildInput reads the sidecars under the caller's own scope.
func BuildInput(ctx context.Context, facts Facts, id ids.OrganizationID) (Input, error) {
	fields, err := facts.ListOrganizationProfileFields(ctx, id)
	if err != nil {
		return Input{}, err
	}
	extracted, err := facts.ListOrganizationFacts(ctx, id)
	if err != nil {
		return Input{}, err
	}
	return Input{
		OrganizationID: id.String(),
		ProfileFields:  fields,
		Facts:          extracted,
	}, nil
}

// KnownRecords is what this dossier was assembled from, keyed by TYPE AND ID.
//
// Keying on the id alone would accept a real fact id cited as a profile field:
// the id passes, and the chip then routes the reader to the wrong place — or to
// a record of a kind they were never shown. The pair is the reference, so the
// pair is what is checked.
//
// A profile field with no row id contributes NOTHING to this set. That is not a
// gap to paper over: a sentence citing a field the assembler cannot name is a
// sentence the reader cannot open, and the filter is supposed to drop it.
func KnownRecords(in Input) map[claims.Evidence]bool {
	known := map[claims.Evidence]bool{
		{EntityType: citeOrganization, EntityID: in.OrganizationID}: true,
	}
	for _, field := range in.ProfileFields {
		if field.Id == nil {
			continue
		}
		known[claims.Evidence{EntityType: citeProfileField, EntityID: field.Id.String()}] = true
	}
	for _, fact := range in.Facts {
		if fact.Id == nil {
			continue
		}
		known[claims.Evidence{EntityType: citeFact, EntityID: fact.Id.String()}] = true
	}
	return known
}
