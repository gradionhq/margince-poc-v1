// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// PromoteTrigger is the genuine-engagement vocabulary (features/01
// §6.4): the closed set of events that justify graduating a lead —
// typed, so a misspelled trigger is unrepresentable past the seam.
type PromoteTrigger string

const (
	TriggerInboundReply  PromoteTrigger = "inbound_reply"
	TriggerMeetingBooked PromoteTrigger = "meeting_booked"
	TriggerMeetingHeld   PromoteTrigger = "meeting_held"
	TriggerHumanQualify  PromoteTrigger = "human_qualify"
)

// ParsePromoteTrigger is the store-side membership check; the transport
// enum is the first line, this is the seam's own guard (an MCP or
// internal caller doesn't pass through the HTTP validator).
func ParsePromoteTrigger(raw string) (PromoteTrigger, error) {
	switch tr := PromoteTrigger(raw); tr {
	case TriggerInboundReply, TriggerMeetingBooked, TriggerMeetingHeld, TriggerHumanQualify:
		return tr, nil
	}
	return "", &values.ParseError{
		Field: "trigger", Code: "invalid_promote_trigger",
		Message: "trigger is one of inbound_reply, meeting_booked, meeting_held, human_qualify",
	}
}

// PromoteLeadInput carries the genuine-engagement trigger and the
// evidence pointer the audit row records.
type PromoteLeadInput struct {
	Trigger            string
	EvidenceActivityID *ids.ActivityID
	EvidenceNote       *string
}

// AlreadyPromotedError maps to 409: promotion happened once; the pointer
// to its outcome lives on the lead row.
type AlreadyPromotedError struct{ PersonID ids.PersonID }

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
func (s *Store) PromoteLead(ctx context.Context, id ids.LeadID, in PromoteLeadInput) (crmcontracts.Person, bool, error) {
	// Promotion mutates the lead AND writes the person side, so it needs
	// both grants — a rep who may work leads but not create contacts
	// cannot mint contacts through this door.
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return crmcontracts.Person{}, false, err
	}
	if err := auth.Require(ctx, "person", principal.ActionCreate); err != nil {
		return crmcontracts.Person{}, false, err
	}
	if _, err := ParsePromoteTrigger(in.Trigger); err != nil {
		return crmcontracts.Person{}, false, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Person{}, false, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return crmcontracts.Person{}, false, err
	}

	var person crmcontracts.Person
	merged := false
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// The lead lock comes BEFORE the promotability read: two
		// concurrent promotes of one lead must serialize here, so the
		// loser re-reads status=promoted and answers 409 instead of
		// minting a second person. IncludeArchived keeps the re-promote
		// 409-with-pointer diagnostic reachable.
		if _, err := storekit.LockRow(ctx, tx, "lead", id.UUID, storekit.IncludeArchived); err != nil {
			return err
		}
		lead, err := promotableLead(ctx, tx, id, in)
		if err != nil {
			return err
		}

		personID, mergeFields, err := s.promoteTarget(ctx, tx, lead, by, &merged)
		if err != nil {
			return err
		}
		if err := carryLeadConsent(ctx, tx, id, personID, by); err != nil {
			return fmt.Errorf("carry lead consent: %w", err)
		}

		person, err = finalizeLeadPromotion(ctx, tx, id, in, lead, personID, merged, mergeFields, active)
		return err
	})
	return person, merged, err
}

