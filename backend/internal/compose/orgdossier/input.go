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
// Both reads run AS THE CALLER, through the same store methods
// GET /organizations/{id}/profile-fields and .../facts serve, inside the same
// object and row-scope gates. So an assembly can only be written from records
// this reader could already fetch for themselves, and synthesis discloses
// nothing new.
//
// What that does NOT yet give is DOSS-AC-N-1's field masking. This platform has
// no per-reader field mask on a company's values — the only masks in the tree
// are over field HISTORY, and that map is empty — so there is no mask here to
// launder and none to honour. Row scope is enforced; field scope does not exist
// yet. See gradionhq/margince#4.
type Facts interface {
	ListOrganizationProfileFields(ctx context.Context, id ids.OrganizationID) ([]crmcontracts.CompanyProfileField, error)
	ListOrganizationFacts(ctx context.Context, id ids.OrganizationID) ([]crmcontracts.OrganizationFact, error)
}

// Input is the assembled factual picture of one company.
//
// OrganizationID is withheld from the JSON the model sees. It is the one id
// every assembly holds, so it cannot ground anything — and a writer handed an
// id it is forbidden to cite would spend sentences on it that the filter then
// drops whole, taking their other, valid citations with them.
type Input struct {
	OrganizationID string `json:"-"`
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
//
// The ORGANIZATION is not in the set either, for the same reason stated once
// more. It is the one id every assembly holds, so admitting it would ground any
// sentence at all — a model with nothing to say could cite the company and pass
// — and the receipt endpoint has nothing to answer with, because the row
// carries no provenance of its own. A citation that resolves to nothing teaches
// the reader that citations do not work.
func KnownRecords(in Input) map[claims.Evidence]bool {
	known := map[claims.Evidence]bool{}
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
