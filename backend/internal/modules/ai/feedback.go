// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The correction ledger: what a human has already decided about a claim the
// system derives (AIRT-SCHEMA-1, AIRT-AC-9).
//
// Everything inferred here is re-derived rather than stored — a brief line, a
// moment, an enriched field. That is what keeps it honest, and it is also what
// makes it forget: correct one, and the next read asserts the same wrong thing
// again, because nothing remembers that a human already answered.
//
// The claim KEY is what makes remembering work. It is a hash of the claim's
// normalized path — what the claim is about — and never of its value. Keyed on
// the value a verdict would evaporate the moment the evidence shifted, which
// is precisely when the human's answer matters most.
//
// The ledger stores the verdict and never the model's asserted value. Nothing
// is gained by keeping what was rejected, and a rejected assertion sitting in
// a table is one bad join away from being shown again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// Claim kinds, as the column's CHECK constrains them.
const (
	ClaimProfileField  = "profile_field"
	ClaimInferredKPI   = "inferred_kpi"
	ClaimNextStep      = "next_step"
	ClaimSignal        = "signal"
	ClaimResearchClaim = "research_claim"
)

// Verdicts. Each one changes what a later re-derivation is allowed to do.
const (
	// VerdictSuppressed: never surface this claim again.
	VerdictSuppressed = "suppressed"
	// VerdictCorrected: show the human's value, and never overwrite it with a
	// fresh inference without a recorded 🟡 approval.
	VerdictCorrected = "corrected"
	// VerdictConfirmed: the claim stands and may carry a "confirmed by" marker.
	VerdictConfirmed = "confirmed"
)

// Subject types the ledger accepts, matching the column's CHECK. They are also
// the RBAC objects the write is gated on: correcting what the system says
// about a contact requires the grant to edit that contact.
var feedbackSubjects = map[string]bool{
	"organization": true,
	"person":       true,
	"deal":         true,
	"lead":         true,
}

// FeedbackStore owns the ledger.
type FeedbackStore struct {
	pool *pgxpool.Pool
}

func NewFeedbackStore(pool *pgxpool.Pool) *FeedbackStore { return &FeedbackStore{pool: pool} }

// ClaimKey is the stable identity of a logical claim within a subject and
// kind: a hash of the claim's normalized PATH, never its value.
//
// Callers pass the path they would use to name the claim to a colleague —
// "profile_field:title", "moment:replied_after_gap". Normalizing here rather
// than trusting the caller is what stops "Title" and "title" becoming two
// claims, which would let a correction be lost to a capitalization.
func ClaimKey(path string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(path))))
	return hex.EncodeToString(sum[:])
}

// Verdict is one recorded human decision.
type Verdict struct {
	ClaimKind      string
	ClaimKey       string
	Verdict        string
	CorrectedValue *string
	Note           *string
}

// RecordInput is a human's decision about one claim.
type RecordInput struct {
	SubjectType string
	SubjectID   ids.UUID
	ClaimKind   string
	// ClaimPath is the claim's normalized path; the store hashes it. Callers
	// pass the path rather than a hash so the hashing rule has exactly one
	// implementation and cannot drift per surface.
	ClaimPath      string
	Verdict        string
	CorrectedValue *string
	Note           *string
}

