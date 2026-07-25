// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The project read paths: single-row get, the filtered keyset list, and
// the one column list + scanner every project read shares.

package deals

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

func (s *Store) GetProject(ctx context.Context, id ids.ProjectID, archived storekit.ArchivedFilter) (crmcontracts.Project, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		return crmcontracts.Project{}, err
	}
	active, err := s.activeColumnsFor(ctx, projectObject)
	if err != nil {
		return crmcontracts.Project{}, err
	}
	var out crmcontracts.Project
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, projectObject, id.UUID); err != nil {
			return err
		}
		out, err = readProject(ctx, tx, id, archived, active)
		return err
	})
	return out, err
}

type ListProjectsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	OrganizationID  *ids.OrganizationID
	OwnerID         *ids.UserID
	Phase           *string
	Key             *string
	IncludeArchived bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
}

// projectQuickFindExpr is the quick-find substring target. A human looking
// for a project reaches for its name OR the handle they write in subject
// lines, so both are folded into one expression — the weighted search_tsv
// the same clause matches already indexes the pair.
const projectQuickFindExpr = `(coalesce(name,'') || ' ' || coalesce(key,''))`

// projectListFields is the project list's core sortable vocabulary.
var projectListFields = map[string]string{
	"created_at":       storekit.KindTimestamp,
	"updated_at":       storekit.KindTimestamp,
	"last_activity_at": storekit.KindTimestamp,
	"name":             fieldcatalog.TypeText,
	"target_end_date":  fieldcatalog.TypeDate,
}

func (s *Store) ListProjects(ctx context.Context, in ListProjectsInput) ([]crmcontracts.Project, storekit.Page, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumnsFor(ctx, projectObject)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(in.Sort, storekit.SortVocabulary(projectListFields, active))
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, projectObject, "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	where = appendProjectFilters(where, in, arg)
	cfClauses, err := storekit.CustomFilterClauses(active, in.CustomFilters, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where = append(where, cfClauses...)
	if in.Cursor != nil && *in.Cursor != "" {
		clause, err := sorted.KeysetClause(*in.Cursor, arg)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, clause)
	}

	var projects []crmcontracts.Project
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+projectColumns+storekit.SelectSuffix(active)+sorted.CursorKeySuffix()+
				` FROM project WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var cursorKeys []*string
		if projects, cursorKeys, err = scanProjectPage(rows, active, sorted); err != nil {
			return err
		}
		if len(projects) > limit {
			projects = projects[:limit]
			last := projects[len(projects)-1]
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if projects == nil {
		projects = []crmcontracts.Project{}
	}
	return projects, page, err
}

// scanProjectPage drains one list query's rows: each project plus, under a
// non-default sort, the row's cursor key.
func scanProjectPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Project, []*string, error) {
	var projects []crmcontracts.Project
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		p, err := scanProject(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		projects = append(projects, p)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return projects, cursorKeys, nil
}

// appendProjectFilters translates the caller's list filters into WHERE
// clauses (the cf_ filters and the keyset cursor stay in ListProjects).
func appendProjectFilters(where []string, in ListProjectsInput, arg func(any) int) []string {
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), projectQuickFindExpr))
	}
	if in.OrganizationID != nil {
		where = append(where, storekit.SQLf("organization_id = $%d", arg(*in.OrganizationID)))
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Phase != nil {
		where = append(where, storekit.SQLf("phase = $%d", arg(*in.Phase)))
	}
	// The key is matched case-insensitively because that is how its
	// uniqueness index is built — a lookup that disagreed with the
	// constraint would report "not found" for a key that cannot be created.
	if in.Key != nil {
		where = append(where, storekit.SQLf("lower(key) = lower($%d)", arg(*in.Key)))
	}
	return where
}

const projectColumns = `id, workspace_id, name, key, organization_id, owner_id, phase, closed_reason,
	description, started_at, target_end_date, ended_at, last_activity_at,
	source, captured_by, version, created_at, updated_at, archived_at`

// readProject resolves one project row; active names the custom-field
// columns to carry alongside the core ones — nil for internal decision
// reads whose result never reaches the wire.
func readProject(ctx context.Context, tx pgx.Tx, id ids.ProjectID, archived storekit.ArchivedFilter, active []fieldcatalog.Column) (crmcontracts.Project, error) {
	q := `SELECT ` + projectColumns + storekit.SelectSuffix(active) + ` FROM project WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	p, err := scanProject(tx.QueryRow(ctx, q, id), active)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Project{}, apperrors.ErrNotFound
	}
	return p, err
}

// scanProject scans core + active custom columns; extra receives any
// trailing expressions the caller's SELECT appended.
func scanProject(row pgx.Row, active []fieldcatalog.Column, extra ...any) (crmcontracts.Project, error) {
	var p crmcontracts.Project
	var id, wsID, orgID ids.UUID
	var ownerID *ids.UUID
	var phase string
	var startedAt, targetEnd, endedAt *time.Time
	var version int64

	dests := []any{
		&id, &wsID, &p.Name, &p.Key, &orgID, &ownerID, &phase, &p.ClosedReason,
		&p.Description, &startedAt, &targetEnd, &endedAt, &p.LastActivityAt,
		&p.Source, &p.CapturedBy, &version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt,
	}
	cf := storekit.ScanDests(active)
	if err := row.Scan(append(append(dests, cf...), extra...)...); err != nil {
		return p, err
	}
	if values := storekit.ExtractValues(active, cf); len(values) > 0 {
		p.AdditionalProperties = values
	}

	p.Id = openapi_types.UUID(id)
	p.WorkspaceId = openapi_types.UUID(wsID)
	p.OrganizationId = openapi_types.UUID(orgID)
	p.OwnerId = uuidPtr(ownerID)
	projectPhase := crmcontracts.ProjectPhase(phase)
	p.Phase = &projectPhase
	if startedAt != nil {
		p.StartedAt = &openapi_types.Date{Time: *startedAt}
	}
	if targetEnd != nil {
		p.TargetEndDate = &openapi_types.Date{Time: *targetEnd}
	}
	if endedAt != nil {
		p.EndedAt = &openapi_types.Date{Time: *endedAt}
	}
	p.Version = &version
	return p, nil
}
