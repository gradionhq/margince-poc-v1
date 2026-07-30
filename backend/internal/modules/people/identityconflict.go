// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The identity-review half of D8 (telegram-oa design §7.3): a lane conflict
// (dedupe.go's routeExact reporting that a later exact lane named a
// different person than the one routing chose) is a REPORT, not a decision —
// routing picked the established binding because a message has to land
// somewhere, not because the rival is wrong. Whether RoutedTo and Rival are
// one human conflated across two lanes, or two people who legitimately share
// a key (a shared household phone, a departed employee's old handle), is a
// human question. This is where that question reaches the one queue the
// system already has for it (DH-DDL-1) instead of a second one invented for
// the occasion.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// fieldMatchedLane is the evidence field name for an exact-lane conflict
// row: not a data field like full_name or email, but which LANE each side of
// the pair was matched through.
const fieldMatchedLane = "matched_lane"

// evidenceSignalExactConflict marks a conflict row's evidence as distinct
// from the fuzzy tier's agree/collide/one_sided vocabulary: those describe a
// FIELD VALUE comparison between two candidate records. This describes two
// different KINDS of identifying key each independently resolving to a
// different existing person — a disagreement between established bindings,
// not a similarity judgment.
const evidenceSignalExactConflict = "exact_conflict"

// identityConflictConfidence is the standing convention design §7.3 leaves
// open, settled here against dedupe_candidate's actual shape (DH-DDL-1):
// elsewhere in this table, confidence is a FUZZY score in [0, dedupeReviewThreshold..1),
// weighed against a threshold because it is a guess. An exact-lane conflict
// is not a guess — it is two independently-established keys (a bound channel
// identity, a stored phone number, each a live row in its own satellite
// table) each already naming a different existing person. That is strictly
// stronger evidence than any name-similarity score, so it is recorded at the
// confidence column's ceiling. Two things this does NOT mean: it is not an
// assertion that RoutedTo and Rival ARE the same person (only a human
// disposes that), and it is not "the algorithm is 100% sure" in the fuzzy
// tier's probabilistic sense. It means this pair is the most certain KIND of
// question the queue can carry, which is also why 1.0 correctly sorts every
// exact conflict ahead of every fuzzy proposal in the confidence-DESC queue
// (idx_dedupe_candidate_open) — a disagreement between two established
// bindings deserves a human's attention before a name that merely looks
// similar to another.
const identityConflictConfidence = 1.0

// EnqueueIdentityConflict raises the identity review for one exact-lane
// disagreement. It runs in its OWN transaction, never the caller's ensure
// transaction: capture's contract is that an inbound message lands on the
// timeline even when this write fails, and sharing a transaction would let a
// fault here roll the message back with it — the caller logs a failure
// rather than propagating it, exactly because this must never be the reason
// a customer's message goes missing.
//
// Idempotent by construction: recordDedupeCandidate orders the pair
// canonically and inserts ON CONFLICT DO NOTHING against dedupe_candidate's
// own pair index (0096), which is NOT partial on disposition. So the SAME
// conflicting identity messaging again — which will recur on every message
// until a human resolves it — meets the existing row whether it is still
// open or already carries a not_a_duplicate verdict, and proposes nothing a
// second time. That is also design §7.3's warning honored: a dismissed pair
// must not be re-raised.
func (s *Store) EnqueueIdentityConflict(ctx context.Context, conflict LaneConflict, source, capturedBy string) (bool, error) {
	evidence := []map[string]any{
		{
			evidenceFieldKey:  fieldMatchedLane,
			evidenceLeftKey:   conflict.RoutedLane,
			evidenceRightKey:  conflict.RivalLane,
			evidenceSignalKey: evidenceSignalExactConflict,
		},
	}
	var recorded bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		recorded, err = recordDedupeCandidate(ctx, tx, entityPerson,
			conflict.RoutedTo.UUID, conflict.Rival.UUID, identityConflictConfidence, evidence, source, capturedBy)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("people: enqueueing identity conflict review: %w", err)
	}
	return recorded, nil
}
