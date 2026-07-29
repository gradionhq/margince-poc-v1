// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The send side of the voice learning loop. RecordDraftedSignal opens a
// signal when a draft is served; this closes it when the human actually
// sends — accepted when the generated text went out untouched,
// edited_sent when they reworded it first. The judgment is the whole
// value of the row: it is what tells a later corpus decision whether this
// profile is drafting in the owner's voice yet.

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RecordSendOutcomeTx records how a voice-drafted message was actually
// sent, against the drafted signal the reference opened.
//
// It runs inside the CALLER's transaction — the send's — so the outcome
// commits with the message or not at all; the workspace GUC is already
// bound there and is never re-set here. That is why it takes a tx rather
// than opening one like its siblings in this package.
//
// recorded=false means there was nothing to record. Every refusal answers
// that way rather than with an error: an email that legitimately went out
// must never fail over a learning signal, and answering "absent" for a
// row the caller may not touch keeps a foreign reference indistinguishable
// from an unknown one.
func (s *VoiceStore) RecordSendOutcomeTx(ctx context.Context, tx pgx.Tx, draftRef, finalBody string) (bool, error) {
	actor, ok := principal.Actor(ctx)
	// ADR-0066 §4 frames the outcome as the OWNER's judgment of the
	// machine's draft. An agent's edit is not the owner's authored text,
	// so only a human closes a signal.
	if !ok || actor.Type != principal.PrincipalHuman || actor.UserID.IsZero() {
		return false, nil
	}
	if err := auth.Require(ctx, "voice_profile", principal.ActionUpdate); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return false, nil
		}
		return false, err
	}

	signal, judgeable, err := s.lockJudgeableSignal(ctx, tx, draftRef, actor.UserID)
	if err != nil || !judgeable {
		return false, err
	}

	outcome, similarity := classifyVoiceSendOutcome(signal.generatedOriginal, finalBody)
	// final_text stays NULL, and the classification's inputs are dropped
	// with this function's frame. This row carries no person, activity, or
	// subject linkage, so Art. 17 erasure structurally cannot find it —
	// only the time-based sweep does. Persisting the sent correspondence
	// here would keep an erased person's mail alive for up to 180 days.
	//
	// The outcome = 'drafted' guard makes a decision terminal: two sends
	// may legitimately carry one reference (they are two emails), and the
	// first transaction to hold this row's lock owns the outcome.
	tag, err := tx.Exec(ctx, `
		UPDATE voice_learning_signal
		SET outcome = $2, similarity = $3, final_captured_by = $4,
		    version = version + 1, updated_at = $5
		WHERE id = $1 AND outcome = 'drafted'`,
		signal.id, outcome, similarity, actor.ID, s.now().UTC())
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	// The row exists to attribute a judgment of the machine's words to a
	// human, so the audit trail carries WHO closed it alongside what was
	// decided — an outcome nobody is named for answers only half the question
	// this table is asked.
	auditID, err := storekit.Audit(ctx, tx, "update", "voice_learning_signal", signal.id,
		map[string]any{voiceKeyOutcome: voiceOutcomeDrafted},
		map[string]any{
			voiceKeyOutcome:         outcome,
			voiceKeySimilarity:      similarity,
			voiceKeyFinalCapturedBy: actor.ID,
		})
	if err != nil {
		return false, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, signal.profileID,
		voiceDraftOutcomeRecordedPayload(signal.profileID, outcome)); err != nil {
		return false, err
	}
	return true, nil
}

// judgeableSignal is a locked learning signal whose served text is still
// present, on a profile the acting human owns.
type judgeableSignal struct {
	id                ids.UUID
	profileID         ids.UUID
	generatedOriginal string
}

