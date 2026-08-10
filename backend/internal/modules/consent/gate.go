// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Gate is the default-deny outbound suppression check (B-EP07.12):
// spelled once here, injected into every outbound surface by the
// composition root. The question is always per PURPOSE — a grant for a
// different purpose authorizes nothing.
type Gate struct {
	store *Store
}

func NewGate(store *Store) *Gate {
	return &Gate{store: store}
}

// RequireGrantedForEmails is the address-shaped spelling of the gate, kept
// because every mail surface asks in addresses (activities/email.go). It is a
// THIN WRAPPER and owns no rule of its own: mail and a messaging channel must
// not be able to drift into two default-deny gates, because the one that stops
// applying looks exactly like the one that passes.
func (g *Gate) RequireGrantedForEmails(ctx context.Context, recipients []string, purposeKey string) error {
	return g.RequireGrantedForRecipients(ctx, connector.EmailRecipients(recipients), purposeKey)
}

// RequireGrantedForRecipients suppresses unless EVERY recipient resolves to
// a subject with an active granted consent for the named purpose. A mail
// recipient resolves to a person — or a live, unpromoted lead (E12.20); a
// channel recipient resolves to a person through their channel identity
// (person_channel_identity), which is the only subject a channel identity can
// bind (0146). Default-deny in all directions: an unknown purpose key, a
// recipient neither subject carries, state unknown, and state withdrawn all
// block. A DOI purpose additionally demands the confirmed round-trip on the
// proof log — a granted-but-unconfirmed row does not send.
func (g *Gate) RequireGrantedForRecipients(ctx context.Context, recipients []connector.Recipient, purposeKey string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("consent: a send needs at least one recipient: %w", apperrors.ErrConsentNotGranted)
	}
	for _, r := range recipients {
		if err := r.Validate(); err != nil {
			// A malformed recipient is a caller DEFECT, not an answer about
			// anybody's consent, and it is reported as the fault it is. Dressed
			// up as ErrConsentNotGranted it would park the send with a reason an
			// operator reads as "this person opted out", and the bug that named
			// nobody would look like a customer's choice.
			return fmt.Errorf("consent: this recipient cannot be put to the gate: %w", err)
		}
	}
	purposeKey = strings.TrimSpace(strings.ToLower(purposeKey))
	return database.WithWorkspaceTx(ctx, g.store.pool, func(tx pgx.Tx) error {
		var purpose PurposeRow
		err := tx.QueryRow(ctx,
			`SELECT id, key, label, class, requires_double_opt_in
			 FROM consent_purpose WHERE key = $1 AND archived_at IS NULL`,
			purposeKey).Scan(&purpose.ID, &purpose.Key, &purpose.Label, &purpose.Class, &purpose.RequiresDOI)
		if err != nil {
			// Unknown purpose ⇒ nothing can be granted under it.
			return fmt.Errorf("consent: purpose %q is not defined: %w", purposeKey, apperrors.ErrConsentNotGranted)
		}
		for _, r := range recipients {
			granted, err := grantedForRecipient(ctx, tx, r, purpose)
			if err != nil {
				return err
			}
			if !granted {
				// The refusal names the recipient, not the person's consent
				// history. For MAIL that discloses nothing: the caller supplied
				// the address it is asking about. The channel arm cannot make the
				// same claim — the channel path resolves its recipient
				// server-side from the conversation, so the caller never held the
				// account id — which is why recipientLabel keeps the channel
				// spelling non-identifying.
				return fmt.Errorf("consent: no active %q grant for %s: %w",
					purposeKey, recipientLabel(r), apperrors.ErrConsentNotGranted)
			}
		}
		return nil
	})
}

// grantedForRecipient answers the one recipient's question.
//
// A recipient that resolves to a PERSON is answered by VerdictForPerson — the
// same code the guard endpoint serves, so the preview a composer shows and the
// check that fires at transmit cannot drift. Two implementations of one
// question are two questions, and the one that stops matching looks exactly
// like the one that still does.
//
// A recipient that resolves only to an unpromoted LEAD falls through to the
// grant predicate below. A lead carries no qualifying events and no §7(3) flag
// — those hang off a person — so for a lead the class model has nothing extra
// to say and the recorded grant IS the whole answer.
func grantedForRecipient(ctx context.Context, tx pgx.Tx, r connector.Recipient, purpose PurposeRow) (bool, error) {
	personID, found, err := resolvePerson(ctx, tx, r)
	if err != nil {
		return false, err
	}
	if !found {
		return grantedForLead(ctx, tx, r, purpose.ID, purpose.RequiresDOI)
	}
	verdict, err := VerdictForPerson(ctx, tx, personID, purpose)
	if err != nil {
		return false, err
	}
	// Only an explicit allow sends. Unknown is not a soft yes: it means nobody
	// has decided, and default-deny is the whole posture.
	if verdict.State != VerdictAllowed {
		return false, nil
	}
	if err := stampDerivedBasis(ctx, tx, personID, verdict); err != nil {
		return false, err
	}
	return true, nil
}

