// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The person view's model-backed lanes.
//
// Separate from the company view's options because the two views degrade
// independently: a deployment that lights up one has said nothing about the
// other, and folding them into one option would tie a person-page decision to a
// company-page one.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
)

// WithPersonDraft binds the lane that writes an email to one contact — the
// person-side mirror of WithAccountDraft, whose sending half is likewise
// POST /emails.
//
// Without it the endpoint still answers, from the deterministic floor: a
// deployment running no model still has a rep who pressed "Write email", and a
// short opener they edit beats a refusal.
//
// It takes no pool. That is the zero-write guarantee expressed as a dependency:
// drafting reads the caller's 360 and writes nothing, so there is nothing for a
// transaction to do.
func WithPersonDraft(brain completer) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		svc := persondraft.NewService(s.person360Svc, brain)
		s.personDraftHandlers = persondraft.NewHandlers(svc, s.sorDispatch.isOverlay)
	}
}
