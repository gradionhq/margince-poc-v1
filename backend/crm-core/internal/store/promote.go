package store

import (
	"context"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// PromoteLeadInput carries the genuine-engagement trigger (features/01
// §6.4 — the transport already rejected cold_outbound_no_reply) and the
// evidence pointer the audit row records.
type PromoteLeadInput struct {
	Trigger            string // inbound_reply | meeting_booked | meeting_held | human_qualify
	EvidenceActivityID *ids.UUID
	EvidenceNote       *string
}

// AlreadyPromotedError maps to 409: promotion happened once; the pointer
// to its outcome lives on the lead row.
type AlreadyPromotedError struct{ PersonID ids.UUID }

func (e *AlreadyPromotedError) Error() string { return "lead is already promoted" }

// PromoteNeedsIdentityError maps to 422: a lead with neither a name nor
// an email cannot become a person worth having.
type PromoteNeedsIdentityError struct{}

func (e *PromoteNeedsIdentityError) Error() string {
	return "lead has neither full_name nor email; enrich it before promoting"
}

// PromoteLead graduates a lead into the clean core (features/01 §6.4,
// ADR-0008): if the lead's email matches a live person it MERGES into
// that person — never a duplicate — else it creates one, carrying the
// lead's provenance, owner and identity. The lead is marked
// status=promoted, stamped with the outcome pointer, and archived off the
// lead list, all in one transaction with ONE audit row (action=promote on
// the lead, recording trigger + evidence + the resulting person) and the
// first-class lead.promoted event alongside the person.* it caused.
func (s *Store) PromoteLead(ctx context.Context, id ids.UUID, in PromoteLeadInput) (crmcontracts.Person, bool, error) {
	// Promotion mutates the lead AND writes the person side, so it needs
	// both grants — a rep who may work leads but not create contacts
	// cannot mint contacts through this door.
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return crmcontracts.Person{}, false, err
	}
	if err := auth.Require(ctx, "person", principal.ActionCreate); err != nil {
		return crmcontracts.Person{}, false, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Person{}, false, err
	}

	var person crmcontracts.Person
	merged := false
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "lead", id); err != nil {
			return err
		}
		// Archived leads resolve here so a re-promote answers 409 with
		// the outcome pointer instead of a misleading 404; a disqualified
		// (archived, unpromoted) lead stays 404 like any archived row.
		lead, err := readLead(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if lead.Status == crmcontracts.LeadStatusPromoted {
			e := &AlreadyPromotedError{}
			if lead.PromotedPersonId != nil {
				e.PersonID = ids.UUID(*lead.PromotedPersonId)
			}
			return e
		}
		if lead.ArchivedAt != nil {
			return apperrors.ErrNotFound
		}
		if lead.FullName == nil && lead.Email == nil {
			return &PromoteNeedsIdentityError{}
		}
		if in.EvidenceActivityID != nil {
			// The evidence must be a real, in-scope activity — a promotion
			// justified by a record the promoter cannot see is not evidence.
			if err := ensureActivityVisible(ctx, tx, *in.EvidenceActivityID); err != nil {
				return err
			}
		}

		personID, err := s.promoteTarget(ctx, tx, lead, by, &merged)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		if _, err := tx.Exec(ctx,
			`UPDATE lead SET status = 'promoted', promoted_person_id = $2, promoted_at = $3, archived_at = $3
			 WHERE id = $1 AND archived_at IS NULL`,
			id, personID, now); err != nil {
			return err
		}

		outcome := "created"
		if merged {
			outcome = "merged"
		}
		after := map[string]any{
			"status": "promoted", "promoted_person_id": personID,
			"trigger": in.Trigger, "dedupe_outcome": outcome,
		}
		if in.EvidenceActivityID != nil {
			after["evidence_activity_id"] = *in.EvidenceActivityID
		}
		if in.EvidenceNote != nil {
			after["evidence_note"] = *in.EvidenceNote
		}
		auditID, err := storekit.Audit(ctx, tx, "promote", "lead", id,
			map[string]any{"status": lead.Status}, after)
		if err != nil {
			return err
		}

		person, err = readPerson(ctx, tx, personID, false)
		if err != nil {
			return err
		}

		// lead.promoted is the first-class verb (events.md §5.5) — the
		// moment the context graph adds the node; never a lead.updated.
		if err := storekit.Emit(ctx, tx, auditID, "lead.promoted", "lead", id, map[string]any{
			"promoted_person_id": personID,
			"dedupe_outcome":     outcome,
			"trigger":            in.Trigger,
			"evidence_ref":       in.EvidenceActivityID,
		}); err != nil {
			return err
		}
		personEvent, personPayload := "person.created", map[string]any{"full_name": person.FullName}
		if merged {
			personEvent, personPayload = "person.updated", map[string]any{"converted_from_lead_id": id}
		}
		return storekit.Emit(ctx, tx, auditID, personEvent, "person", personID, personPayload)
	})
	return person, merged, err
}

