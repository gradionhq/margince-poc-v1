// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// Data-subject requests (Art. 15/16/17, B-E11.30): the compliance
// workflow rows the DPO works through. Admin-mediated and human-only at
// the transport (x-agent-access); status transitions demand a
// resolution before a request closes. No dsr.* family exists in the
// events.md closed catalog, so these ride the audit-only lane
// ratified in events.md §5.3c, like the other compliance-config surfaces.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The wire field names a DSR ValidationError carries. A client tells a
// stale-transition refusal apart from a missing-answer one on this exact
// string (details.errors[].field), so each is spelled once here rather than
// retyped at every raise site.
const (
	fieldStatus     = "status"
	fieldSubjectRef = "subject_ref"
)

// illegalTransition is raised from both guards — the pre-erase check and the
// conditional UPDATE that loses a concurrent race — which must name the same
// field and reason, or a client stops recognising the race it already handles.
func illegalTransition(from, to string) *ValidationError {
	return &ValidationError{
		Field:  fieldStatus,
		Reason: from + " → " + to + " is not a legal transition",
	}
}

const dsrColumns = `id, kind, status, subject_ref, assignee_id, due_at, resolution, created_at`

// dsrSelectByID is the single-row fetch shared by GetDSR and UpdateDSR — one
// spelling so the projected columns cannot drift between the two paths.
const dsrSelectByID = "SELECT " + dsrColumns + " FROM data_subject_request WHERE id = $1"

type dsrRow struct {
	// ID is the data_subject_request case id — a compliance workflow row,
	// not a kernel entity, so it stays untyped.
	ID         ids.UUID
	Kind       string
	Status     string
	SubjectRef string
	AssigneeID *ids.UserID
	DueAt      time.Time
	Resolution *string
	CreatedAt  time.Time
}

func scanDSR(r pgx.Row) (dsrRow, error) {
	var d dsrRow
	err := r.Scan(&d.ID, &d.Kind, &d.Status, &d.SubjectRef, &d.AssigneeID, &d.DueAt, &d.Resolution, &d.CreatedAt)
	return d, err
}

// dsrTransitions is the closed status machine: open → in_progress →
// fulfilled|rejected, with a direct open→closed shortcut. A closed
// request never reopens (a new concern is a new request).
var dsrTransitions = map[string]map[string]bool{
	"open":        {"in_progress": true, "fulfilled": true, "rejected": true},
	"in_progress": {"fulfilled": true, "rejected": true},
}

