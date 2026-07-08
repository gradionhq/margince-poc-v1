// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// DuplicateLeadError carries the live lead already holding an email
// (uq_lead_email_dedupe → 409, features/01 §6.2).
type DuplicateLeadError struct {
	Email      string
	ExistingID ids.LeadID
}

func (e *DuplicateLeadError) Error() string        { return "lead with email " + e.Email + " already exists" }
func (e *DuplicateLeadError) Is(target error) bool { return target == apperrors.ErrConflict }

type CreateLeadInput struct {
	FullName        *string
	Email           *string
	Title           *string
	CompanyName     *string
	CandidateOrgKey *string
	Status          string
	OwnerID         *ids.UserID
	SourceSystem    *string
	SourceID        *string
	Source          string
}

// CreateLead inserts into the segregated lead table — never person, never
// relationship (ADR-0008: the anti-pollution guarantee is structural).
// Idempotent on (source_system, source_id): a re-import returns the
// existing row instead of erroring, so bulk sourcing can re-run.
func (s *Store) CreateLead(ctx context.Context, in CreateLeadInput) (crmcontracts.Lead, bool, error) {
	if err := auth.Require(ctx, "lead", principal.ActionCreate); err != nil {
		return crmcontracts.Lead{}, false, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Lead{}, false, err
	}
	if in.Status == "" {
		in.Status = string(LeadStatusNew)
	}
	if _, err := ParseLeadStatus(in.Status); err != nil {
		return crmcontracts.Lead{}, false, err
	}
	// Parse-don't-validate: the address normalizes once here, so the
	// dedupe probe, the insert and the audit image all see one spelling
	// (the SQL lower() stays as defense in depth).
	if in.Email != nil {
		parsed, err := values.ParseEmail(*in.Email)
		if err != nil {
			return crmcontracts.Lead{}, false, err
		}
		normalized := parsed.String()
		in.Email = &normalized
	}

	var out crmcontracts.Lead
	created := true
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := workspaceID(ctx)

		replay, err := replayedLead(ctx, tx, in)
		if err != nil {
			return err
		}
		if replay != nil {
			created, out = false, *replay
			return nil
		}
		if err := ensureLeadEmailUnclaimed(ctx, tx, in.Email); err != nil {
			return err
		}

		id := ids.New[ids.LeadKind]()
		// The initial score is the §3 fit component — a fresh lead has no
		// behavioral history yet; signal recompute moves it later.
		fitScore, _ := ScoreLead(deref(in.Title), in.Source, nil, time.Now().UTC())
		_, err = tx.Exec(ctx,
			`INSERT INTO lead (id, workspace_id, full_name, email, title, company_name, candidate_org_key,
			                   status, score, owner_id, source_system, source_id, source, captured_by)
			 VALUES ($1, $2, $3, lower($4), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			id, wsID, in.FullName, in.Email, in.Title, in.CompanyName, in.CandidateOrgKey,
			in.Status, fitScore, in.OwnerID, in.SourceSystem, in.SourceID, in.Source, by)
		if err != nil {
			// Race behind the pre-checks: the constraint name tells an
			// email dedupe hit from a concurrent same-source import — the
			// latter is a plain conflict, not a "duplicate email" (the
			// email may not even be set). No re-read here: the failed
			// INSERT aborted the transaction.
			if mapped, ok := leadUniqueViolation(err, in.Email); ok {
				return mapped
			}
			return fmt.Errorf("insert lead: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "lead", id.UUID, nil, map[string]any{"email": in.Email, "company_name": in.CompanyName})
		if err != nil {
			return fmt.Errorf("audit lead create: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "lead.created", "lead", id.UUID, nil); err != nil {
			return fmt.Errorf("emit lead.created: %w", err)
		}
		if out, err = readLead(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created lead: %w", err)
		}
		return nil
	})
	return out, created, err
}

// replayedLead resolves the (source_system, source_id) idempotency key:
// a re-import returns the existing row. The replay path returns a
// record, so it carries the read's row scope: re-importing someone
// else's source key must not hand over their lead — out of scope
// answers the same 409 the unique-index race does.
func replayedLead(ctx context.Context, tx pgx.Tx, in CreateLeadInput) (*crmcontracts.Lead, error) {
	if in.SourceSystem == nil || in.SourceID == nil {
		return nil, nil
	}
	var existing ids.LeadID
	err := tx.QueryRow(ctx,
		`SELECT id FROM lead WHERE source_system = $1 AND source_id = $2`,
		*in.SourceSystem, *in.SourceID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("probe source-key idempotency: %w", err)
	}
	visible, err := auth.VisibleTo(ctx, tx, "lead", existing.UUID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, apperrors.ErrConflict
	}
	out, err := readLead(ctx, tx, existing, storekit.IncludeArchived)
	if err != nil {
		return nil, fmt.Errorf("read replayed lead: %w", err)
	}
	return &out, nil
}

// ensureLeadEmailUnclaimed answers the live-email dedupe probe with the
// contract's 409, disclosing the existing id only when the caller could
// read that row.
func ensureLeadEmailUnclaimed(ctx context.Context, tx pgx.Tx, email *string) error {
	if email == nil {
		return nil
	}
	var existing ids.LeadID
	err := tx.QueryRow(ctx,
		`SELECT id FROM lead WHERE email = lower($1) AND archived_at IS NULL`,
		*email).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("probe email dedupe: %w", err)
	}
	dup := &DuplicateLeadError{Email: *email}
	visible, err := auth.VisibleTo(ctx, tx, "lead", existing.UUID)
	if err != nil {
		return err
	}
	if visible {
		dup.ExistingID = existing
	}
	return dup
}

func (s *Store) GetLead(ctx context.Context, id ids.LeadID, archived storekit.ArchivedFilter) (crmcontracts.Lead, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListLeadsInput struct {
	Cursor          *string
	Limit           *int
	Status          *string
	OwnerID         *ids.UserID
	Query           *string
	IncludeArchived bool
}

func (s *Store) ListLeads(ctx context.Context, in ListLeadsInput) ([]crmcontracts.Lead, storekit.Page, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, "lead", "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf("status = $%d", arg(*in.Status)))
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), "coalesce(full_name,'') || ' ' || coalesce(company_name,'')"))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var leads []crmcontracts.Lead
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+leadColumns+` FROM lead WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLead(rows)
			if err != nil {
				return err
			}
			leads = append(leads, l)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(leads) > limit {
			leads = leads[:limit]
			last := leads[len(leads)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if leads == nil {
		leads = []crmcontracts.Lead{}
	}
	return leads, page, err
}

// leadUniqueViolation maps a lead write's unique-index violation to the
// contract error: the email dedupe index answers 409 duplicate-email; any
// other unique index a plain conflict. The bool is false when err is not a
// unique violation at all, so the caller keeps its own wrapping.
func leadUniqueViolation(err error, email *string) (error, bool) {
	name, ok := storekit.UniqueViolation(err)
	if !ok {
		return nil, false
	}
	if name == "uq_lead_email_dedupe" {
		return &DuplicateLeadError{Email: deref(email)}, true
	}
	return apperrors.ErrConflict, true
}

// DisqualifyLead is the one path enforcing "disqualified ⇒ archived"
// (DELETE /leads/{id} in the contract).
func (s *Store) DisqualifyLead(ctx context.Context, id ids.LeadID) (crmcontracts.Lead, error) {
	if err := auth.Require(ctx, "lead", principal.ActionDelete); err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
			return err
		}
		// The row lock makes the status read and the update below one
		// race-free unit.
		if _, err := storekit.LockRow(ctx, tx, "lead", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		current, err := readLead(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE lead SET status = 'disqualified', archived_at = now() WHERE id = $1 AND archived_at IS NULL`,
			id); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "archive", "lead", id.UUID,
			map[string]any{"status": current.Status}, map[string]any{"status": "disqualified"})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "lead.disqualified", "lead", id.UUID, nil); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

const leadColumns = `id, workspace_id, full_name, email, title, company_name, candidate_org_key,
	status, score, score_override_reason, score_computed, owner_id, source_system, source_id,
	promoted_person_id, promoted_at, source, captured_by, version, created_at, updated_at, archived_at`

func readLead(ctx context.Context, tx pgx.Tx, id ids.LeadID, archived storekit.ArchivedFilter) (crmcontracts.Lead, error) {
	q := `SELECT ` + leadColumns + ` FROM lead WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	l, err := scanLead(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Lead{}, apperrors.ErrNotFound
	}
	return l, err
}

func scanLead(row pgx.Row) (crmcontracts.Lead, error) {
	var l crmcontracts.Lead
	var id, wsID ids.UUID
	var ownerID, promotedPerson *ids.UUID
	var email *string
	var status string
	var version int64

	err := row.Scan(&id, &wsID, &l.FullName, &email, &l.Title, &l.CompanyName, &l.CandidateOrgKey,
		&status, &l.Score, &l.ScoreOverrideReason, &l.ScoreComputed, &ownerID, &l.SourceSystem, &l.SourceId,
		&promotedPerson, &l.PromotedAt, &l.Source, &l.CapturedBy, &version, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt)
	if err != nil {
		return l, err
	}

	l.Id = openapi_types.UUID(id)
	l.WorkspaceId = openapi_types.UUID(wsID)
	l.OwnerId = uuidPtr(ownerID)
	l.PromotedPersonId = uuidPtr(promotedPerson)
	if email != nil {
		e := openapi_types.Email(*email)
		l.Email = &e
	}
	l.Status = crmcontracts.LeadStatus(status)
	l.Version = &version
	return l, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
