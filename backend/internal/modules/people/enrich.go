// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The accepted enrichment of a KNOWN organization (EP05 / scrapeCompany).
// Unlike the cold-start read-back it never resolves-by-domain or creates — it
// fills the org the proposal named, and only its empty fields, with the
// executing principal's provenance (agent:scrape). The per-field apply loop is
// shared with the read-back (applyEvidenceFields) so both write website
// evidence under the same site_read source vocabulary; the target differs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ErrNoEnrichTarget is returned when an org that IS visible has no domain to
// read — the caller maps it to a 422, distinct from a 404 (not visible).
var ErrNoEnrichTarget = errors.New("people: organization has no domain to enrich from")

// EnrichTargetURL returns the URL scrapeCompany should read for orgID: the
// org's primary domain as https://<domain>. Row-scoped — an org the caller
// cannot see is ErrNotFound (existence-hiding); a visible org with no domain
// is ErrNoEnrichTarget.
func (s *Store) EnrichTargetURL(ctx context.Context, orgID ids.OrganizationID) (string, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return "", err
	}
	var domain string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx,
			`SELECT domain FROM organization_domain
			 WHERE organization_id = $1 AND archived_at IS NULL
			 ORDER BY is_primary DESC, created_at ASC
			 LIMIT 1`, orgID).Scan(&domain)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoEnrichTarget
		}
		return err
	})
	if err != nil {
		return "", err
	}
	return "https://" + domain, nil
}

// ApplyEnrichment executes an ACCEPTED enrichment proposal against the named
// org: fill only empty columns, upsert the evidence row for every field, never
// overwrite a human-set value. One transaction, one audit row, one
// organization.updated event; captured_by is the executing principal
// (agent:scrape), source is site_read.
func (s *Store) ApplyEnrichment(ctx context.Context, orgID ids.OrganizationID, in ApplyColdStartProfileInput) error {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	if len(in.Fields) == 0 {
		return errors.New("people: an accepted enrichment carries no fields")
	}

	return s.tx(ctx, func(tx pgx.Tx) error {
		wsID := workspaceID(ctx)
		// The target is a KNOWN row — an enrichment never creates or resolves
		// by domain. Row-scope is re-checked here so a leaked org id buys
		// nothing (existence-hiding 404).
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		applied, err := applyEvidenceFields(ctx, tx, wsID, orgID, companySourceSiteRead, by, in.Fields)
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "organization", orgID.UUID, nil, map[string]any{
			auditKeySource: companySourceSiteRead, auditKeySourceURL: in.SourceURL, auditKeyFields: applied,
		})
		if err != nil {
			return fmt.Errorf("audit enrichment apply: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID, crmcontracts.WebhookPayloadOrganizationUpdated{
			ChangedFields: map[string]any{
				eventKeyDelta: applied, auditKeySource: companySourceSiteRead, auditKeySourceURL: in.SourceURL,
			},
		}); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}
		return nil
	})
}

// UnmarshalEnrichment decodes a staged enrichment proposal — the org id plus
// the shared field array. Shared with the compose effect so both sides agree
// on the JSON shape.
func UnmarshalEnrichment(raw json.RawMessage) (ids.OrganizationID, string, []ColdStartFieldInput, error) {
	var proposal struct {
		OrganizationID ids.OrganizationID `json:"organization_id"`
		SourceURL      string             `json:"source_url"`
		Fields         []struct {
			Field           string  `json:"field"`
			Value           string  `json:"value"`
			EvidenceSnippet string  `json:"evidence_snippet"`
			SourceURL       string  `json:"source_url"`
			Confidence      float32 `json:"confidence"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return ids.OrganizationID{}, "", nil, fmt.Errorf("people: enrichment proposal payload: %w", err)
	}
	fields := make([]ColdStartFieldInput, 0, len(proposal.Fields))
	for _, f := range proposal.Fields {
		fields = append(fields, ColdStartFieldInput{
			Field: f.Field, Value: f.Value, EvidenceSnippet: f.EvidenceSnippet,
			SourceURL: f.SourceURL, Confidence: f.Confidence,
		})
	}
	return proposal.OrganizationID, proposal.SourceURL, fields, nil
}
