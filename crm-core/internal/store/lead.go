package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/crmctx"
	"github.com/gradionhq/margince/backend/kernel/errs"
	"github.com/gradionhq/margince/backend/kernel/ids"
)

// DuplicateLeadError carries the live lead already holding an email
// (uq_lead_email_dedupe → 409, features/01 §6.2).
type DuplicateLeadError struct {
	Email      string
	ExistingID ids.UUID
}

func (e *DuplicateLeadError) Error() string        { return "lead with email " + e.Email + " already exists" }
func (e *DuplicateLeadError) Is(target error) bool { return target == errs.ErrConflict }

type CreateLeadInput struct {
	FullName        *string
	Email           *string
	Title           *string
	CompanyName     *string
	CandidateOrgKey *string
	Status          string
	OwnerID         *ids.UUID
	SourceSystem    *string
	SourceID        *string
	Source          string
}

// CreateLead inserts into the segregated lead table — never person, never
// relationship (ADR-0008: the anti-pollution guarantee is structural).
// Idempotent on (source_system, source_id): a re-import returns the
// existing row instead of erroring, so bulk sourcing can re-run.
func (s *Store) CreateLead(ctx context.Context, in CreateLeadInput) (crmcontracts.Lead, bool, error) {
	if err := require(ctx, "lead", crmctx.ActionCreate); err != nil {
		return crmcontracts.Lead{}, false, err
	}
	by, err := capturedBy(ctx)
	if err != nil {
		return crmcontracts.Lead{}, false, err
	}
	if in.Status == "" {
		in.Status = "new"
	}

	var out crmcontracts.Lead
	created := true
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := mustWorkspace(ctx)

		if in.SourceSystem != nil && in.SourceID != nil {
			var existing ids.UUID
			err := tx.QueryRow(ctx,
				`SELECT id FROM lead WHERE source_system = $1 AND source_id = $2`,
				*in.SourceSystem, *in.SourceID).Scan(&existing)
			if err == nil {
				// The replay path returns a record, so it carries the
				// read's row scope: re-importing someone else's source key
				// must not hand over their lead. Out of scope answers the
				// same 409 the unique-index race does.
				visible, verr := visibleTo(ctx, tx, "lead", existing)
				if verr != nil {
					return verr
				}
				if !visible {
					return errs.ErrConflict
				}
				created = false
				out, err = readLead(ctx, tx, existing, true)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if in.Email != nil {
			var existing ids.UUID
			err := tx.QueryRow(ctx,
				`SELECT id FROM lead WHERE email = lower($1) AND archived_at IS NULL`,
				*in.Email).Scan(&existing)
			if err == nil {
				dup := &DuplicateLeadError{Email: *in.Email}
				visible, verr := visibleTo(ctx, tx, "lead", existing)
				if verr != nil {
					return verr
				}
				if visible {
					dup.ExistingID = existing
				}
				return dup
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}

		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO lead (id, workspace_id, full_name, email, title, company_name, candidate_org_key,
			                   status, owner_id, source_system, source_id, source, captured_by)
			 VALUES ($1, $2, $3, lower($4), $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			id, wsID, in.FullName, in.Email, in.Title, in.CompanyName, in.CandidateOrgKey,
			in.Status, in.OwnerID, in.SourceSystem, in.SourceID, in.Source, by)
		if err != nil {
			// Race behind the pre-checks: the constraint name tells an
			// email dedupe hit from a concurrent same-source import — the
			// latter is a plain conflict, not a "duplicate email" (the
			// email may not even be set). No re-read here: the failed
			// INSERT aborted the transaction.
			if name, ok := uniqueViolation(err); ok {
				if name == "uq_lead_email_dedupe" {
					return &DuplicateLeadError{Email: deref(in.Email)}
				}
				return errs.ErrConflict
			}
			return err
		}

		auditID, err := audit(ctx, tx, "create", "lead", id, nil, map[string]any{"email": in.Email, "company_name": in.CompanyName})
		if err != nil {
			return err
		}
		if err := emit(ctx, tx, auditID, "lead.created", "lead", id, nil); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, false)
		return err
	})
	return out, created, err
}

func (s *Store) GetLead(ctx context.Context, id ids.UUID, includeArchived bool) (crmcontracts.Lead, error) {
	if err := require(ctx, "lead", crmctx.ActionRead); err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := ensureVisible(ctx, tx, "lead", id); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, includeArchived)
		return err
	})
	return out, err
}

type ListLeadsInput struct {
	Cursor          *string
	Limit           *int
	Status          *string
	OwnerID         *ids.UUID
	Query           *string
	IncludeArchived bool
}

