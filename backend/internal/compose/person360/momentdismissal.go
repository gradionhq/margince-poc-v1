// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// Hiding a moment is a PREFERENCE, not a correction.
//
// The distinction decides which store the write lands in, and getting it wrong
// leaks one person's screen into everybody's data. A dismissal says "I have
// seen this and do not want to be told again"; it feeds no formula, no rule, no
// brief, no agent context and no other viewer's page (ADR-0096 D3). Saying a
// derived CLAIM is wrong is a different act with a different destination — the
// workspace-scoped AI feedback ledger, because a correction is shared truth.
//
// So this table carries no audit row and emits no event. There is no business
// fact here to reconstruct, and an outbox event announcing that somebody closed
// a card would be noise on a bus that carries record changes.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DismissPersonMoment implements POST /people/{id}/moment/dismiss.
func (h Handlers) DismissPersonMoment(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var body crmcontracts.DismissPersonMomentRequest
	if !httperr.Decode(w, r, &body) {
		return
	}
	if body.ClaimKey == "" {
		httperr.Write(w, r, httperr.Validation("claim_key", "required",
			"name the moment to dismiss"))
		return
	}
	if body.EvidenceFingerprint == "" {
		// Without the fingerprint the dismissal could never re-arm, which makes
		// it a permanent silence nobody asked for.
		httperr.Write(w, r, httperr.Validation("evidence_fingerprint", "required",
			"pass the fingerprint the moment was showing, so the dismissal lifts when its evidence changes"))
		return
	}
	err := h.svc.DismissMoment(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)), body)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DismissMoment records that this viewer has put this moment away while its
// evidence stands.
//
// Human-only, for the reason Acknowledge is: an agent principal carries the
// granting human's id, so resolving "the acting user" would let a passport read
// silence a card on a human's screen that the human never saw.
func (s *Service) DismissMoment(ctx context.Context, personID ids.PersonID, in crmcontracts.DismissPersonMomentRequest) error {
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Anything that names a record is gated: dismissing a moment about a
		// person the caller cannot read would confirm that person exists.
		if err := auth.EnsureVisibleLive(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO person_moment_dismissal
				(workspace_id, user_id, person_id, claim_key, evidence_fingerprint)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, user_id, person_id, claim_key)
			DO UPDATE SET evidence_fingerprint = EXCLUDED.evidence_fingerprint,
			              dismissed_at = now()`,
			storekit.MustWorkspace(ctx), userID, personID, in.ClaimKey, in.EvidenceFingerprint)
		if err != nil {
			return err
		}
		return nil
	})
}
