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

// RequireGrantedForEmails suppresses unless EVERY recipient resolves to
// a person — or a live, unpromoted lead (E12.20) — with an active
// granted consent for the named purpose. Default-deny in all
// directions: an unknown purpose key, an address neither subject
// carries, state unknown, and state withdrawn all block. A DOI purpose
// additionally demands the confirmed round-trip on the proof log — a
// granted-but-unconfirmed row does not send.
func (g *Gate) RequireGrantedForEmails(ctx context.Context, recipients []string, purposeKey string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("consent: a send needs at least one recipient: %w", apperrors.ErrConsentNotGranted)
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
		for _, email := range recipients {
			var granted bool
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
				)`, email, purposeID, requiresDOI).Scan(&granted)
			if err != nil {
				return err
			}
			if !granted {
				// The refusal names the address, not the person's consent
				// history — the caller typed the address, so no new
				// information is disclosed.
				return fmt.Errorf("consent: no active %q grant for %s: %w", purposeKey, email, apperrors.ErrConsentNotGranted)
			}
		}
		return nil
	})
}
