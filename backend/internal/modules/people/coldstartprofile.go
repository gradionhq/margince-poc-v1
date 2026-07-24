// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The accepted cold-start read-back (features/07 §1): a human approval
// releases the staged fields onto the organization the source URL names
// — resolve by domain, create when absent, fill only what no human has
// set, and keep every value's verbatim evidence queryable
// (organization_profile_field, 0037). One transaction, one audit row,
// one organization event; captured_by comes from the executing
// principal (agent:coldstart), source is site_read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ColdStartFieldInput is one accepted, evidenced field.
type ColdStartFieldInput struct {
	Field           string
	Value           string
	EvidenceSnippet string
	SourceURL       string
	Confidence      float32
}

// ApplyColdStartProfileInput carries the whole accepted proposal.
type ApplyColdStartProfileInput struct {
	SourceURL string
	Fields    []ColdStartFieldInput
}

// columnBackedColdStartFields maps read-back fields onto organization
// columns; everything else lives only in organization_profile_field.
var columnBackedColdStartFields = map[string]string{
	"legal_name":         "legal_name",
	"industry":           "industry",
	"registered_address": "address",
}

// ApplyColdStartProfile executes an ACCEPTED coldstart proposal. A
// column a human (or any earlier capture) already filled is left
// untouched — acceptance covers the staged diff, not an overwrite of
// standing values (features/07 §2: colliding writes need their own 🟡).
// The evidence row is upserted for EVERY accepted field, column-backed
// or not, so provenance stays queryable either way.
func (s *Store) ApplyColdStartProfile(ctx context.Context, in ApplyColdStartProfileInput) (ids.OrganizationID, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return ids.OrganizationID{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return ids.OrganizationID{}, err
	}
	host, err := coldStartHost(in.SourceURL)
	if err != nil {
		return ids.OrganizationID{}, err
	}
	if len(in.Fields) == 0 {
		return ids.OrganizationID{}, errors.New("people: an accepted coldstart proposal carries no fields")
	}

	var orgID ids.OrganizationID
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		orgID, err = applyColdStartTx(ctx, tx, in, host, by)
		return err
	})
	if err != nil {
		return ids.OrganizationID{}, err
	}
	return orgID, nil
}

// applyColdStartTx resolves-or-creates the target organization, fills the
// accepted fields (evidence for every one, columns only when empty), and
// runs the write shape — create vs update chosen by whether the org was
// just minted — all inside the caller's transaction.
func applyColdStartTx(ctx context.Context, tx pgx.Tx, in ApplyColdStartProfileInput, host, by string) (ids.OrganizationID, error) {
	wsID := workspaceID(ctx)
	orgID, created, err := resolveOrCreateColdStartOrg(ctx, tx, wsID, host, by, in.Fields)
	if err != nil {
		return ids.OrganizationID{}, err
	}
	applied, err := applyEvidenceFields(ctx, tx, wsID, orgID, companySourceSiteRead, by, in.Fields)
	if err != nil {
		return ids.OrganizationID{}, err
	}

	action := "update"
	if created {
		action = "create"
	}
	auditID, err := storekit.Audit(ctx, tx, action, "organization", orgID.UUID, nil, map[string]any{
		auditKeySource: companySourceSiteRead, auditKeySourceURL: in.SourceURL, auditKeyFields: applied,
	})
	if err != nil {
		return ids.OrganizationID{}, fmt.Errorf("audit coldstart apply: %w", err)
	}
	payload := coldStartApplyPayload(created, in, host, by, applied)
	if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID, payload); err != nil {
		return ids.OrganizationID{}, fmt.Errorf("emit %s: %w", payload.EventType(), err)
	}
	return orgID, nil
}

// coldStartApplyPayload builds the organization-side event an accepted
// cold-start profile emits — organization.created (the union struct,
// display_name/primary_domain from the accepted legal_name field and the
// resolved host) when the apply minted a fresh organization, or an
// organization.updated changed_fields note when it filled an existing
// one — the ONE place that maps the applied field delta onto the
// published schema. The two shapes are different published events, not
// variants of one, so the return type is the shared events.Payload seam.
//
//nolint:ireturn // dispatches to PublicEventOrganizationCreated vs Updated by the created condition; tested directly via the interface in person_organization_payload_test.go
func coldStartApplyPayload(created bool, in ApplyColdStartProfileInput, host, by string, applied map[string]any) events.Payload {
	if created {
		displayName := fieldValue(in.Fields, "legal_name")
		if displayName == "" {
			// The org row is stored with host as its display name when no
			// legal_name was accepted (resolveOrCreateColdStartOrg's fallback),
			// so organization.created must publish the same — never an empty
			// display_name the record does not actually carry.
			displayName = host
		}
		primaryDomain := host
		source := companySourceSiteRead
		capturedBy := by
		return crmcontracts.PublicEventOrganizationCreated{
			DisplayName:   &displayName,
			PrimaryDomain: &primaryDomain,
			Source:        &source,
			CapturedBy:    &capturedBy,
		}
	}
	return crmcontracts.PublicEventOrganizationUpdated{
		ChangedFields: map[string]any{
			eventKeyDelta: applied, auditKeySource: companySourceSiteRead, auditKeySourceURL: in.SourceURL,
		},
	}
}

