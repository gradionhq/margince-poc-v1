// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// The one answer to "may we write to this person for this purpose", and the
// reason for it.
//
// It exists so the composer's preview and the dispatcher's transmit-time check
// cannot drift. Two implementations of the same question are two questions, and
// the one that stops matching looks exactly like the one that still does — a
// preview that says "allowed" over a send that refuses is worse than no preview
// at all, because the rep has already written the message.
//
// ADR-0098 is the ruling this encodes: not every purpose is consent-gated.
// Individual business correspondence is not advertising under UWG §7, and its
// lawful basis is Art 6(1)(b)/(f). Consent, with its German evidence standard,
// belongs to marketing. Treating a reply to somebody who wrote to us as a
// consent violation is a frame that is legally wrong and that every rep
// correctly ignores, which is worse than useless.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Class decides which question a purpose answers to.
type Class string

const (
	// ClassBusinessCorrespondence is 1:1 mail: replies, meeting follow-ups,
	// answers to their own question. Allowed on a qualifying event, no consent
	// object consulted.
	ClassBusinessCorrespondence Class = "business_correspondence"
	// ClassTransactional is invoices and account notices — allowed while the
	// business relationship exists.
	ClassTransactional Class = "transactional"
	// ClassMarketing is »Werbung« read broadly: newsletters, campaigns, and
	// feedback requests. Express consent with double-opt-in proof, or the
	// §7(3) existing-customer flag.
	ClassMarketing Class = "marketing"
	// ClassPhoneOutreach is specced and dormant — no call provider is wired.
	// The purpose exists so the model is complete, not because a path uses it.
	ClassPhoneOutreach Class = "phone_outreach"
)

// Verdict is the effective answer for one person and one purpose.
type Verdict struct {
	// State is allowed, blocked, or unknown. Unknown is not a soft block: it
	// means no decision is recorded, and the offered action is to REQUEST
	// consent rather than to send anyway.
	State string
	// Reason is the answer in the reader's words — "she wrote to you on 2 May",
	// "opt-out 12 Jul", "no consent recorded". A verdict a rep cannot explain
	// to the person in front of them is not usable.
	Reason string
	// Qualifying names the event that flipped correspondence to allowed, when
	// one did. Recording WHICH event is what makes the Art 6(1)(f) balancing
	// accountable rather than merely asserted.
	Qualifying *QualifyingEvent
}

const (
	// VerdictAllowed proceeds.
	VerdictAllowed = "allowed"
	// VerdictBlocked refuses, and Reason says why.
	VerdictBlocked = "blocked"
	// VerdictUnknown has no decision recorded either way.
	VerdictUnknown = "unknown"
)

// QualifyingEvent is the deterministic thing on the record that makes business
// correspondence lawful.
type QualifyingEvent struct {
	Kind             string
	OccurredAt       time.Time
	SourceEntityType string
	SourceEntityID   string
	Note             string
}

// PurposeRow is one configured purpose, as both callers read it.
type PurposeRow struct {
	ID          string
	Key         string
	Label       string
	Class       Class
	RequiresDOI bool
}

// VerdictForPerson is THE decision. Both the guard endpoint and the transmit
// gate call it, so a preview and a send answer with the same code.
//
// The order of the checks is the ruling, and it is not rearrangeable: an
// objection is evaluated FIRST and overrides every basis below it, including
// legitimate interest, including a qualifying event, including §7(3). For
// direct marketing Art 21(2)–(3) is absolute — there is no balancing and no
// override toggle, so there is no branch here that can reach past a
// suppression.
func VerdictForPerson(ctx context.Context, tx pgx.Tx, personID string, purpose PurposeRow) (Verdict, error) {
	suppressed, at, err := objectionStands(ctx, tx, personID, purpose.ID)
	if err != nil {
		return Verdict{}, err
	}
	if suppressed {
		return Verdict{
			State:  VerdictBlocked,
			Reason: fmt.Sprintf("they objected on %s, and an objection overrides every other basis", at.Format("2 Jan 2006")),
		}, nil
	}

	switch purpose.Class {
	case ClassTransactional:
		// Art 6(1)(b): the contract itself is the basis. Nothing to consult.
		return Verdict{State: VerdictAllowed, Reason: "account and contract notices need no consent"}, nil

	case ClassBusinessCorrespondence:
		event, found, err := latestQualifyingEvent(ctx, tx, personID)
		if err != nil {
			return Verdict{}, err
		}
		if !found {
			// No inbound, no inquiry, no deal, no recorded exchange. There is
			// nothing here to balance, so this is not the easy Art 6(1)(f) case
			// and the honest answer is that nobody has decided.
			return Verdict{
				State:  VerdictUnknown,
				Reason: "they have never written to you and no deal or inquiry connects you",
			}, nil
		}
		return Verdict{
			State:      VerdictAllowed,
			Reason:     qualifyingReason(event),
			Qualifying: &event,
		}, nil

	case ClassPhoneOutreach:
		// Dormant by decision: the purpose exists so the model is complete. A
		// surface that offered it would offer a path nothing implements.
		return Verdict{State: VerdictBlocked, Reason: "no call path is configured"}, nil

	default:
		return marketingVerdict(ctx, tx, personID, purpose)
	}
}

