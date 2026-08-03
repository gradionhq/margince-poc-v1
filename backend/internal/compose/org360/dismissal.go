// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// Suggestion dismissals: the rep saying "not this, not now".
//
// Per user, because advice one rep has judged is not advice their colleague
// has seen. Keyed on the suggestion's evidence fingerprint, so it stays gone
// while the situation holds and re-arms by itself when the evidence changes.
//
// A row is written ONLY for a fingerprint the rules currently produce for this
// account and this caller. That is what bounds the table: one row per suggestion
// a human actually clicked, so it grows with use rather than with whatever a
// client chooses to send. No retention cap is needed, and no judgment is ever
// deleted to make room for another.
//
// The two obvious alternatives are both wrong. Accepting any
// well-formed fingerprint makes this an authenticated write sink — every distinct
// value is a row nothing will ever collect. Capping the stored count instead
// silently deletes the earliest judgments on an account with more suggestions
// than the cap, so a rep working through it has advice they already dismissed
// come back. Verifying on write is the only version with neither failure.
//
// suggestion_dismissal is view state, not a record fact: written on a click,
// readable by nobody but its own user, actionable by no consumer. It carries
// no audit row and no outbox event — the same ruling as user_record_view.

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DismissSuggestion records that this human does not want this advice.
func (s *Service) DismissSuggestion(ctx context.Context, orgID ids.OrganizationID, fingerprint string) error {
	// An agent has no opinion to record, and consuming a human's dismissal on
	// their behalf would silence advice they never saw.
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Anything that names a record is gated: dismissing advice about an
		// account the caller cannot read would confirm it exists.
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		// After the record gate, so a caller who may not read this account gets the
		// same 404 whatever they put in the body. The shape check alone is
		// account-independent, but the raisesSuggestion call below is not — keeping
		// both on this side of the gate means there is one order to reason about.
		if !isFingerprint(fingerprint) {
			return httperr.Validation("fingerprint", "malformed",
				"dismiss a suggestion by the fingerprint it was served with, unchanged")
		}
		raises, err := s.raisesSuggestion(ctx, tx, orgID, now, fingerprint)
		if err != nil {
			return err
		}
		if !raises {
			// Nothing to dismiss, and that is a success rather than an error. Either
			// the situation resolved between the render and the click — in which case
			// the suggestion is already gone and storing a row would change nothing —
			// or the fingerprint was never served, in which case there is nothing to
			// silence. Saying which would answer a question the caller has no business
			// asking.
			return nil
		}
		// The row's existence IS the dismissal, so a repeat click is a no-op. Nothing
		// reads a dismissal's age, and the id is a v7 uuid, so the instant stays
		// recoverable for support without a column nobody reads.
		_, err = tx.Exec(ctx, `
			INSERT INTO suggestion_dismissal (workspace_id, user_id, organization_id, fingerprint)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (workspace_id, user_id, organization_id, fingerprint) DO NOTHING`,
			storekit.MustWorkspace(ctx), userID, orgID, fingerprint)
		if err != nil {
			return fmt.Errorf("record the suggestion dismissal: %w", err)
		}
		return nil
	})
}

// raisesSuggestion reports whether this account currently raises the suggestion
// this fingerprint identifies, for this caller.
//
// It re-derives the candidates through the SAME function the card serves them
// from, so "a suggestion the rep could have dismissed" has one definition. A
// second spelling here would drift, and the failure would be silent: a dismissal
// that stores a row the card never filters.
func (s *Service) raisesSuggestion(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, fingerprint string,
) (bool, error) {
	facts, err := readSignalFacts(ctx, tx, orgID)
	if err != nil {
		return false, err
	}
	// The dismissal has no assembled page to take the stage from, so it reads
	// it — off the hot path, and once.
	lifecycle, err := organizationLifecycle(ctx, tx, orgID)
	if err != nil {
		return false, err
	}
	in, err := gatherSuggestionInputs(ctx, tx, orgID, now, facts, lifecycle)
	if err != nil {
		return false, err
	}
	for _, suggestion := range candidateSuggestions(orgID, now, in) {
		if suggestion.Fingerprint == fingerprint {
			return true, nil
		}
	}
	return false, nil
}

// fingerprintPattern is the shape fingerprint() produces: a sha256 digest in
// lowercase hex.
//
// The shape is checked before the value is looked for, so a malformed body is a
// stated 422 rather than a silent no-op the caller cannot tell from a hit. It
// also rejects the NUL byte Postgres would otherwise turn into a 500 for what is
// a client mistake.
var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func isFingerprint(value string) bool {
	return fingerprintPattern.MatchString(value)
}

// dismissedFingerprints asks which of THESE suggestions this caller has already
// judged.
//
// It asks about the candidates in hand rather than reading the whole stored set,
// so the page read is bounded by the suggestions this account raises.
//
// The user_id predicate is explicit in SQL: RLS binds the workspace, so without
// it one rep's judgment would silence their colleague's suggestions.
func (s *Service) dismissedFingerprints(
	ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, candidates []string,
) (map[string]bool, error) {
	userID, err := actingUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT fingerprint FROM suggestion_dismissal
		WHERE user_id = $1 AND organization_id = $2 AND fingerprint = ANY($3)`,
		userID, orgID, candidates)
	if err != nil {
		return nil, fmt.Errorf("read the suggestion dismissals: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		out[fingerprint] = true
	}
	return out, rows.Err()
}
