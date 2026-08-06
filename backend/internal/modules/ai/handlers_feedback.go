// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RecordAIFeedback implements (POST /ai/feedback): a human's verdict on a
// claim the system derived.
//
// It answers 204 and returns nothing. There is nothing useful to hand back —
// the caller already knows what they decided, and the consequence of the
// verdict shows up in the next re-derivation of the surface they decided on,
// not in this response.
func (h Handlers) RecordAIFeedback(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.AIFeedbackInput
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := RecordInput{
		SubjectType: string(req.SubjectType),
		SubjectID:   ids.UUID(req.SubjectId),
		ClaimKind:   string(req.ClaimKind),
		ClaimPath:   req.ClaimPath,
		Verdict:     string(req.Verdict),
		Note:        req.Note,
	}
	// Carried through only for the verdict that defines it. The store refuses
	// the mismatch, and passing a stray value here would make that refusal
	// look like a server fault rather than the caller's.
	if req.Verdict == crmcontracts.AIFeedbackInputVerdictCorrected {
		in.CorrectedValue = req.CorrectedValue
	}
	if err := h.feedback.Record(r.Context(), in); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