// carryLeadConsent re-points the lead's consent state to the promoted
// person (data-model §7: subject re-pointed, proof preserved), in the
// same single transaction — people's sanctioned cross-aggregate SQL
// ownership, the mergeConsent (merge.go) rule applied to promotion:
//
//   - withdrawal wins: the lead's withdrawn flips an existing person
//     grant, with an appended consent_event proof row — a state change
//     without proof would break the Art. 7(1) invariant.
//   - where the person already holds a row for a purpose, the person's
//     state stands — the lead's grant never overrides it; the colliding
//     lead row is dropped (its proof events remain).
//   - the lead's remaining rows re-point: person_id set, lead_id
//     cleared, so the carried state no longer rides the retired lead's
//     lifecycle (person_consent.lead_id cascades on lead deletion).
//
// Historical consent_event rows stay AS WRITTEN — the lead-scoped
// entries ARE the proof that consent predates promotion.
func carryLeadConsent(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, personID ids.PersonID, by string) error {
	if _, err := tx.Exec(ctx, `
		WITH flipped AS (
		  UPDATE person_consent b SET state = 'withdrawn', captured_at = $3, source = 'promotion'
		  FROM person_consent a
		  WHERE a.lead_id = $1 AND b.person_id = $2
		    AND a.purpose_id = b.purpose_id
		    AND a.state = 'withdrawn' AND b.state <> 'withdrawn'
		  RETURNING b.purpose_id
		)
		INSERT INTO consent_event (workspace_id, person_id, purpose_id, new_state, source,
		                           policy_text, policy_version, captured_at, captured_by)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, $2, purpose_id, 'withdrawn', 'promotion',
		       'withdrawal carried over from the promoted lead', 'promotion', $3, $4
		FROM flipped`,
		leadID, personID, time.Now().UTC(), by); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM person_consent a
		WHERE a.lead_id = $1 AND EXISTS (
		  SELECT 1 FROM person_consent b
		  WHERE b.person_id = $2 AND b.purpose_id = a.purpose_id)`,
		leadID, personID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE person_consent SET person_id = $2, lead_id = NULL WHERE lead_id = $1`,
		leadID, personID)
	return err
}

// finalizeLeadPromotion retires the lead and lands the write shape for the
// whole promotion: the status flip, the ONE audit row (action=promote,
// recording trigger + evidence + the resulting person), and the paired
// lead.promoted + person.* events — all inside the caller's transaction,
// still under the lead row lock taken by PromoteLead.
func finalizeLeadPromotion(ctx context.Context, tx pgx.Tx, id ids.LeadID, in PromoteLeadInput, lead crmcontracts.Lead, personID ids.PersonID, merged bool, mergeFields map[string]any, active []fieldcatalog.Column) (crmcontracts.Person, error) {
	now := time.Now().UTC()
	tag, err := tx.Exec(ctx,
		`UPDATE lead SET status = 'promoted', promoted_person_id = $2, promoted_at = $3, archived_at = $3
		 WHERE id = $1 AND archived_at IS NULL`,
		id, personID, now)
	if err != nil {
		return crmcontracts.Person{}, fmt.Errorf("mark lead promoted: %w", err)
	}
	if tag.RowsAffected() != 1 {
		// Under the row lock only this transaction can retire the
		// lead; a zero-row update means the guards above are broken.
		// Failing loudly keeps the phantom person and its events out.
		return crmcontracts.Person{}, apperrors.ErrConflict
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
	auditID, err := storekit.Audit(ctx, tx, "promote", "lead", id.UUID,
		map[string]any{"status": lead.Status}, after)
	if err != nil {
		return crmcontracts.Person{}, fmt.Errorf("audit lead promote: %w", err)
	}

	person, err := readPerson(ctx, tx, personID, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Person{}, fmt.Errorf("read promoted person: %w", err)
	}

	// lead.promoted is the first-class verb (events.md §5.5) — the
	// moment the context graph adds the node; never a lead.updated.
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, leadPromotedPayload(personID, outcome, in.Trigger, in.EvidenceActivityID)); err != nil {
		return crmcontracts.Person{}, fmt.Errorf("emit lead.promoted: %w", err)
	}
	// A fill-only merge that changed nothing has no person.updated to emit —
	// a changed_fields note with no fields would be a false claim, so skip it
	// (lead.promoted above still records the promotion). A create always emits
	// person.created.
	if personPayload := promotedPersonPayload(person, merged, mergeFields); personPayload != nil {
		if err := storekit.EmitEvent(ctx, tx, auditID, personID.UUID, personPayload); err != nil {
			return crmcontracts.Person{}, fmt.Errorf("emit %s: %w", personPayload.EventType(), err)
		}
	}
	return person, nil
}

