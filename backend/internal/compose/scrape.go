// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// scrapeCompany (EP05 / ADR-0006): the `enrich` verb applied to an EXISTING
// organization. It reads the company's website — an explicit `url` override,
// else the org's own domain — through the SAME fetch + evidence gate as the
// cold-start read-back (evidenceExtractor), and stages a 🟡 proposal bound to
// that org. Nothing is written until a human accepts via /approvals, which
// fills only the org's empty fields. Distinct from onboarding: it targets a
// known record and never creates one.

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
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// scrapeEngine stages a per-org enrichment over the shared extractor.
type scrapeEngine struct {
	extract   evidenceExtractor
	people    *people.Store
	approvals *approvals.Service
}

// Propose resolves the URL to read (override, else the org's domain — both
// row-scoped: an org the caller cannot see is ErrNotFound), extracts
// evidence-grounded fields, and stages an "enrich" approval bound to the org.
func (e *scrapeEngine) Propose(ctx context.Context, orgID ids.UUID, override string) (crmcontracts.EnrichmentProposal, error) {
	rawURL := override
	if rawURL == "" {
		var err error
		// EnrichTargetURL enforces visibility AND yields the domain.
		rawURL, err = e.people.EnrichTargetURL(ctx, ids.From[ids.OrganizationKind](orgID))
		if err != nil {
			return crmcontracts.EnrichmentProposal{}, err
		}
	} else {
		// An override skips the domain lookup, so visibility must be proven
		// on its own — reading the org row-scoped 404s a hidden id before any
		// egress happens on the caller's behalf.
		if _, err := e.people.GetOrganization(ctx, ids.From[ids.OrganizationKind](orgID), storekit.LiveOnly); err != nil {
			return crmcontracts.EnrichmentProposal{}, err
		}
	}

	evidenced, err := e.extract.extract(ctx, rawURL, coldStartFieldValid)
	if err != nil {
		return crmcontracts.EnrichmentProposal{}, err
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

	proposal := crmcontracts.EnrichmentProposal{
		OrganizationId: openapi_types.UUID(orgID),
		SourceUrl:      rawURL,
		Status:         crmcontracts.EnrichmentProposalStatusStaged,
		Fields:         fields,
	}
	proposedChange, err := json.Marshal(proposal)
	if err != nil {
		return crmcontracts.EnrichmentProposal{}, err
	}
	digest := sha256.Sum256(proposedChange)
	// No kind-specific announce event: the generic approval.requested marks
	// the staging and organization.updated fires on accept — both catalogued.
	// There is no enrichment_proposed event in the events.md §5 catalog, and
	// this build does not invent one (the spec owns the catalog).
	approvalID, err := e.approvals.Stage(ctx, approvals.StageInput{
		Kind:           "enrich",
		ProposedChange: proposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     "organization",
		TargetID:       orgID,
		Summary:        "Enrichment of " + rawURL,
	})
	if err != nil {
		return crmcontracts.EnrichmentProposal{}, err
	}

	now := time.Now().UTC()
	proposal.ProposalId = openapi_types.UUID(approvalID.UUID)
	proposal.CreatedAt = &now
	return proposal, nil
}

type scrapeHandlers struct{ engine *scrapeEngine }

func (h scrapeHandlers) ScrapeCompany(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.engine == nil {
		// The process role declared no model path (--routing); the operation
		// stays an explicit 501, never a silent guess.
		httperr.NotImplemented(w, r, "scrapeCompany (no model path configured)")
		return
	}
	// The body is optional (no override reads the org's own domain).
	var override string
	if r.ContentLength != 0 {
		var req crmcontracts.EnrichCompanyRequest
		if !httperr.Decode(w, r, &req) {
			return
		}
		if req.Url != nil {
			override = *req.Url
		}
	}
	if override != "" {
		parsed, err := url.Parse(override)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			httperr.Write(w, r, httperr.Validation("url", "invalid", "url must be an absolute http(s) URL"))
			return
		}
	}

	proposal, err := h.engine.Propose(r.Context(), ids.UUID(id), override)
	if err != nil {
		var unreadable *unreadableError
		switch {
		case errors.As(err, &unreadable):
			// The client sees a generic 422; the real cause (SSRF refusal,
			// timeout, thin page, empty gate) stays server-side.
			slog.ErrorContext(r.Context(), "company enrichment unreadable", "org", ids.UUID(id), "err", unreadable.cause)
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusUnprocessableEntity,
				Code:   "company_unreadable",
				Detail: "Couldn't read enough from this company's site. Retry or add a URL.",
			})
		case errors.Is(err, people.ErrNoEnrichTarget):
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusUnprocessableEntity,
				Code:   "company_unreadable",
				Detail: "This company has no website on file. Add a URL to read from.",
			})
		default:
			httperr.Write(w, r, err)
		}
		return
	}
	httperr.WriteJSON(w, http.StatusOK, proposal)
}
