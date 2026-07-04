// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Manual per-record sharing (A52/ADR-0039): identity owns the grant
// rows because a grant IS access administration — platform/auth's
// visibility predicates read the table by SQL, never by import. A
// grant widens own/team base scope for exactly one record; revocation
// binds on the next query because the predicate evaluates live.

import (
	"context"
	"errors"
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

var shareableRecordTypes = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true,
}

const grantColumns = `id, record_type, record_id, subject_type, subject_id, access, granted_by, reason, expires_at, created_at`

type grantRow struct {
	ID          ids.UUID
	RecordType  string
	RecordID    ids.UUID
	SubjectType string
	SubjectID   ids.UUID
	Access      string
	GrantedBy   ids.UUID
	Reason      *string
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

func scanGrant(r pgx.Row) (grantRow, error) {
	var g grantRow
	err := r.Scan(&g.ID, &g.RecordType, &g.RecordID, &g.SubjectType, &g.SubjectID,
		&g.Access, &g.GrantedBy, &g.Reason, &g.ExpiresAt, &g.CreatedAt)
	return g, err
}

type ListGrantsInput struct {
	RecordType  *string
	RecordID    *ids.UUID
	SubjectType *string
	SubjectID   *ids.UUID
}

func (s *Service) ListRecordGrants(ctx context.Context, in ListGrantsInput) ([]grantRow, error) {
	var out []grantRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where := "(expires_at IS NULL OR expires_at > now())"
		if in.RecordType != nil {
			where += storekit.SQLf(" AND record_type = $%d", arg(*in.RecordType))
		}
		if in.RecordID != nil {
			where += storekit.SQLf(" AND record_id = $%d", arg(*in.RecordID))
		}
		if in.SubjectType != nil {
			where += storekit.SQLf(" AND subject_type = $%d", arg(*in.SubjectType))
		}
		if in.SubjectID != nil {
			where += storekit.SQLf(" AND subject_id = $%d", arg(*in.SubjectID))
		}
		rows, err := tx.Query(ctx,
			"SELECT "+grantColumns+" FROM record_grant WHERE "+where+" ORDER BY created_at DESC", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGrant(rows)
			if err != nil {
				return err
			}
			// A grant row names a row-scoped record: only grants whose
			// target the caller could read are disclosed.
			visible, err := auth.VisibleTo(ctx, tx, g.RecordType, g.RecordID)
			if err != nil {
				return err
			}
			if visible {
				out = append(out, g)
			}
		}
		return rows.Err()
	})
	return out, err
}

type CreateGrantInput struct {
	RecordType  string
	RecordID    ids.UUID
	SubjectType string
	SubjectID   ids.UUID
	Access      string
	Reason      *string
	ExpiresAt   *time.Time
}

func (s *Service) CreateRecordGrant(ctx context.Context, in CreateGrantInput) (grantRow, error) {
	if !shareableRecordTypes[in.RecordType] {
		return grantRow{}, &InvalidScopeError{Scope: "record_type " + in.RecordType}
	}
	// Sharing widens who sees the record — the grantor needs the
	// record's own write grant (the spec's manage_sharing permission is
	// not yet in the policy vocabulary; feedback/08).
	if err := auth.Require(ctx, in.RecordType, principal.ActionUpdate); err != nil {
		return grantRow{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return grantRow{}, errors.New("crmauth: only a human shares records directly; agents stage through the approval gate")
	}
	var out grantRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Scope-intersection: you can only share what you can see (H1
		// probe on the client-supplied record reference).
		if err := auth.EnsureLinkTarget(ctx, tx, in.RecordType, in.RecordID); err != nil {
			return err
		}
		subjectTable := "app_user"
		if in.SubjectType == "team" {
			subjectTable = "team"
		}
		var subjectExists bool
		if err := tx.QueryRow(ctx,
			storekit.SQLf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, subjectTable),
			in.SubjectID).Scan(&subjectExists); err != nil {
			return err
		}
		if !subjectExists {
			return apperrors.ErrNotFound
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO record_grant (workspace_id, record_type, record_id, subject_type, subject_id,
			                          access, granted_by, reason, expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+grantColumns,
			in.RecordType, in.RecordID, in.SubjectType, in.SubjectID,
			in.Access, actor.UserID, in.Reason, in.ExpiresAt)
		var err error
		if out, err = scanGrant(row); err != nil {
			if storekit.IsUniqueViolation(err) {
				return apperrors.ErrConflict
			}
			return err
		}
		_, err = storekit.Audit(ctx, tx, "record_share", in.RecordType, in.RecordID, nil, map[string]any{
			"subject_type": in.SubjectType, "subject_id": in.SubjectID, "access": in.Access,
		})
		return err
	})
	return out, err
}

func (s *Service) RevokeRecordGrant(ctx context.Context, id ids.UUID) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("crmauth: only a human revokes shares directly; agents stage through the approval gate")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		grant, err := scanGrant(tx.QueryRow(ctx,
			"SELECT "+grantColumns+" FROM record_grant WHERE id = $1", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := auth.Require(ctx, grant.RecordType, principal.ActionUpdate); err != nil {
			return err
		}
		if err := auth.EnsureLinkTarget(ctx, tx, grant.RecordType, grant.RecordID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM record_grant WHERE id = $1`, id); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "record_unshare", grant.RecordType, grant.RecordID, map[string]any{
			"subject_type": grant.SubjectType, "subject_id": grant.SubjectID, "access": grant.Access,
		}, nil)
		return err
	})
}

func wireGrant(g grantRow) crmcontracts.RecordGrant {
	out := crmcontracts.RecordGrant{
		Id:          openapi_types.UUID(g.ID),
		RecordType:  crmcontracts.RecordGrantRecordType(g.RecordType),
		RecordId:    openapi_types.UUID(g.RecordID),
		SubjectType: crmcontracts.RecordGrantSubjectType(g.SubjectType),
		SubjectId:   openapi_types.UUID(g.SubjectID),
		Access:      crmcontracts.RecordGrantAccess(g.Access),
		GrantedBy:   openapi_types.UUID(g.GrantedBy),
		Reason:      g.Reason,
		ExpiresAt:   g.ExpiresAt,
		CreatedAt:   g.CreatedAt,
	}
	return out
}