// promotedPersonPayload builds the person-side event a lead promotion
// emits — its own verb (person.created) on a fresh person, or a
// person.updated changed_fields note carrying the fields the merge ACTUALLY
// applied when the promotion instead merged into an existing person
// (merged=true, PO-F-1). changed_fields is the real merge delta (mergeFields),
// so it reports a filled title and omits converted_from_lead_id when that was
// already set — not a fixed map that could misstate the change. A merge that
// applied nothing returns nil (no person.updated to emit). The two shapes are
// different published events, not variants of one, so the return type is the
// shared events.Payload seam rather than a single struct.
//
//nolint:ireturn // dispatches to PublicEventPersonCreated vs Updated by the merged condition; tested directly via the interface in person_organization_payload_test.go
func promotedPersonPayload(person crmcontracts.Person, merged bool, mergeFields map[string]any) events.Payload {
	if merged {
		if len(mergeFields) == 0 {
			return nil
		}
		return crmcontracts.PublicEventPersonUpdated{ChangedFields: mergeFields}
	}
	return crmcontracts.PublicEventPersonCreated{FullName: person.FullName}
}

// leadPromotedPayload builds the lead-side event a promotion emits —
// its own verb (events.md §5.5), never a lead.updated. evidenceActivityID
// is nil for a human_qualify with no linked activity; the wire field is
// then omitted rather than marshaled as null.
func leadPromotedPayload(personID ids.PersonID, outcome, trigger string, evidenceActivityID *ids.ActivityID) crmcontracts.PublicEventLeadPromoted {
	p := crmcontracts.PublicEventLeadPromoted{
		PromotedPersonId: openapi_types.UUID(personID.UUID),
		DedupeOutcome:    outcome,
		Trigger:          trigger,
	}
	if evidenceActivityID != nil {
		p.EvidenceRef = uuidPtr(&evidenceActivityID.UUID)
	}
	return p
}

// promotableLead loads the lead and enforces every promotion guard:
// visibility, the once-only rule, live status, minimal identity, and
// in-scope evidence. Archived leads resolve here so a re-promote answers
// 409 with the outcome pointer instead of a misleading 404; a
// disqualified (archived, unpromoted) lead stays 404 like any archived
// row.
func promotableLead(ctx context.Context, tx pgx.Tx, id ids.LeadID, in PromoteLeadInput) (crmcontracts.Lead, error) {
	if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
		return crmcontracts.Lead{}, err
	}
	// An internal read that builds the promoted person; its result is not
	// returned to the wire as a lead, so it carries no custom columns (nil).
	lead, err := readLead(ctx, tx, id, storekit.IncludeArchived, nil)
	if err != nil {
		return crmcontracts.Lead{}, fmt.Errorf("read lead before promote: %w", err)
	}
	if lead.Status == crmcontracts.LeadStatusPromoted {
		e := &AlreadyPromotedError{}
		if lead.PromotedPersonId != nil {
			e.PersonID = ids.From[ids.PersonKind](ids.UUID(*lead.PromotedPersonId))
		}
		return crmcontracts.Lead{}, e
	}
	if lead.ArchivedAt != nil {
		return crmcontracts.Lead{}, apperrors.ErrNotFound
	}
	if lead.FullName == nil && lead.Email == nil {
		return crmcontracts.Lead{}, &PromoteNeedsIdentityError{}
	}
	if in.EvidenceActivityID != nil {
		// The evidence must be a real, in-scope activity — a promotion
		// justified by a record the promoter cannot see is not evidence.
		if err := auth.EnsureActivityVisible(ctx, tx, in.EvidenceActivityID.UUID); err != nil {
			return crmcontracts.Lead{}, err
		}
	}
	return lead, nil
}

