// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The lead read spine: the one column list every lead read shares, the
// single-row read, and the scanner. Split out of lead.go so each file
// stays one concept under the 500-LOC cap.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// liveOnlyClause narrows a read to unarchived rows, spelled once.
const liveOnlyClause = ` AND archived_at IS NULL`

const leadColumns = `id, workspace_id, full_name, email, title, company_name, candidate_org_key,
	linkedin_url, status, score, score_override_reason, score_computed, owner_id, project_id, source_system, source_id,
	promoted_person_id, promoted_at, source, captured_by, version, created_at, updated_at, archived_at`

// readLead resolves one lead row; active names the custom-field columns
// to carry alongside the core ones — nil for internal decision reads whose
// result never reaches the wire.
func readLead(ctx context.Context, tx pgx.Tx, id ids.LeadID, archived storekit.ArchivedFilter, active []fieldcatalog.Column) (crmcontracts.Lead, error) {
	q := `SELECT ` + leadColumns + storekit.SelectSuffix(active) + ` FROM lead WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += liveOnlyClause
	}
	l, err := scanLead(tx.QueryRow(ctx, q, id), active)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Lead{}, apperrors.ErrNotFound
	}
	return l, err
}

// scanLead scans core + active custom columns. Lead lists page by a
// (created_at, id) WHERE cursor, not a SELECT cursor-key suffix, so there
// are no trailing expressions to scan — unlike scanPerson's keyset page.
func scanLead(row pgx.Row, active []fieldcatalog.Column) (crmcontracts.Lead, error) {
	var l crmcontracts.Lead
	var id, wsID ids.UUID
	var ownerID, projectID, promotedPerson *ids.UUID
	var email *string
	var status string
	var version int64

	dests := []any{
		&id, &wsID, &l.FullName, &email, &l.Title, &l.CompanyName, &l.CandidateOrgKey,
		&l.LinkedinUrl, &status, &l.Score, &l.ScoreOverrideReason, &l.ScoreComputed, &ownerID, &projectID, &l.SourceSystem, &l.SourceId,
		&promotedPerson, &l.PromotedAt, &l.Source, &l.CapturedBy, &version, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt,
	}
	cf := storekit.ScanDests(active)
	if err := row.Scan(append(dests, cf...)...); err != nil {
		return l, err
	}
	if values := storekit.ExtractValues(active, cf); len(values) > 0 {
		l.AdditionalProperties = values
	}

	l.Id = openapi_types.UUID(id)
	l.WorkspaceId = openapi_types.UUID(wsID)
	l.OwnerId = uuidPtr(ownerID)
	l.ProjectId = uuidPtr(projectID)
	l.PromotedPersonId = uuidPtr(promotedPerson)
	if email != nil {
		e := openapi_types.Email(*email)
		l.Email = &e
	}
	l.Status = crmcontracts.LeadStatus(status)
	l.Version = &version
	return l, nil
}