func (s *Store) ListLeads(ctx context.Context, in ListLeadsInput) ([]crmcontracts.Lead, Page, error) {
	if err := require(ctx, "lead", crmctx.ActionRead); err != nil {
		return nil, Page{}, err
	}
	limit := clampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := scopeClause(ctx, arg)
	if err != nil {
		return nil, Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Status != nil {
		where = append(where, sprintf("status = $%d", arg(*in.Status)))
	}
	if in.OwnerID != nil {
		where = append(where, sprintf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, sprintf("search_tsv @@ plainto_tsquery('simple', $%d)", arg(*in.Query)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := decodeCursor(*in.Cursor)
		if err != nil {
			return nil, Page{}, err
		}
		where = append(where, sprintf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var leads []crmcontracts.Lead
	var page Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+leadColumns+` FROM lead WHERE `+strings.Join(where, " AND ")+
				sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
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
			page = Page{HasMore: true, NextCursor: encodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if leads == nil {
		leads = []crmcontracts.Lead{}
	}
	return leads, page, err
}

type UpdateLeadInput struct {
	FullName        *string
	Email           *string
	Title           *string
	CompanyName     *string
	CandidateOrgKey *string
	Status          *string // only new ↔ working here; terminal states have their own paths
	Score           *int
	OwnerID         *ids.UUID
	IfVersion       *int64
}

func (s *Store) UpdateLead(ctx context.Context, id ids.UUID, in UpdateLeadInput) (crmcontracts.Lead, error) {
	if err := require(ctx, "lead", crmctx.ActionUpdate); err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureVisible(ctx, tx, "lead", id); err != nil {
			return err
		}
		current, err := readLead(ctx, tx, id, false)
		if err != nil {
			return err
		}

		p := newPatch()
		if in.FullName != nil {
			p.set("full_name", current.FullName, *in.FullName)
		}
		if in.Email != nil {
			p.set("email", current.Email, strings.ToLower(*in.Email))
		}
		if in.Title != nil {
			p.set("title", current.Title, *in.Title)
		}
		if in.CompanyName != nil {
			p.set("company_name", current.CompanyName, *in.CompanyName)
		}
		if in.CandidateOrgKey != nil {
			p.set("candidate_org_key", current.CandidateOrgKey, *in.CandidateOrgKey)
		}
		if in.Status != nil {
			p.set("status", current.Status, *in.Status)
		}
		if in.Score != nil {
			p.set("score", current.Score, *in.Score)
		}
		if in.OwnerID != nil {
			p.set("owner_id", current.OwnerId, *in.OwnerID)
		}
		if p.empty() {
			out = current
			return nil
		}

		if err := p.apply(ctx, tx, "lead", id, in.IfVersion); err != nil {
			if name, ok := uniqueViolation(err); ok {
				if name == "uq_lead_email_dedupe" {
					return &DuplicateLeadError{Email: deref(in.Email)}
				}
				return errs.ErrConflict
			}
			return err
		}
		auditID, err := audit(ctx, tx, "update", "lead", id, p.before, p.after)
		if err != nil {
			return err
		}
		if err := emit(ctx, tx, auditID, "lead.updated", "lead", id, p.after); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, false)
		return err
	})
	return out, err
}

// DisqualifyLead is the one path enforcing "disqualified ⇒ archived"
// (DELETE /leads/{id} in the contract).
func (s *Store) DisqualifyLead(ctx context.Context, id ids.UUID) (crmcontracts.Lead, error) {
	if err := require(ctx, "lead", crmctx.ActionDelete); err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureVisible(ctx, tx, "lead", id); err != nil {
			return err
		}
		current, err := readLead(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE lead SET status = 'disqualified', archived_at = now() WHERE id = $1 AND archived_at IS NULL`,
			id); err != nil {
			return err
		}
		auditID, err := audit(ctx, tx, "archive", "lead", id,
			map[string]any{"status": current.Status}, map[string]any{"status": "disqualified"})
		if err != nil {
			return err
		}
		if err := emit(ctx, tx, auditID, "lead.disqualified", "lead", id, nil); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, true)
		return err
	})
	return out, err
}

const leadColumns = `id, workspace_id, full_name, email, title, company_name, candidate_org_key,
	status, score, owner_id, source_system, source_id, promoted_person_id, promoted_at,
	source, captured_by, version, created_at, updated_at, archived_at`

func readLead(ctx context.Context, tx pgx.Tx, id ids.UUID, includeArchived bool) (crmcontracts.Lead, error) {
	q := `SELECT ` + leadColumns + ` FROM lead WHERE id = $1`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	l, err := scanLead(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Lead{}, errs.ErrNotFound
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
		&status, &l.Score, &ownerID, &l.SourceSystem, &l.SourceId, &promotedPerson, &l.PromotedAt,
		&l.Source, &l.CapturedBy, &version, &l.CreatedAt, &l.UpdatedAt, &l.ArchivedAt)
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
