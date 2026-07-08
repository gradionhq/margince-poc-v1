// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cold-start read-back (features/07 §1, B-E01.2b/.13): extract the
// onboarding fields with VERBATIM evidence from EXACTLY ONE input — a company
// `url` (fetched), pasted `text` (the fallback when a site is unreadable), or
// a `self_description` (the user's own words; a field it grounds cites that
// statement as evidence — honest grounding, not fabrication) — and stage the
// result as a 🟡 approval. Nothing touches real records until a human accepts
// via the inbox, whatever the input kind. Extraction and the no-guess gate are
// the shared evidenceExtractor (enrichextract.go); this file owns only what is
// specific to onboarding: the accepted field vocabulary, the ColdStart
// contract mapping and the "coldstart" staging.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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

// Propose runs fetch → extract → no-guess validation → stage for the `url`
// input and returns the contract proposal.
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
			SourceKind:      crmcontracts.ColdStartFieldSourceKindUrl,
			SourceUrl:       &f.SourceURL,
			Confidence:      f.Confidence,
		}
	}
	return e.stage(ctx, crmcontracts.ColdStartProposal{
		SourceKind: crmcontracts.ColdStartProposalSourceKindUrl,
		SourceUrl:  &rawURL,
		Status:     "staged",
		Fields:     fields,
	}, "Cold-start read-back of "+rawURL, map[string]any{
		"source_url":  rawURL,
		"field_count": len(fields),
	})
}

// ProposeText runs the pasted-text fallback: the SAME model+gate seam the url
// path uses after its fetch, over the text the user supplied. Every surviving
// field cites the paste itself — source_kind=text, no source_url, and the
// char offset of its evidence within the paste for highlight-back.
func (e *coldStartEngine) ProposeText(ctx context.Context, pasted string) (crmcontracts.ColdStartProposal, error) {
	evidenced, err := e.extract.extractGrounded(ctx, "Pasted company text", pasted, "", coldStartFieldValid)
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}

	fields := make([]crmcontracts.ColdStartField, len(evidenced))
	for i, f := range evidenced {
		fields[i] = crmcontracts.ColdStartField{
			Field:           crmcontracts.ColdStartFieldField(f.Field),
			Value:           f.Value,
			EvidenceSnippet: f.EvidenceSnippet,
			SourceKind:      crmcontracts.ColdStartFieldSourceKindText,
			EvidenceOffset:  runeOffset(pasted, f.EvidenceSnippet),
			Confidence:      f.Confidence,
		}
	}
	return e.stage(ctx, crmcontracts.ColdStartProposal{
		SourceKind: crmcontracts.ColdStartProposalSourceKindText,
		Status:     "staged",
		Fields:     fields,
	}, "Cold-start read-back of pasted text", map[string]any{
		"source_kind": string(crmcontracts.ColdStartProposalSourceKindText),
		"field_count": len(fields),
	})
}

// ProposeSelfDescription grounds fields in the user's own statement
// (B-E01.13): the same gate as every other kind, with the statement as the
// only admissible evidence — a field it does not support is ABSENT, and what
// survives cites the user's own words (source_kind=self_description).
func (e *coldStartEngine) ProposeSelfDescription(ctx context.Context, statement string) (crmcontracts.ColdStartProposal, error) {
	evidenced, err := e.extract.extractGrounded(ctx, "The user's own description of their business", statement, "", coldStartFieldValid)
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}

	fields := make([]crmcontracts.ColdStartField, len(evidenced))
	for i, f := range evidenced {
		fields[i] = crmcontracts.ColdStartField{
			Field:           crmcontracts.ColdStartFieldField(f.Field),
			Value:           f.Value,
			EvidenceSnippet: f.EvidenceSnippet,
			SourceKind:      crmcontracts.ColdStartFieldSourceKindSelfDescription,
			Confidence:      f.Confidence,
		}
	}
	return e.stage(ctx, crmcontracts.ColdStartProposal{
		SourceKind: crmcontracts.ColdStartProposalSourceKindSelfDescription,
		Status:     "staged",
		Fields:     fields,
	}, "Cold-start read-back of a self-description", map[string]any{
		"source_kind": string(crmcontracts.ColdStartProposalSourceKindSelfDescription),
		"field_count": len(fields),
	})
}