// resolveOrCreateColdStartOrg finds the organization the source domain
// names, or creates it (with its primary domain) when absent. It reports
// whether it created the org so the caller selects the create/update audit
// action and event.
func resolveOrCreateColdStartOrg(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, host, by string, fields []ColdStartFieldInput) (ids.OrganizationID, bool, error) {
	var orgID ids.OrganizationID
	err := tx.QueryRow(ctx,
		`SELECT organization_id FROM organization_domain WHERE domain = lower($1) AND archived_at IS NULL`,
		host).Scan(&orgID)
	if err == nil {
		return orgID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.OrganizationID{}, false, fmt.Errorf("resolve organization by domain: %w", err)
	}

	orgID = ids.New[ids.OrganizationKind]()
	// Name-source authority (ADR-0072/A118, PO-F-2a): a bare-domain fallback
	// name is provisional ('domain') so a later dossier/signature may overwrite
	// it; a scraped/confirmed legal name is authoritative ('human'), never
	// clobbered. This mirrors migration 0118's own backfill rule (a name equal
	// to the domain is 'domain', everything else 'human') applied forward at the
	// second automated domain-namer, so this site never diverges from it.
	displayName := host
	nameSource := nameSourceDomain
	if legal := fieldValue(fields, "legal_name"); legal != "" {
		displayName = legal
		nameSource = nameSourceHuman
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization (id, workspace_id, display_name, name_source, source, captured_by)
		 VALUES ($1, $2, $3, $4, 'coldstart', $5)`,
		orgID, wsID, displayName, nameSource, by); err != nil {
		return ids.OrganizationID{}, false, fmt.Errorf("insert coldstart organization: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
		 VALUES ($1, $2, lower($3), true, 'coldstart', $4)`,
		wsID, orgID, host, by); err != nil {
		return ids.OrganizationID{}, false, fmt.Errorf("insert coldstart organization domain: %w", err)
	}
	return orgID, true, nil
}

// coldStartColumns whitelists the identifier a fillEmptyOrgColumn UPDATE
// may name — values are bind parameters, the column never is.
var coldStartColumns = map[string]string{
	"legal_name": `UPDATE organization SET legal_name = $2 WHERE id = $1 AND legal_name IS NULL`,
	"industry":   `UPDATE organization SET industry = $2 WHERE id = $1 AND industry IS NULL`,
	// A scraped registered address arrives as one formatted line; it
	// fills line1 only when no structured address exists yet.
	"address": `UPDATE organization SET address_line1 = $2 WHERE id = $1 AND address_line1 IS NULL
	            AND address_city IS NULL AND address_postal_code IS NULL`,
}

// applyEvidenceFields fills the column-backed fields (only when empty) and
// upserts the evidence row for EVERY field, returning what was applied. Shared
// by the cold-start read-back and per-org enrichment so both write provenance
// identically; the caller supplies the executing principal (by) and owns the
// audit source. A re-accept refreshes an agent-captured row and never touches
// one a human has since claimed.
func applyEvidenceFields(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, orgID ids.OrganizationID, source, by string, fields []ColdStartFieldInput) (map[string]any, error) {
	return applyEvidenceFieldsWithOverwrite(ctx, tx, wsID, orgID, source, by, fields, nil)
}