// promoteTarget resolves where the lead lands: the §1.3 dedupe path — a
// live person already holding the lead's email is merged into, anything
// else creates. Returns the person id, sets *merged, and (on the merge path)
// the fields the merge actually applied so the person.updated event reports
// the true delta (nil on the create path).
func (s *Store) promoteTarget(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead, by string, merged *bool) (ids.PersonID, map[string]any, error) {
	if lead.Email != nil {
		var existing ids.PersonID
		err := tx.QueryRow(ctx,
			`SELECT person_id FROM person_email WHERE email = lower($1) AND archived_at IS NULL`,
			string(*lead.Email)).Scan(&existing)
		switch {
		case err == nil:
			// Merging returns the person, so it is a read: a match the
			// promoter cannot see answers a bare conflict, not the record.
			visible, verr := auth.VisibleTo(ctx, tx, "person", existing.UUID)
			if verr != nil {
				return ids.PersonID{}, nil, verr
			}
			if !visible {
				return ids.PersonID{}, nil, apperrors.ErrConflict
			}
			*merged = true
			mergeFields, merr := s.mergeLeadIntoPerson(ctx, tx, lead, existing)
			return existing, mergeFields, merr
		case !errors.Is(err, pgx.ErrNoRows):
			return ids.PersonID{}, nil, fmt.Errorf("probe person email dedupe: %w", err)
		}
	}

	name := deref(lead.FullName)
	if name == "" {
		// Identity was checked upstream, so an email exists; a person
		// needs SOME name until enrichment fills it.
		name = string(*lead.Email)
	}
	wsID := workspaceID(ctx)
	id := ids.New[ids.PersonKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, title, owner_id, source, captured_by, converted_from_lead_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, wsID, name, lead.Title, uuidPtrToIDs(lead.OwnerId), lead.Source, by, ids.UUID(lead.Id)); err != nil {
		return ids.PersonID{}, nil, fmt.Errorf("insert promoted person: %w", err)
	}
	if lead.Email != nil {
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, position, source, captured_by)
			 VALUES ($1, $2, lower($3), 'work', true, 1, $4, $5)`,
			wsID, id, string(*lead.Email), lead.Source, by); err != nil {
			return ids.PersonID{}, nil, fmt.Errorf("insert promoted person email: %w", err)
		}
	}
	return id, nil, nil
}

// mergeLeadIntoPerson is the non-lossy merge half: the person gains the
// origin pointer and any identity the lead has that the person lacks
// (fill-only — a promotion never overwrites human-curated contact data).
// It returns the fields the merge actually applied (the patch's after map),
// so person.updated.changed_fields reports the real delta — not a fixed
// converted_from_lead_id that lies when the field was already set, and never
// omitting a title it just filled. A no-op merge returns a nil map.
func (s *Store) mergeLeadIntoPerson(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead, personID ids.PersonID) (map[string]any, error) {
	lock, err := storekit.LockRow(ctx, tx, "person", personID.UUID, storekit.LiveOnly)
	if err != nil {
		return nil, fmt.Errorf("lock merge-target person: %w", err)
	}
	// A fill-only decision read, never the wire — core columns suffice.
	current, err := readPerson(ctx, tx, personID, storekit.LiveOnly, nil)
	if err != nil {
		return nil, fmt.Errorf("read merge-target person: %w", err)
	}
	p := storekit.NewPatch()
	if current.ConvertedFromLeadId == nil {
		p.Set("converted_from_lead_id", nil, ids.UUID(lead.Id))
	}
	if current.Title == nil && lead.Title != nil {
		p.Set("title", nil, *lead.Title)
	}
	if p.Empty() {
		// A no-op fill-only merge: no columns changed. Return an empty (not
		// nil) map so the caller reads "no delta" via len == 0 and skips the
		// person.updated event, without a nil-nil return.
		return map[string]any{}, nil
	}
	if err := p.ApplyLocked(ctx, tx, lock); err != nil {
		return nil, err
	}
	return p.After(), nil
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