// stage lands the proposal as a pending "coldstart" approval — the staged row
// IS the proposal (ADR-0036: staged rows are the authority object), so the
// proposal id is the approval id. Identical for every input kind: 🟡 always,
// auto-write never.
func (e *coldStartEngine) stage(ctx context.Context, proposal crmcontracts.ColdStartProposal, summary string, announce map[string]any) (crmcontracts.ColdStartProposal, error) {
	proposedChange, err := json.Marshal(proposal)
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}
	digest := sha256.Sum256(proposedChange)
	approvalID, err := e.approvals.Stage(ctx, approvals.StageInput{
		Kind:           "coldstart",
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		Summary:        summary,
		Announce: []approvals.AnnouncedEvent{{
			Type:    "coldstart.read_back_proposed",
			Payload: announce,
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

// runeOffset is the contract's evidence_offset: the char (rune) position of
// the snippet within the pasted text, nil when the snippet cannot be located
// verbatim (the contract allows null over a fabricated position).
func runeOffset(text, snippet string) *int {
	byteIdx := strings.Index(text, snippet)
	if byteIdx < 0 {
		return nil
	}
	offset := utf8.RuneCountInString(text[:byteIdx])
	return &offset
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

	populated := 0
	for _, input := range []*string{req.Url, req.Text, req.SelfDescription} {
		if input != nil {
			populated++
		}
	}
	if populated != 1 {
		httperr.Write(w, r, &httperr.DetailedError{
			Status:  http.StatusUnprocessableEntity,
			Code:    "validation_error",
			Detail:  "provide exactly one of url, text or self_description",
			Details: map[string]any{"populated_fields": populated},
		})
		return
	}

	var proposal crmcontracts.ColdStartProposal
	var err error
	var unreadableDetail string
	switch {
	case req.Url != nil:
		parsed, parseErr := url.Parse(*req.Url)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
			return
		}
		unreadableDetail = "Couldn't read enough from this page. Retry or paste text."
		proposal, err = h.engine.Propose(r.Context(), *req.Url)
	case req.Text != nil:
		if strings.TrimSpace(*req.Text) == "" {
			httperr.Write(w, r, httperr.Validation("text", "empty", "text must not be empty"))
			return
		}
		unreadableDetail = "Couldn't ground any company fact in this text. Paste more of the page."
		proposal, err = h.engine.ProposeText(r.Context(), *req.Text)
	default:
		if strings.TrimSpace(*req.SelfDescription) == "" {
			httperr.Write(w, r, httperr.Validation("self_description", "empty", "self_description must not be empty"))
			return
		}
		unreadableDetail = "Couldn't ground any field in this description. Say more about your business."
		proposal, err = h.engine.ProposeSelfDescription(r.Context(), *req.SelfDescription)
	}
	if err != nil {
		var unreadable *unreadableError
		if errors.As(err, &unreadable) {
			// The client sees a generic 422; the real cause (SSRF refusal,
			// timeout, thin input, empty gate) stays server-side.
			slog.ErrorContext(r.Context(), "coldstart read-back unreadable",
				"source_kind", coldStartInputKind(req), "err", unreadable.cause)
			httperr.Write(w, r, &httperr.DetailedError{
				Status:  http.StatusUnprocessableEntity,
				Code:    "coldstart_unreadable",
				Detail:  unreadableDetail,
				Details: map[string]any{"populated_fields": 0},
			})
			return
		}
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, proposal)
}

// coldStartInputKind names the single populated input for the server log —
// the pasted text / statement itself is tenant data and never logged.
func coldStartInputKind(req crmcontracts.ColdStartRequest) string {
	switch {
	case req.Url != nil:
		return "url:" + *req.Url
	case req.Text != nil:
		return "text"
	default:
		return "self_description"
	}
}