func applyEvidenceFieldsWithOverwrite(
	ctx context.Context,
	tx pgx.Tx,
	wsID ids.WorkspaceID,
	orgID ids.OrganizationID,
	source string,
	by string,
	fields []ColdStartFieldInput,
	overwrite map[string]bool,
) (map[string]any, error) {
	applied := map[string]any{}
	for _, f := range fields {
		if column, backed := columnBackedColdStartFields[f.Field]; backed {
			filled, err := writeOrgColumn(ctx, tx, orgID, column, f.Value, overwrite[f.Field])
			if err != nil {
				return nil, err
			}
			if filled {
				applied[f.Field] = f.Value
				// The shared field-provenance layer (B-E02.12) records the
				// filled COLUMN's origin; the profile-field evidence row
				// below keeps the full snippet either way.
				confidence := f.Confidence
				stamp := storekit.FieldStamp{Field: column, Confidence: &confidence}
				if f.SourceURL != "" {
					// A missing source link reads as NULL, never as ''.
					stamp.EvidenceRef = &f.SourceURL
				}
				if err := storekit.StampFields(ctx, tx, "organization", orgID.UUID, source, by, []storekit.FieldStamp{stamp}); err != nil {
					return nil, err
				}
			}
		} else {
			applied[f.Field] = f.Value
		}
		// The evidence row lands for every accepted field; a re-accept
		// refreshes an agent-captured row and never touches one a human has
		// since claimed.
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_profile_field
			  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (workspace_id, organization_id, field)
			DO UPDATE SET value = EXCLUDED.value, evidence_snippet = EXCLUDED.evidence_snippet,
			              source_url = EXCLUDED.source_url, confidence = EXCLUDED.confidence,
			              source = EXCLUDED.source,
			              captured_by = EXCLUDED.captured_by, captured_at = now()
			WHERE $10 OR organization_profile_field.captured_by NOT LIKE 'human:%'`,
			wsID, orgID, f.Field, f.Value, f.EvidenceSnippet, f.SourceURL, f.Confidence, source, by, overwrite[f.Field]); err != nil {
			return nil, fmt.Errorf("upsert profile field %s: %w", f.Field, err)
		}
	}
	return applied, nil
}

func writeOrgColumn(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, column, value string, overwrite bool) (bool, error) {
	if !overwrite {
		return fillEmptyOrgColumn(ctx, tx, orgID, column, value)
	}
	queries := map[string]string{
		"legal_name": `UPDATE organization SET legal_name = $2, updated_at = now()
			WHERE id = $1 AND legal_name IS DISTINCT FROM $2`,
		"industry": `UPDATE organization SET industry = $2, updated_at = now()
			WHERE id = $1 AND industry IS DISTINCT FROM $2`,
		"address": `UPDATE organization SET address_line1 = $2, updated_at = now()
			WHERE id = $1 AND address_line1 IS DISTINCT FROM $2`,
	}
	query, ok := queries[column]
	if !ok {
		return false, fmt.Errorf("people: %q is not a coldstart-writable column", column)
	}
	tag, err := tx.Exec(ctx, query, orgID, value)
	if err != nil {
		return false, fmt.Errorf("replace %s: %w", column, err)
	}
	return tag.RowsAffected() == 1, nil
}

func fillEmptyOrgColumn(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, column, value string) (bool, error) {
	query, ok := coldStartColumns[column]
	if !ok {
		return false, fmt.Errorf("people: %q is not a coldstart-fillable column", column)
	}
	tag, err := tx.Exec(ctx, query, orgID, value)
	if err != nil {
		return false, fmt.Errorf("fill %s: %w", column, err)
	}
	return tag.RowsAffected() == 1, nil
}

func fieldValue(fields []ColdStartFieldInput, name string) string {
	for _, f := range fields {
		if f.Field == name {
			return f.Value
		}
	}
	return ""
}

func coldStartHost(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("people: coldstart source url %q has no host", rawURL)
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www."), nil
}

// UnmarshalColdStartFields decodes the staged proposal's field array —
// shared with the compose effect so both sides agree on the JSON shape.
func UnmarshalColdStartFields(raw json.RawMessage) (string, []ColdStartFieldInput, error) {
	var proposal struct {
		SourceURL string `json:"source_url"`
		Fields    []struct {
			Field           string  `json:"field"`
			Value           string  `json:"value"`
			EvidenceSnippet string  `json:"evidence_snippet"`
			SourceURL       string  `json:"source_url"`
			Confidence      float32 `json:"confidence"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return "", nil, fmt.Errorf("people: coldstart proposal payload: %w", err)
	}
	fields := make([]ColdStartFieldInput, 0, len(proposal.Fields))
	for _, f := range proposal.Fields {
		fields = append(fields, ColdStartFieldInput{
			Field: f.Field, Value: f.Value, EvidenceSnippet: f.EvidenceSnippet,
			SourceURL: f.SourceURL, Confidence: f.Confidence,
		})
	}
	return proposal.SourceURL, fields, nil
}
