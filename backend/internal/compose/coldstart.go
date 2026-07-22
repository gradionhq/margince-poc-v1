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

// Readback runs fetch → extract → no-guess validation for whichever single
// input the request carries, and returns the surviving evidenced fields. It
// writes nothing and stages nothing: the caller decides what the read-back
// becomes — a 🟡 approval nobody is waiting at (Propose), or the pre-fill of a
// form the human confirms field by field (the preview transport).
func (e *coldStartEngine) Readback(ctx context.Context, req crmcontracts.ColdStartRequest) ([]crmcontracts.ColdStartField, error) {
	switch {
	case req.Url != nil:
		evidenced, err := e.extract.extract(ctx, *req.Url, coldStartFieldValid)
		if err != nil {
			return nil, err
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
		return fields, nil

	case req.Text != nil:
		// The pasted-text fallback: the SAME model+gate seam the url path uses
		// after its fetch. Every surviving field cites the paste itself — no
		// source_url, plus the char offset of its evidence for highlight-back.
		evidenced, err := e.extract.extractGrounded(ctx, "Pasted company text", *req.Text, "", coldStartFieldValid)
		if err != nil {
			return nil, err
		}
		fields := make([]crmcontracts.ColdStartField, len(evidenced))
		for i, f := range evidenced {
			fields[i] = crmcontracts.ColdStartField{
				Field:           crmcontracts.ColdStartFieldField(f.Field),
				Value:           f.Value,
				EvidenceSnippet: f.EvidenceSnippet,
				SourceKind:      crmcontracts.ColdStartFieldSourceKindText,
				EvidenceOffset:  runeOffset(*req.Text, f.EvidenceSnippet),
				Confidence:      f.Confidence,
			}
		}
		return fields, nil

	default:
		// Grounded in the user's own statement (B-E01.13): the same gate as
		// every other kind, with the statement as the only admissible evidence
		// — a field it does not support is ABSENT, and what survives cites the
		// user's own words.
		evidenced, err := e.extract.extractGrounded(ctx, "The user's own description of their business", *req.SelfDescription, "", coldStartFieldValid)
		if err != nil {
			return nil, err
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
		return fields, nil
	}
}

// Propose is the staging path: a read-back nobody is watching becomes a 🟡
// approval for a human to accept later, out of band.
func (e *coldStartEngine) Propose(ctx context.Context, req crmcontracts.ColdStartRequest) (crmcontracts.ColdStartProposal, error) {
	fields, err := e.Readback(ctx, req)
	if err != nil {
		return crmcontracts.ColdStartProposal{}, err
	}
	kind := coldStartProposalKind(req)
	summary, announce := coldStartStagingNotice(req, kind, len(fields))
	return e.stage(ctx, crmcontracts.ColdStartProposal{
		SourceKind: kind,
		SourceUrl:  req.Url,
		Status:     "staged",
		Fields:     fields,
	}, summary, announce)
}

// coldStartProposalKind names the single populated input on the wire.
func coldStartProposalKind(req crmcontracts.ColdStartRequest) crmcontracts.ColdStartProposalSourceKind {
	switch {
	case req.Url != nil:
		return crmcontracts.ColdStartProposalSourceKindUrl
	case req.Text != nil:
		return crmcontracts.ColdStartProposalSourceKindText
	default:
		return crmcontracts.ColdStartProposalSourceKindSelfDescription
	}
}

// coldStartStagingNotice builds the approval's human summary and its
// announced coldstart.read_back_proposed payload. The pasted text /
// statement is tenant data and never announced — only its kind and how
// much it grounded.
func coldStartStagingNotice(req crmcontracts.ColdStartRequest, kind crmcontracts.ColdStartProposalSourceKind, fieldCount int) (string, crmcontracts.WebhookPayloadColdstartReadBackProposed) {
	if req.Url != nil {
		return "Cold-start read-back of " + *req.Url, crmcontracts.WebhookPayloadColdstartReadBackProposed{
			SourceUrl:  req.Url,
			FieldCount: fieldCount,
		}
	}
	subject := "pasted text"
	if kind == crmcontracts.ColdStartProposalSourceKindSelfDescription {
		subject = "a self-description"
	}
	sourceKind := string(kind)
	return "Cold-start read-back of " + subject, crmcontracts.WebhookPayloadColdstartReadBackProposed{
		SourceKind: &sourceKind,
		FieldCount: fieldCount,
	}
}

// stage lands the proposal as a pending "coldstart" approval — the staged row
// IS the proposal (ADR-0036: staged rows are the authority object), so the
// proposal id is the approval id. Identical for every input kind: 🟡 always,
// auto-write never.
func (e *coldStartEngine) stage(ctx context.Context, proposal crmcontracts.ColdStartProposal, summary string, announce crmcontracts.WebhookPayloadColdstartReadBackProposed) (crmcontracts.ColdStartProposal, error) {
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

// ColdStartReadback stages the read-back as a 🟡 approval — the asynchronous
// path, for a proposal no human is currently looking at.
func (h coldstartHandlers) ColdStartReadback(w http.ResponseWriter, r *http.Request) {
	req, ok := h.acceptColdStartRequest(w, r, "coldStartReadback")
	if !ok {
		return
	}
	proposal, err := h.engine.Propose(r.Context(), req)
	if err != nil {
		writeColdStartError(w, r, req, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, proposal)
}

// ColdStartPreview returns the read-back for the company form to pre-fill,
// staging nothing: the human confirms in the form itself, and PUT /company is
// the write. The extraction and the no-guess gate are identical to the staging
// path — only what happens to the result differs.
func (h coldstartHandlers) ColdStartPreview(w http.ResponseWriter, r *http.Request) {
	req, ok := h.acceptColdStartRequest(w, r, "coldStartPreview")
	if !ok {
		return
	}
	fields, err := h.engine.Readback(r.Context(), req)
	if err != nil {
		writeColdStartError(w, r, req, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ColdStartReadback{Fields: fields})
}

// acceptColdStartRequest decodes and validates the shared request shape for
// both transports: the engine must be wired, exactly one input must be
// populated, and that input must be well-formed. It writes the problem
// response itself and reports whether the caller may proceed.
func (h coldstartHandlers) acceptColdStartRequest(w http.ResponseWriter, r *http.Request, operation string) (crmcontracts.ColdStartRequest, bool) {
	var req crmcontracts.ColdStartRequest
	if h.engine == nil {
		// The process role declared no model path (--routing); the operation
		// stays an explicit 501, never a silent guess.
		httperr.NotImplemented(w, r, operation+" (no model path configured)")
		return req, false
	}
	if !httperr.Decode(w, r, &req) {
		return req, false
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
		return req, false
	}

	switch {
	case req.Url != nil:
		parsed, err := url.Parse(*req.Url)
		if err != nil || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) || parsed.Host == "" {
			httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
			return req, false
		}
	case req.Text != nil:
		if strings.TrimSpace(*req.Text) == "" {
			httperr.Write(w, r, httperr.Validation("text", "empty", "text must not be empty"))
			return req, false
		}
	default:
		if strings.TrimSpace(*req.SelfDescription) == "" {
			httperr.Write(w, r, httperr.Validation("self_description", "empty", "self_description must not be empty"))
			return req, false
		}
	}
	return req, true
}

// writeColdStartError maps an extraction failure onto the honest 422. The
// client sees a generic, actionable message; the real cause (SSRF refusal,
// timeout, thin input, empty gate) stays server-side.
func writeColdStartError(w http.ResponseWriter, r *http.Request, req crmcontracts.ColdStartRequest, err error) {
	var unreadable *unreadableError
	if !errors.As(err, &unreadable) {
		httperr.Write(w, r, err)
		return
	}
	slog.ErrorContext(r.Context(), "coldstart read-back unreadable",
		"source_kind", coldStartInputKind(req), "err", unreadable.cause)
	httperr.Write(w, r, &httperr.DetailedError{
		Status:  http.StatusUnprocessableEntity,
		Code:    "coldstart_unreadable",
		Detail:  coldStartUnreadableDetail(req),
		Details: map[string]any{"populated_fields": 0},
	})
}

// coldStartUnreadableDetail says what to try next, in the terms of whatever
// the user actually supplied.
func coldStartUnreadableDetail(req crmcontracts.ColdStartRequest) string {
	switch {
	case req.Url != nil:
		return "Couldn't read enough from this page. Retry or paste text."
	case req.Text != nil:
		return "Couldn't ground any company fact in this text. Paste more of the page."
	default:
		return "Couldn't ground any field in this description. Say more about your business."
	}
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