// promoteTarget resolves where the lead lands: the §1.3 dedupe path — a
// live person already holding the lead's email is merged into, anything
// else creates. Returns the person id and sets *merged.
func (s *Store) promoteTarget(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead, by string, merged *bool) (ids.UUID, error) {
	if lead.Email != nil {
		var existing ids.UUID
		err := tx.QueryRow(ctx,
			`SELECT person_id FROM person_email WHERE email = lower($1) AND archived_at IS NULL`,
			string(*lead.Email)).Scan(&existing)
		switch {
		case err == nil:
			// Merging returns the person, so it is a read: a match the
			// promoter cannot see answers a bare conflict, not the record.
			visible, verr := auth.VisibleTo(ctx, tx, "person", existing)
			if verr != nil {
				return ids.Nil, verr
			}
			if !visible {
				return ids.Nil, apperrors.ErrConflict
			}
			*merged = true
			return existing, s.mergeLeadIntoPerson(ctx, tx, lead, existing)
		case !errors.Is(err, pgx.ErrNoRows):
			return ids.Nil, err
		}
	}

	name := deref(lead.FullName)
	if name == "" {
		// Identity was checked upstream, so an email exists; a person
		// needs SOME name until enrichment fills it.
		name = string(*lead.Email)
	}
	wsID := storekit.MustWorkspace(ctx)
	id := ids.NewV7()
	if _, err := tx.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, title, owner_id, source, captured_by, converted_from_lead_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, wsID, name, lead.Title, uuidPtrToIDs(lead.OwnerId), lead.Source, by, ids.UUID(lead.Id)); err != nil {
		return ids.Nil, err
	}
	if lead.Email != nil {
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, position, source, captured_by)
			 VALUES ($1, $2, lower($3), 'work', true, 1, $4, $5)`,
			wsID, id, string(*lead.Email), lead.Source, by); err != nil {
			return ids.Nil, err
		}
	}
	return id, nil
}

// mergeLeadIntoPerson is the non-lossy merge half: the person gains the
// origin pointer and any identity the lead has that the person lacks
// (fill-only — a promotion never overwrites human-curated contact data).
func (s *Store) mergeLeadIntoPerson(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead, personID ids.UUID) error {
	current, err := readPerson(ctx, tx, personID, false)
	if err != nil {
		return err
	}
	p := storekit.NewPatch()
	if current.ConvertedFromLeadId == nil {
		p.Set("converted_from_lead_id", nil, ids.UUID(lead.Id))
	}
	if current.Title == nil && lead.Title != nil {
		p.Set("title", nil, *lead.Title)
	}
	if p.Empty() {
		return nil
	}
	return p.Apply(ctx, tx, "person", personID, nil)
}

// uuidPtrToIDs converts the contract's optional UUID back to the kernel
// type for SQL args.
func uuidPtrToIDs(u *openapi_types.UUID) *ids.UUID {
	if u == nil {
		return nil
	}
	converted := ids.UUID(*u)
	return &converted
}