// stampDerivedBasis records a basis the gate worked out for itself, before the
// send relies on it (ADR-0098 D2, Art 5(2)).
//
// A lawful basis nobody wrote down is an assertion, and the controller carries
// the burden of showing it. A basis read from a stored row needs nothing: it is
// already the record.
func stampDerivedBasis(ctx context.Context, tx pgx.Tx, personID string, verdict Verdict) error {
	if !verdict.QualifyingDerived || verdict.Qualifying == nil {
		return nil
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	return RecordDerivedQualifyingEvent(ctx, tx, personID, *verdict.Qualifying, by)
}

// resolvePerson finds the person behind a recipient, through whichever
// identity the recipient carries.
//
// Only a LIVE identity resolves. An archived address is one somebody detached
// from a record — it is uniquely held only among live rows (uq_person_email_dedupe
// is partial on archived_at IS NULL), so the same string can sit archived on one
// person and live on another. Picking either would bind this send's whole verdict
// to an identity nobody currently holds, and the answer would be about the wrong
// human.
//
// AMBIGUITY REFUSES rather than picks. A bare LIMIT 1 over more than one live
// match is a silent choice between two people, made by row order — which is the
// one thing a default-deny gate must never do. The dedupe index makes a live
// duplicate impossible for email in a healthy schema; this refuses anyway,
// because a gate that trusts an invariant it does not check is a gate that stops
// applying the moment the invariant slips.
func resolvePerson(ctx context.Context, tx pgx.Tx, r connector.Recipient) (string, bool, error) {
	var rows pgx.Rows
	var err error
	if r.Channel != nil {
		// Person-only by construction: a channel identity binds a Person and
		// nothing else (0146 has no lead arm).
		rows, err = tx.Query(ctx, `
			SELECT DISTINCT p.id FROM person_channel_identity pci
			JOIN person p ON p.id = pci.person_id AND p.archived_at IS NULL
			WHERE pci.provider = $1 AND pci.channel_user_id = $2 AND pci.archived_at IS NULL
			LIMIT 2`, r.Channel.Provider, r.Channel.ChannelUserID)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT DISTINCT p.id FROM person_email pe
			JOIN person p ON p.id = pe.person_id AND p.archived_at IS NULL
			WHERE lower(pe.email) = lower($1) AND pe.archived_at IS NULL
			LIMIT 2`, r.Email)
	}
	if err != nil {
		return "", false, fmt.Errorf("consent: resolve the recipient: %w", err)
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", false, fmt.Errorf("consent: resolve the recipient: %w", err)
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("consent: resolve the recipient: %w", err)
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0], true, nil
	default:
		return "", false, fmt.Errorf(
			"consent: this recipient resolves to more than one live contact, so no consent answer is about one person: %w",
			apperrors.ErrConsentNotGranted)
	}
}

// grantedForLead is the unpromoted-lead arm (E12.20).
//
// It is only reached when resolvePerson found no person, so a channel
// recipient never arrives here: a channel identity binds a Person and nothing
// else (0146 has no lead arm), and one that resolves to nobody is a recipient
// with no subject — which default-deny refuses rather than guesses at.
//
// The predicate is the recorded grant, the named purpose, and the DOI
// round-trip where the purpose demands one. A lead carries no qualifying
// events and no §7(3) flag, so there is nothing here for the class model to
// add.
func grantedForLead(ctx context.Context, tx pgx.Tx, r connector.Recipient, purposeID string, requiresDOI bool) (bool, error) {
	if r.Channel != nil {
		return false, nil
	}
	var granted bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM lead l
		  JOIN person_consent pc ON pc.lead_id = l.id AND pc.purpose_id = $2
		  WHERE lower(l.email) = lower($1) AND l.archived_at IS NULL
		    AND pc.state = 'granted'
		    AND (NOT $3::boolean OR EXISTS (
		      SELECT 1 FROM consent_event ce
		      WHERE ce.lead_id = l.id AND ce.purpose_id = $2
		        AND ce.new_state = 'granted' AND ce.double_opt_in_confirmed_at IS NOT NULL))
		)`, r.Email, purposeID, requiresDOI).Scan(&granted)
	if err != nil {
		return false, fmt.Errorf("consent: read the lead's grant: %w", err)
	}
	return granted, nil
}

// recipientLabel names a refused recipient in its own vocabulary: the address
// for mail, the bare provider for a channel.
//
// The channel spelling carries NO identifier — neither the account id nor the
// username. This text becomes the detail of a 409 (httperr copies err.Error()
// into it), and a channel account id is an opaque third-party identifier the
// caller never supplied: the reply path resolves the recipient server-side from
// the conversation precisely so a caller cannot name one. A refusal is not the
// place to hand one back. This is narrower than it may read — the activity's
// own source key carries the chat id, so a timeline reader is not being denied
// something it could not otherwise reach; what this refuses is minting the
// identifier into an error string for anyone who can provoke one.
//
// A username would be no better, and worse in its own way: a handle can be
// released and re-claimed, so a refusal quoting one could name a different human
// than the one it refused. The caller is looking at the conversation it asked
// about, so the provider is the part it does not already know.
func recipientLabel(r connector.Recipient) string {
	if r.Channel != nil {
		return "this " + r.Channel.Provider + " recipient"
	}
	return r.Email
}
