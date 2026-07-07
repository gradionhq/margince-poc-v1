// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The website cold-start read-back (features/07 §1): fetch a company page,
// extract the onboarding fields with VERBATIM evidence, stage the result as a
// 🟡 approval — nothing touches real records until a human accepts via the
// inbox. Fetch, extraction and the no-guess gate are the shared
// evidenceExtractor (enrichextract.go); this file owns only what is specific
// to onboarding: the accepted field vocabulary, the ColdStart contract mapping
// and the "coldstart" staging.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// coldStartEngine stages a read-back over the shared extractor.
type coldStartEngine struct {
	extract   evidenceExtractor
	approvals *approvals.Service
}

// coldStartFieldValid is this engine's slice of the shared vocabulary: the
// contract's ColdStartField enum is the authority on which onboarding fields
// may be returned.
func coldStartFieldValid(name string) bool {
	return crmcontracts.ColdStartFieldField(name).Valid()
}

// Propose runs fetch → extract → no-guess validation → stage, and returns the
// contract proposal. The staged approval row IS the proposal (ADR-0036: staged
// rows are the authority object), so the proposal id is the approval id.
func (e *coldStartEngine) Propose(ctx context.Context, rawURL string) (crmcontracts.ColdStartProposal, error) {
	evidenced, err := e.extract.extract(ctx, rawURL, coldStartFieldValid)
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}

	fields := make([]crmcontracts.ColdStartField, len(evidenced))
	for i, f := range evidenced {
		fields[i] = crmcontracts.ColdStartField{
			Field:           crmcontracts.ColdStartFieldField(f.Field),
			Value:           f.Value,
			EvidenceSnippet: f.EvidenceSnippet,
			SourceUrl:       f.SourceURL,
			Confidence:      f.Confidence,
		}
	}

	proposal := crmcontracts.ColdStartProposal{
		SourceUrl: rawURL,
		Status:    "staged",
		Fields:    fields,
	}
	proposedChange, err := json.Marshal(proposal)
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}
	digest := sha256.Sum256(proposedChange)
	approvalID, err := e.approvals.Stage(ctx, approvals.StageInput{
		Kind:           "coldstart",
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		Summary:        "Cold-start read-back of " + rawURL,
		Announce: []approvals.AnnouncedEvent{{
			Type:    "coldstart.read_back_proposed",
			Payload: map[string]any{"source_url": rawURL, "field_count": len(fields)},
		}},
	})
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}

	now := time.Now().UTC()
	proposal.ProposalId = openapi_types.UUID(approvalID.UUID)
	proposal.CreatedAt = &now
	return proposal, nil
}

type coldstartHandlers struct{ engine *coldStartEngine }

func (h coldstartHandlers) ColdStartReadback(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		// The process role declared no model path (--routing); the operation
		// stays an explicit 501, never a silent guess.
		httperr.NotImplemented(w, r, "coldStartReadback (no model path configured)")
		return
	}
	var req crmcontracts.ColdStartRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	parsed, err := url.Parse(req.Url)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
		return
	}
	proposal, err := h.engine.Propose(r.Context(), req.Url)
	if err != nil {
		var unreadable *unreadableError
		if errors.As(err, &unreadable) {
			// The client sees a generic 422; the real cause (SSRF refusal,
			// timeout, thin page, empty gate) stays server-side.
			slog.ErrorContext(r.Context(), "coldstart read-back unreadable", "url", req.Url, "err", unreadable.cause)
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusUnprocessableEntity,
				Code:   "coldstart_unreadable",
				Detail: "Couldn't read enough from this page. Retry or paste text.",
			})
			return
		}
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, proposal)
}
