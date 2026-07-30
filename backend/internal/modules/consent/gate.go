// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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
		var purposeID string
		var requiresDOI bool
		err := tx.QueryRow(ctx,
			`SELECT id, requires_double_opt_in FROM consent_purpose WHERE key = $1 AND archived_at IS NULL`,
			purposeKey).Scan(&purposeID, &requiresDOI)
		if err != nil {
			// Unknown purpose ⇒ nothing can be granted under it.
			return fmt.Errorf("consent: purpose %q is not defined: %w", purposeKey, apperrors.ErrConsentNotGranted)
		}
		for _, r := range recipients {
			granted, err := grantedForRecipient(ctx, tx, r, purposeID, requiresDOI)
			if err != nil {
				return err
			}
			if !granted {
				// The refusal names the recipient, not the person's consent
				// history: the caller already holds the address or the channel
				// identity it asked about, so no new information is disclosed.
				return fmt.Errorf("consent: no active %q grant for %s: %w",
					purposeKey, recipientLabel(r), apperrors.ErrConsentNotGranted)
			}
		}
		return nil
	})
}

// grantedForRecipient answers the one recipient's question. The two arms differ
// only in how they reach a subject; the grant predicate — active state, the
// named purpose, and the DOI round-trip where the purpose demands one — is the
// same clause in both, because they are the same rule about the same rows.
func grantedForRecipient(ctx context.Context, tx pgx.Tx, r connector.Recipient, purposeID string, requiresDOI bool) (bool, error) {
	var granted bool
	if r.Channel != nil {
		// Person-only by construction: a channel identity binds a Person and
		// nothing else (0146 has no lead arm), so there is no second subject to
		// union in here the way the address arm unions the lead.
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1
			  FROM person_channel_identity pci
			  JOIN person p ON p.id = pci.person_id AND p.archived_at IS NULL
			  JOIN person_consent pc ON pc.person_id = p.id AND pc.purpose_id = $3
			  WHERE pci.provider = $1 AND pci.channel_user_id = $2
			    AND pci.archived_at IS NULL
			    AND pc.state = 'granted'
			    AND (NOT $4::boolean OR EXISTS (
			      SELECT 1 FROM consent_event ce
			      WHERE ce.person_id = p.id AND ce.purpose_id = $3
			        AND ce.new_state = 'granted' AND ce.double_opt_in_confirmed_at IS NOT NULL))
			)`, r.Channel.Provider, r.Channel.ChannelUserID, purposeID, requiresDOI).Scan(&granted)
		return granted, err
	}
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM person_email pe
		  JOIN person p ON p.id = pe.person_id AND p.archived_at IS NULL
		  JOIN person_consent pc ON pc.person_id = p.id AND pc.purpose_id = $2
		  WHERE lower(pe.email) = lower($1)
		    AND pc.state = 'granted'
		    AND (NOT $3::boolean OR EXISTS (
		      SELECT 1 FROM consent_event ce
		      WHERE ce.person_id = p.id AND ce.purpose_id = $2
		        AND ce.new_state = 'granted' AND ce.double_opt_in_confirmed_at IS NOT NULL))
		) OR EXISTS (
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
	return granted, err
}

// recipientLabel names a refused recipient in its own vocabulary: the address
// for mail, provider:account for a channel. The channel spelling omits the
// username deliberately — a handle can be released and re-claimed, so a refusal
// quoting one could name a different human than the one it refused.
func recipientLabel(r connector.Recipient) string {
	if r.Channel != nil {
		return r.Channel.Provider + ":" + r.Channel.ChannelUserID
	}
	return r.Email
}