// lockJudgeableSignal finds and locks the signal a draft reference opened.
// ok=false is every "nothing to record" case, all indistinguishable from
// each other by design.
func (s *VoiceStore) lockJudgeableSignal(ctx context.Context, tx pgx.Tx, draftRef string, owner ids.UUID) (judgeableSignal, bool, error) {
	hash := sha256.Sum256([]byte(draftRef))
	var (
		signal            judgeableSignal
		generatedOriginal *string
	)
	// The ownership predicate rides in the lookup rather than following it,
	// and only the signal is locked: a draft reference is deterministic, so
	// a colleague who computes one must not be able to take a lock on
	// another human's row and serialize against that owner's own send. That
	// wait is observable, and it would tell "absent" and "someone else's"
	// apart — the oracle over another human's drafts this whole path is
	// written to deny. The owner match is the same row scope every profile
	// read goes through, restated here as defence in depth on top of RLS.
	//
	// Both the erasure and the archive filter matter. Art. 17 erasure and
	// the retention sweep NULL the content IN PLACE and leave the row
	// drafted, so a lookup that ignored content_erased_at would find it,
	// compare the sent body against a missing original, call that an edit,
	// and write a fresh judgment over text an erasure already removed.
	err := tx.QueryRow(ctx, `
		SELECT s.id, s.voice_profile_id, s.generated_original
		FROM voice_learning_signal s
		JOIN voice_profile p ON p.id = s.voice_profile_id
		WHERE s.draft_ref_hash = $1 AND s.content_erased_at IS NULL AND s.archived_at IS NULL
		  AND p.archived_at IS NULL AND p.scope = 'user' AND p.owner_id = $2
		FOR UPDATE OF s`, hash[:], owner).
		Scan(&signal.id, &signal.profileID, &generatedOriginal)
	if errors.Is(err, pgx.ErrNoRows) {
		return judgeableSignal{}, false, nil
	}
	if err != nil {
		return judgeableSignal{}, false, err
	}
	if generatedOriginal == nil {
		// A live row without the served text cannot be judged at all: the
		// erased row's reasoning, one step further.
		return judgeableSignal{}, false, nil
	}
	signal.generatedOriginal = *generatedOriginal
	return signal, true, nil
}

// classifyVoiceSendOutcome judges the served draft against what actually
// went out, and returns the DDL outcome with its similarity.
//
// The metric is PINNED, not incidental: a later PR promotes these rows
// retroactively into a training corpus, and a similarity whose definition
// drifted would poison every corpus decision made from the rows already
// stored. It is a normalized token-level Levenshtein ratio over
// NFC-normalized, case-folded, whitespace-collapsed text.
func classifyVoiceSendOutcome(generatedOriginal, finalBody string) (string, float64) {
	original := voiceComparableTokens(generatedOriginal)
	final := voiceComparableTokens(finalBody)
	// The classification is decided on the tokens rather than on a
	// similarity == 1 float comparison: what "unedited" means must not
	// depend on floating-point rounding.
	if slices.Equal(original, final) {
		return voiceOutcomeAccepted, 1
	}
	return voiceOutcomeEditedSent, voiceTokenSimilarity(original, final)
}

// voiceComparableTokens applies the pinned normalization: NFC, Unicode
// full case folding, and whitespace collapsed into tokens. So a mail
// client reflowing the draft or changing its capitalization compares
// equal — that is not the human rewriting it. A fresh Caser per call:
// cases.Caser is stateful and not safe for concurrent use.
func voiceComparableTokens(s string) []string {
	return strings.Fields(cases.Fold().String(norm.NFC.String(s)))
}

// voiceTokenSimilarity is 1 - distance/max(len(a), len(b)) in token
// counts. Two empty token lists score 1: nothing changed between them.
func voiceTokenSimilarity(a, b []string) float64 {
	longest := max(len(a), len(b))
	if longest == 0 {
		return 1
	}
	ratio := 1 - float64(voiceTokenDistance(a, b))/float64(longest)
	// The distance never exceeds the longer side, so this states the
	// column CHECK's [0,1] bound rather than repairing an overflow.
	return min(1, max(0, ratio))
}

// voiceTokenDistance is Levenshtein distance over whole tokens, carrying
// two rows instead of the whole matrix — a mail body is short, but the
// matrix is still needless.
func voiceTokenDistance(a, b []string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			substitution := previous[j-1]
			if a[i-1] != b[j-1] {
				substitution++
			}
			current[j] = min(previous[j]+1, current[j-1]+1, substitution)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
