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

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

type CreateLeadInput struct {
	FullName        *string
	Email           *string
	Title           *string
	CompanyName     *string
	CandidateOrgKey *string
	LinkedInURL     *string
	Status          string
	OwnerID         *ids.UserID
	ProjectID       *ids.ProjectID
	SourceSystem    *string
	SourceID        *string
	Source          string
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (customfields.go).
	CustomFields map[string]any
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
	in, err = normalizedCreateLeadInput(in)
	if err != nil {
		return crmcontracts.Lead{}, false, err
	}
	active, err := s.activeColumns(ctx, "lead")
	if err != nil {
		return crmcontracts.Lead{}, false, err
	}

	var out crmcontracts.Lead
	created := true
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := workspaceID(ctx)

		replay, err := replayedLead(ctx, tx, in, active)
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
		if err := ensureLeadLinkedInUnclaimed(ctx, tx, in.LinkedInURL); err != nil {
			return err
		}

		id := ids.New[ids.LeadKind]()
		// The initial score is the §3 fit component — a fresh lead has no
		// behavioral history yet; signal recompute moves it later.
		fitScore, _ := ScoreLead(deref(in.Title), in.Source, nil, time.Now().UTC())
		cfCols, cfHolders, cfArgs := storekit.InsertFragments(active, in.CustomFields, 17)
		if in.ProjectID != nil {
			if err := auth.EnsureLinkTarget(ctx, tx, "project", in.ProjectID.UUID); err != nil {
				return err
			}
		}
		args := []any{
			id, wsID, in.FullName, in.Email, in.Title, in.CompanyName, in.CandidateOrgKey,
			in.LinkedInURL, in.Status, fitScore, in.OwnerID, in.ProjectID, in.SourceSystem, in.SourceID, in.Source, by,
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO lead (id, workspace_id, full_name, email, title, company_name, candidate_org_key,
			                   linkedin_url, status, score, owner_id, project_id, source_system, source_id, source, captured_by`+cfCols+`)
			 VALUES ($1, $2, $3, lower($4), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16`+cfHolders+`)`,
			append(args, cfArgs...)...)
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
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventLeadCreated{}); err != nil {
			return fmt.Errorf("emit lead.created: %w", err)
		}
		if out, err = readLead(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read created lead: %w", err)
		}
		return nil
	})
	return out, created, err
}

// normalizedCreateLeadInput is CreateLead's parse-don't-validate step:
// status defaults and is membership-checked, and the two identity keys —
// email and LinkedIn URL — normalize ONCE here, so the dedupe probes,
// the insert and the audit image all see one spelling (the SQL lower()
// stays as defense in depth).
func normalizedCreateLeadInput(in CreateLeadInput) (CreateLeadInput, error) {
	if in.Status == "" {
		in.Status = string(LeadStatusNew)
	}
	if _, err := parseWritableLeadStatus(in.Status); err != nil {
		return CreateLeadInput{}, err
	}
	if in.Email != nil {
		parsed, err := values.ParseEmail(*in.Email)
		if err != nil {
			return CreateLeadInput{}, err
		}
		normalized := parsed.String()
		in.Email = &normalized
	}
	if in.LinkedInURL != nil {
		normalized, err := NormalizeLinkedInURL(*in.LinkedInURL)
		if err != nil {
			return CreateLeadInput{}, err
		}
		in.LinkedInURL = &normalized
	}
	return in, nil
}

// replayedLead resolves the (source_system, source_id) idempotency key:
// a re-import returns the existing row. The replay path returns a
// record, so it carries the read's row scope: re-importing someone
// else's source key must not hand over their lead — out of scope
// answers the same 409 the unique-index race does.
func replayedLead(ctx context.Context, tx pgx.Tx, in CreateLeadInput, active []fieldcatalog.Column) (*crmcontracts.Lead, error) {
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
	out, err := readLead(ctx, tx, existing, storekit.IncludeArchived, active)
	if err != nil {
		return nil, fmt.Errorf("read replayed lead: %w", err)
	}
	return &out, nil
}

// FindLeadByLinkedInURL is the E12.11 exact-match dedupe probe: the
// earliest-captured live lead holding this profile URL (the canonical
// original when duplicates slipped in), or nil when the workspace has none.
// The lookup normalizes its input the way CreateLead stores it, so the
// comparison is exact by construction. Returning a record makes this a
// read: the caller's row scope applies, and an out-of-scope match reads
// as no match — the capture path then warns on what the caller could see,
// never on hidden rows (idx_lead_linkedin is a lookup index, not UNIQUE:
// merging duplicates is a human decision, so the probe warns, it does not
// refuse).
func (s *Store) FindLeadByLinkedInURL(ctx context.Context, rawURL string) (*crmcontracts.Lead, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return nil, err
	}
	normalized, err := NormalizeLinkedInURL(rawURL)
	if err != nil {
		return nil, err
	}

	args := []any{normalized}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := scopeOrAllRows(ctx, "lead", "", arg)
	if err != nil {
		return nil, err
	}

	var out *crmcontracts.Lead
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// A dedupe probe for the capture path — its result is not rendered
		// with custom fields, so no catalog columns are carried (nil active).
		l, err := scanLead(tx.QueryRow(ctx,
			`SELECT `+leadColumns+` FROM lead
			 WHERE linkedin_url = $1 AND archived_at IS NULL AND `+scope+`
			 ORDER BY created_at ASC, id ASC LIMIT 1`, args...), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("probe linkedin dedupe: %w", err)
		}
		out = &l
		return nil
	})
	return out, err
}

func (s *Store) GetLead(ctx context.Context, id ids.LeadID, archived storekit.ArchivedFilter) (crmcontracts.Lead, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return crmcontracts.Lead{}, err
	}
	active, err := s.activeColumns(ctx, "lead")
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, archived, active)
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
	// CapturedByKind filters on the captured_by prefix (ADR-0075/A121 §3a).
	CapturedByKind *string
	// AiWritten filters on whether an AI wrote into the record (§3a).
	AiWritten *bool
}

func (s *Store) ListLeads(ctx context.Context, in ListLeadsInput) ([]crmcontracts.Lead, storekit.Page, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, "lead")
	if err != nil {
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
	kindClause, ok, err := capturedByKindClause(in.CapturedByKind, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if ok {
		where = append(where, kindClause)
	}
	if ai := aiWrittenClause(in.AiWritten, "lead", arg); ai != "" {
		where = append(where, ai)
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
			`SELECT `+leadColumns+storekit.SelectSuffix(active)+` FROM lead WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLead(rows, active)
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
	active, err := s.activeColumns(ctx, "lead")
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
			return err
		}
		// The row lock makes the status read and the update below one
		// race-free unit.
		if _, err := storekit.LockRow(ctx, tx, "lead", id.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		current, err := readLead(ctx, tx, id, storekit.LiveOnly, active)
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
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventLeadDisqualified{}); err != nil {
			return err
		}
		out, err = readLead(ctx, tx, id, storekit.IncludeArchived, active)
		return err
	})
	return out, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