// marketingVerdict is the strict arm, unchanged in strictness by ADR-0098:
// express consent with the DOI round-trip, or the §7(3) existing-customer flag
// with all four of its conditions on the record. There is no legitimate-interest
// escape for marketing email, B2C or B2B, and the product does not offer the
// toggle.
func marketingVerdict(ctx context.Context, tx pgx.Tx, personID string, purpose PurposeRow) (Verdict, error) {
	state, granted, err := recordedState(ctx, tx, personID, purpose.ID, purpose.RequiresDOI)
	if err != nil {
		return Verdict{}, err
	}
	if granted {
		return Verdict{State: VerdictAllowed, Reason: "they gave consent, with the confirmation on file"}, nil
	}
	if state == string(StateWithdrawn) {
		return Verdict{State: VerdictBlocked, Reason: "they withdrew consent for this purpose"}, nil
	}
	if state == string(StateGranted) && purpose.RequiresDOI {
		// Granted but never confirmed. The BGH evidence standard is the whole
		// point of double opt-in, so an unconfirmed grant is not proof and does
		// not send.
		return Verdict{
			State:  VerdictBlocked,
			Reason: "consent was recorded but never confirmed by the double opt-in",
		}, nil
	}
	flagged, err := existingCustomerFlag(ctx, tx, personID)
	if err != nil {
		return Verdict{}, err
	}
	if flagged {
		return Verdict{State: VerdictAllowed, Reason: "existing customer under UWG §7(3), with the sale and opt-out notice on file"}, nil
	}
	return Verdict{State: VerdictUnknown, Reason: "no consent recorded"}, nil
}

func qualifyingReason(event QualifyingEvent) string {
	when := event.OccurredAt.Format("2 Jan")
	switch event.Kind {
	case "inbound_message":
		return fmt.Sprintf("they wrote to you on %s", when)
	case "inquiry":
		return fmt.Sprintf("they made an inquiry on %s", when)
	case "active_deal":
		return "an open deal connects you"
	default:
		return fmt.Sprintf("a recorded exchange on %s: %s", when, event.Note)
	}
}

// objectionStands asks whether an opt-out, unsubscribe or Art 21 objection is
// on the record — for this purpose or globally.
func objectionStands(ctx context.Context, tx pgx.Tx, personID, purposeID string) (bool, time.Time, error) {
	var at time.Time
	err := tx.QueryRow(ctx, `
		SELECT pc.updated_at
		FROM person_consent pc
		WHERE pc.person_id = $1 AND pc.purpose_id = $2 AND pc.state = 'withdrawn'
		LIMIT 1`, personID, purposeID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, fmt.Errorf("read the objection state: %w", err)
	}
	return true, at, nil
}

// latestQualifyingEvent reads the most recent recorded event that makes
// correspondence lawful.
func latestQualifyingEvent(ctx context.Context, tx pgx.Tx, personID string) (QualifyingEvent, bool, error) {
	var event QualifyingEvent
	var sourceType, sourceID, note *string
	err := tx.QueryRow(ctx, `
		SELECT kind, occurred_at, source_entity_type, source_entity_id, note
		FROM consent_qualifying_event
		WHERE person_id = $1
		ORDER BY occurred_at DESC
		LIMIT 1`, personID).Scan(&event.Kind, &event.OccurredAt, &sourceType, &sourceID, &note)
	if errors.Is(err, pgx.ErrNoRows) {
		return QualifyingEvent{}, false, nil
	}
	if err != nil {
		return QualifyingEvent{}, false, fmt.Errorf("read the qualifying event: %w", err)
	}
	if sourceType != nil {
		event.SourceEntityType = *sourceType
	}
	if sourceID != nil {
		event.SourceEntityID = *sourceID
	}
	if note != nil {
		event.Note = *note
	}
	return event, true, nil
}

// recordedState reads the person's own decision for this purpose, and whether
// it satisfies the DOI round-trip when the purpose demands one.
func recordedState(ctx context.Context, tx pgx.Tx, personID, purposeID string, requiresDOI bool) (string, bool, error) {
	var state string
	var granted bool
	err := tx.QueryRow(ctx, `
		SELECT pc.state,
		       pc.state = 'granted' AND (NOT $3::boolean OR EXISTS (
		         SELECT 1 FROM consent_event ce
		         WHERE ce.person_id = pc.person_id AND ce.purpose_id = pc.purpose_id
		           AND ce.new_state = 'granted' AND ce.double_opt_in_confirmed_at IS NOT NULL))
		FROM person_consent pc
		WHERE pc.person_id = $1 AND pc.purpose_id = $2`,
		personID, purposeID, requiresDOI).Scan(&state, &granted)
	if errors.Is(err, pgx.ErrNoRows) {
		return string(StateUnknown), false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the recorded consent state: %w", err)
	}
	return state, granted, nil
}

// existingCustomerFlag reads the UWG §7(3) flag. The DDL already refuses a row
// without the opt-out notice, so a live row here IS all four conditions.
func existingCustomerFlag(ctx context.Context, tx pgx.Tx, personID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM consent_existing_customer_flag
		  WHERE person_id = $1 AND revoked_at IS NULL)`, personID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read the existing-customer flag: %w", err)
	}
	return exists, nil
}

// PurposesForGuard lists the purposes a guard read reports on, in a fixed order
// so the rail card does not reshuffle between visits.
func PurposesForGuard(ctx context.Context, tx pgx.Tx) ([]PurposeRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, key, label, class, requires_double_opt_in
		FROM consent_purpose
		WHERE archived_at IS NULL
		ORDER BY CASE class
		           WHEN 'business_correspondence' THEN 0
		           WHEN 'transactional' THEN 1
		           WHEN 'marketing' THEN 2
		           ELSE 3
		         END, key`)
	if err != nil {
		return nil, fmt.Errorf("list the consent purposes: %w", err)
	}
	defer rows.Close()
	var out []PurposeRow
	for rows.Next() {
		var p PurposeRow
		if err := rows.Scan(&p.ID, &p.Key, &p.Label, &p.Class, &p.RequiresDOI); err != nil {
			return nil, fmt.Errorf("scan a consent purpose: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the consent purposes: %w", err)
	}
	return out, nil
}