// Record writes a human's verdict, replacing any previous one for the same
// claim.
//
// Replacing rather than appending is the point: a verdict is the current
// answer to "has a human decided this", and two answers is no answer. The
// history is in audit_log, where every other mutation's history lives.
func (s *FeedbackStore) Record(ctx context.Context, in RecordInput) error {
	if !feedbackSubjects[in.SubjectType] {
		return &values.ParseError{Field: "subject_type", Code: "invalid_subject_type",
			Message: "a claim is about an organization, person, deal or lead"}
	}
	if strings.TrimSpace(in.ClaimPath) == "" {
		return &values.ParseError{Field: "claim_path", Code: "missing_claim_path",
			Message: "a verdict names the claim it is about"}
	}
	// A corrected verdict without a value is a human's answer that was lost on
	// the way in. The database refuses it too; refusing here names what is
	// missing instead of surfacing a constraint.
	if (in.Verdict == VerdictCorrected) != (in.CorrectedValue != nil) {
		return &values.ParseError{Field: "corrected_value", Code: "corrected_value_mismatch",
			Message: "a corrected verdict carries the human's value, and no other verdict does"}
	}
	// Gated on the SUBJECT's own grant rather than on a new object of its own:
	// correcting what the system says about a contact is editing that contact,
	// and a separate object would let the two drift apart.
	if err := auth.Require(ctx, in.SubjectType, principal.ActionUpdate); err != nil {
		return err
	}
	actor, _ := principal.Actor(ctx)
	key := ClaimKey(in.ClaimPath)

	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Row scope before the write: a subject outside the caller's scope is
		// a not-found, so a verdict cannot be used to probe which records
		// exist.
		if err := auth.EnsureVisible(ctx, tx, in.SubjectType, in.SubjectID); err != nil {
			return err
		}
		var id ids.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO ai_feedback
			  (workspace_id, subject_type, subject_id, claim_kind, claim_key,
			   verdict, corrected_value, note, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, $7, 'human', $8)
			ON CONFLICT (workspace_id, subject_type, subject_id, claim_kind, claim_key)
			DO UPDATE SET verdict = EXCLUDED.verdict,
			              corrected_value = EXCLUDED.corrected_value,
			              note = EXCLUDED.note,
			              captured_by = EXCLUDED.captured_by,
			              version = ai_feedback.version + 1,
			              updated_at = now()
			RETURNING id`,
			in.SubjectType, in.SubjectID, in.ClaimKind, key,
			in.Verdict, in.CorrectedValue, in.Note, actor.ID,
		).Scan(&id); err != nil {
			return fmt.Errorf("ai: recording a human's verdict on a derived claim: %w", err)
		}
		// Audit-only: the closed catalog (events.md §5) defines no
		// ai_feedback.* type, and the closed-verb law forbids inventing one
		// build-side. The audit row carries the attribution, which is what a
		// "corrected by you" marker and any later dispute both need.
		//
		// The after-image records the claim and the verdict, never the value
		// the model asserted — the ledger does not keep that and neither does
		// its audit trail.
		if _, err := storekit.Audit(ctx, tx, "update", "ai_feedback", id, nil, map[string]any{
			"subject_type": in.SubjectType,
			"subject_id":   in.SubjectID.String(),
			"claim_kind":   in.ClaimKind,
			"claim_key":    key,
			"verdict":      in.Verdict,
		}); err != nil {
			return err
		}
		return nil
	})
}

// VerdictsForTx returns every verdict recorded about one record, keyed by
// "<claim_kind>:<claim_key>".
//
// One read per record rather than per claim: a page re-derives many claims
// about the same subject, and asking the ledger per line would be a query per
// rendered sentence.
func (s *FeedbackStore) VerdictsForTx(ctx context.Context, tx pgx.Tx, subjectType string, subjectID ids.UUID) (map[string]Verdict, error) {
	if !feedbackSubjects[subjectType] {
		return nil, &values.ParseError{Field: "subject_type", Code: "invalid_subject_type",
			Message: "a claim is about an organization, person, deal or lead"}
	}
	// A read grant on the subject, matching the read this consult decorates.
	// The caller has already resolved the subject's row scope by the time it
	// asks what a human decided about it.
	if err := auth.Require(ctx, subjectType, principal.ActionRead); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT claim_kind, claim_key, verdict, corrected_value, note
		  FROM ai_feedback
		 WHERE subject_type = $1 AND subject_id = $2`, subjectType, subjectID)
	if err != nil {
		return nil, fmt.Errorf("ai: reading the verdicts recorded about a record: %w", err)
	}
	defer rows.Close()

	out := map[string]Verdict{}
	for rows.Next() {
		var v Verdict
		if err := rows.Scan(&v.ClaimKind, &v.ClaimKey, &v.Verdict, &v.CorrectedValue, &v.Note); err != nil {
			return nil, fmt.Errorf("ai: reading a recorded verdict: %w", err)
		}
		out[VerdictLookupKey(v.ClaimKind, v.ClaimKey)] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ai: reading the recorded verdicts: %w", err)
	}
	return out, nil
}

// VerdictLookupKey is how a consulting surface finds one claim's verdict in
// the map VerdictsForTx returns. It exists as a named function so the two
// sides cannot spell the composite key differently.
func VerdictLookupKey(claimKind, claimKey string) string {
	return claimKind + ":" + claimKey
}