// requireDSRAdmin gates the DSR case queue. A request row names a data
// subject (subject_ref is their email/name), so beyond the person grant
// the caller must be a human with an unbounded row scope — the same bar
// ListAuditLog carries. A scoped rep must not enumerate or read the queue
// of everyone else's data-subject requests.
func requireDSRAdmin(ctx context.Context, action principal.Action) error {
	if err := auth.Require(ctx, "person", action); err != nil {
		return err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman || !auth.Unbounded(actor) {
		return apperrors.ErrPermissionDenied
	}
	return nil
}

// ListDSRs walks the case queue newest-id-last. status narrows to one
// queue state ("" = no filter); the contract publishes the filter, so the
// store implements it rather than returning everything.
func (s *Store) ListDSRs(ctx context.Context, limit *int, cursor string, status string) ([]dsrRow, storekit.Page, error) {
	if err := requireDSRAdmin(ctx, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	bounded := storekit.ClampLimit(limit)
	var out []dsrRow
	var page storekit.Page
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		sql := "SELECT " + dsrColumns + " FROM data_subject_request WHERE true"
		if cursor != "" {
			after, err := ids.Parse(cursor)
			if err != nil {
				return &ValidationError{Field: "cursor", Reason: "malformed"}
			}
			sql += storekit.SQLf(" AND id > $%d", arg(after))
		}
		if status != "" {
			sql += storekit.SQLf(" AND status = $%d", arg(status))
		}
		sql += storekit.SQLf(" ORDER BY id LIMIT $%d", arg(bounded+1))
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDSR(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > bounded {
			out = out[:bounded]
			page = storekit.Page{HasMore: true, NextCursor: out[bounded-1].ID.String()}
		}
		return nil
	})
	return out, page, err
}

type CreateDSRInput struct {
	Kind       string
	SubjectRef string
	AssigneeID *ids.UserID
	DueAt      time.Time
}

func (s *Store) CreateDSR(ctx context.Context, in CreateDSRInput) (dsrRow, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return dsrRow{}, err
	}
	if strings.TrimSpace(in.SubjectRef) == "" {
		return dsrRow{}, &ValidationError{Field: fieldSubjectRef, Reason: "required"}
	}
	var out dsrRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO data_subject_request (workspace_id, kind, subject_ref, assignee_id, due_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			RETURNING `+dsrColumns,
			in.Kind, strings.TrimSpace(in.SubjectRef), in.AssigneeID, in.DueAt)
		var err error
		if out, err = scanDSR(row); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", "data_subject_request", out.ID, nil, map[string]any{
			"kind": in.Kind, fieldSubjectRef: in.SubjectRef, "due_at": in.DueAt,
		})
		return err
	})
	return out, err
}

// GetDSR reads one request (staff surface — the person.update gate the
// whole DSR surface carries).
func (s *Store) GetDSR(ctx context.Context, id ids.UUID) (dsrRow, error) {
	if err := requireDSRAdmin(ctx, principal.ActionUpdate); err != nil {
		return dsrRow{}, err
	}
	var out dsrRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDSR(tx.QueryRow(ctx,
			dsrSelectByID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	return out, err
}

type UpdateDSRInput struct {
	Status     *string
	AssigneeID *ids.UserID
	Resolution *string
}

// hasResolution reports whether an update carries (or the row already
// stores) an actual answer — a nil-only check would accept "" or
// whitespace-only text as a resolution, which is exactly the "closing needs
// an answer" rule this guards against.
func hasResolution(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}

// validateDSRUpdate is the one spelling of every UpdateDSR precondition:
// the closed transition map and the "closing needs an answer" rule. It is
// called twice — inside UpdateDSR's own transaction (the authoritative
// gate, every caller must clear it) and by the handler ahead of fulfilling
// an erasure (an early refusal, so a request that could never legally
// close never triggers the irreversible erase).
func validateDSRUpdate(current dsrRow, in UpdateDSRInput) *ValidationError {
	if in.Status == nil || *in.Status == current.Status {
		return nil
	}
	if !dsrTransitions[current.Status][*in.Status] {
		return illegalTransition(current.Status, *in.Status)
	}
	if (*in.Status == "fulfilled" || *in.Status == "rejected") &&
		!hasResolution(in.Resolution) && !hasResolution(current.Resolution) {
		return &ValidationError{Field: "resolution", Reason: "closing a request needs its answer"}
	}
	return nil
}

func (s *Store) UpdateDSR(ctx context.Context, id ids.UUID, in UpdateDSRInput) (dsrRow, error) {
	if err := requireDSRAdmin(ctx, principal.ActionUpdate); err != nil {
		return dsrRow{}, err
	}
	var out dsrRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanDSR(tx.QueryRow(ctx,
			dsrSelectByID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if verr := validateDSRUpdate(current, in); verr != nil {
			return verr
		}
		sql := `
			UPDATE data_subject_request SET
			  status = coalesce($2, status),
			  assignee_id = coalesce($3, assignee_id),
			  resolution = coalesce($4, resolution)
			WHERE id = $1`
		args := []any{id, in.Status, in.AssigneeID, in.Resolution}
		if in.Status != nil {
			// Nothing holds this row locked between the read above and the
			// write below, so require it to still be in the state we just
			// validated the transition against — a status change that lands
			// in that window (another officer closing or rejecting the same
			// request) is refused as illegal rather than silently overwritten.
			args = append(args, current.Status)
			sql += storekit.SQLf(" AND status = $%d", len(args))
		}
		sql += " RETURNING " + dsrColumns
		row := tx.QueryRow(ctx, sql, args...)
		if out, err = scanDSR(row); err != nil {
			if in.Status != nil && errors.Is(err, pgx.ErrNoRows) {
				return illegalTransition(current.Status, *in.Status)
			}
			return err
		}
		_, err = storekit.Audit(ctx, tx, "update", "data_subject_request", id, map[string]any{
			fieldStatus: current.Status,
		}, map[string]any{
			fieldStatus: out.Status, "resolution": in.Resolution != nil,
		})
		return err
	})
	return out, err
}

func wireDSR(d dsrRow) crmcontracts.DataSubjectRequest {
	out := crmcontracts.DataSubjectRequest{
		Id:         openapi_types.UUID(d.ID),
		Kind:       crmcontracts.DataSubjectRequestKind(d.Kind),
		Status:     crmcontracts.DataSubjectRequestStatus(d.Status),
		SubjectRef: d.SubjectRef,
		DueAt:      d.DueAt,
		Resolution: d.Resolution,
		CreatedAt:  d.CreatedAt,
	}
	if d.AssigneeID != nil {
		assignee := openapi_types.UUID(d.AssigneeID.UUID)
		out.AssigneeId = &assignee
	}
	return out
}
