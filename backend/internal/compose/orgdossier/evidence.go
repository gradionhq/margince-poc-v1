// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The receipt behind one cited record (DOSS-WIRE-3).
//
// A generated sentence carries the records it was written from; this is what a
// reader gets when they open one. It is the whole reason the surfaces above are
// allowed to write prose at all: a sentence nobody can check is worth less than
// the fact it paraphrases, and this is where checking happens.
//
// The rule this file exists to keep is that a receipt never invents. Each
// provenance kind owes its own identifying fields, and one that cannot be
// filled is NAMED in `gaps` rather than sent as an empty string — because an
// unrecorded canonical URL and a recorded empty one read identically otherwise,
// and only one of them means the reader has nowhere to go.

import (
	"context"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EvidenceFor builds the receipt for one cited record.
//
// It reads through the SAME row-scoped seam the assemblies read, as the caller,
// so a record the reader cannot see is a 404 with no body — the existence of a
// record they may not open is itself a disclosure (DOSS-AC-11).
func EvidenceFor(ctx context.Context, facts Facts, orgID ids.OrganizationID,
	entityType string, entityID openapi_types.UUID,
) (crmcontracts.ClaimEvidence, error) {
	var zero crmcontracts.ClaimEvidence
	in, err := BuildInput(ctx, facts, orgID)
	if err != nil {
		return zero, err
	}
	switch entityType {
	case citeProfileField:
		return profileFieldEvidence(in, entityID)
	case citeFact:
		return factEvidence(in, entityID)
	case citeOrganization:
		// The organization is cited by claims about the company as a whole. It
		// is the page the reader is already on and carries no provenance of its
		// own, so there is no receipt to write.
		return zero, apperrors.ErrNotFound
	}
	return zero, apperrors.ErrNotFound
}

func profileFieldEvidence(in Input, entityID openapi_types.UUID) (crmcontracts.ClaimEvidence, error) {
	for _, field := range in.ProfileFields {
		if field.Id == nil || *field.Id != entityID {
			continue
		}
		return receipt(receiptRow{
			entityType:  citeProfileField,
			entityID:    entityID,
			source:      string(field.Source),
			label:       fieldLabel(field.Field),
			value:       field.Value,
			excerpt:     field.EvidenceSnippet,
			sourceURL:   field.SourceUrl,
			confidence:  field.Confidence,
			capturedBy:  field.CapturedBy,
			retrievedAt: field.RetrievedAt,
			verifiedAt:  field.VerifiedAt,
		}), nil
	}
	// Not in the caller's own input means either no such row or one they may
	// not see, and the two answer alike on purpose.
	return crmcontracts.ClaimEvidence{}, apperrors.ErrNotFound
}

func factEvidence(in Input, entityID openapi_types.UUID) (crmcontracts.ClaimEvidence, error) {
	for _, fact := range in.Facts {
		if fact.Id == nil || *fact.Id != entityID {
			continue
		}
		return receipt(receiptRow{
			entityType:  citeFact,
			entityID:    entityID,
			source:      string(fact.Source),
			label:       strings.ReplaceAll(string(fact.Field), "_", " "),
			value:       fact.Value,
			excerpt:     fact.EvidenceSnippet,
			sourceURL:   fact.SourceUrl,
			confidence:  fact.Confidence,
			capturedBy:  fact.CapturedBy,
			retrievedAt: fact.RetrievedAt,
			verifiedAt:  fact.VerifiedAt,
		}), nil
	}
	return crmcontracts.ClaimEvidence{}, apperrors.ErrNotFound
}

// receiptRow is one stored value's provenance, flattened out of the two row
// shapes that carry it. The two tables differ in what they key on and agree on
// everything a receipt reads.
type receiptRow struct {
	entityType  string
	entityID    openapi_types.UUID
	source      string
	label       string
	value       string
	excerpt     *string
	sourceURL   *string
	confidence  *float32
	capturedBy  *string
	retrievedAt *time.Time
	verifiedAt  *time.Time
}

func receipt(row receiptRow) crmcontracts.ClaimEvidence {
	kind := sourceKind(row.source)
	// Every stored value is stamped with its capturer from the authenticated
	// principal, so an absent one is a defect rather than a shape. It is named
	// as a gap below instead of rendered as an empty attribution, which would
	// read as "produced by nobody" rather than "we did not record who".
	producedBy := ""
	if row.capturedBy != nil {
		producedBy = strings.TrimSpace(*row.capturedBy)
	}
	identity, gaps := identityFor(kind, row, producedBy)
	if producedBy == "" {
		gaps = append(gaps, "produced_by")
	}
	out := crmcontracts.ClaimEvidence{
		EntityType:     crmcontracts.ClaimEvidenceEntityType(row.entityType),
		EntityId:       row.entityID,
		SourceKind:     kind,
		Label:          &row.label,
		Value:          &row.value,
		ProducedBy:     producedBy,
		Identity:       &identity,
		Excerpt:        quoted(row.excerpt),
		RetrievedAt:    row.retrievedAt,
		LastVerifiedAt: row.verifiedAt,
	}
	if len(gaps) > 0 {
		out.Gaps = &gaps
	}
	// A model confidence exists only where a model read something. Printing one
	// beside a person's own answer or an imported row would fabricate a number
	// nobody computed (DOSS-AC-16).
	if kind == crmcontracts.ClaimEvidenceSourceKindSiteRead {
		out.Confidence = row.confidence
	}
	return out
}

// quoted drops an excerpt that would render as an empty quotation. A blank
// string and an absent one are the same fact — nothing was quoted — and only
// the absent one renders honestly; the blank one draws quote marks around air.
func quoted(excerpt *string) *string {
	if excerpt == nil || strings.TrimSpace(*excerpt) == "" {
		return nil
	}
	return excerpt
}

// sourceKind maps the stored provenance vocabulary (migration 0099) onto the
// receipt's. An unrecognized value reports as a rule rather than guessing at a
// person or a connector: attributing a value to the wrong origin is a worse
// answer than a vague one.
func sourceKind(source string) crmcontracts.ClaimEvidenceSourceKind {
	switch source {
	case string(crmcontracts.CompanyProfileFieldSourceSiteRead):
		return crmcontracts.ClaimEvidenceSourceKindSiteRead
	case string(crmcontracts.CompanyProfileFieldSourceHuman):
		return crmcontracts.ClaimEvidenceSourceKindHuman
	case string(crmcontracts.CompanyProfileFieldSourceConnector):
		return crmcontracts.ClaimEvidenceSourceKindConnector
	case string(crmcontracts.CompanyProfileFieldSourceMigration):
		return crmcontracts.ClaimEvidenceSourceKindMigration
	}
	return crmcontracts.ClaimEvidenceSourceKindRule
}

// identityFor fills the fields THIS kind owes, and names the ones it cannot.
//
// The gaps list is the point. A site-read receipt with no canonical URL leaves
// the reader with a claim they were told is checkable and no way to check it,
// and saying so is the only honest rendering of that.
func identityFor(kind crmcontracts.ClaimEvidenceSourceKind, row receiptRow, producedBy string) (map[string]any, []string) {
	identity := map[string]any{}
	var gaps []string
	need := func(name string, value *string) {
		if value == nil || strings.TrimSpace(*value) == "" {
			gaps = append(gaps, name)
			return
		}
		identity[name] = *value
	}
	// The capturer under whatever name this kind gives it. An absent one is
	// named as a gap by the caller, so it must not ALSO appear here as an empty
	// attribution — a receipt that does both says the field is missing and
	// renders it as blank, and the reader believes whichever they see first.
	nameCapturer := func(name string) {
		if producedBy != "" {
			identity[name] = producedBy
		}
	}
	switch kind {
	case crmcontracts.ClaimEvidenceSourceKindSiteRead:
		need("source_url", row.sourceURL)
		need("excerpt", row.excerpt)
	case crmcontracts.ClaimEvidenceSourceKindHuman:
		// Who said so, and when they confirmed it. The actor is always present
		// — it is stamped from the authenticated principal, never a request —
		// so only the confirmation can be missing.
		nameCapturer("actor")
		if row.verifiedAt == nil {
			gaps = append(gaps, "verified_at")
		}
	case crmcontracts.ClaimEvidenceSourceKindConnector:
		nameCapturer("connector")
		need("source_url", row.sourceURL)
	case crmcontracts.ClaimEvidenceSourceKindMigration:
		nameCapturer("import")
	case crmcontracts.ClaimEvidenceSourceKindRule:
		nameCapturer("produced_by")
	}
	return identity, gaps
}
