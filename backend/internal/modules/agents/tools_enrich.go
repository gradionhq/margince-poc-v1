// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// enrich (EP05 / ADR-0006): read a company's own website and PROPOSE what it
// says about them. It is the one tool that reaches outside the workspace to
// fetch rather than to deliver, which is why it spends the `enrich` cap and not
// `write` — a granting human who withheld `enrich` withheld exactly this.
//
// Nothing is written to the organization here. The proposal carries per-field
// evidence and lands in the approvals inbox; a human accepting it fills only
// EMPTY fields and never overwrites a human-set value. Two approvals are in
// play and they are not the same one: the transport gate stages the CALL
// (confirm-first, because the fetch spends budget and leaves the workspace),
// and the engine stages the PROPOSAL a human then accepts.
//
// The evidence gate is the engine's, not this tool's: every returned field
// carries a non-empty snippet + source_url + confidence or it is absent. A tool
// that re-derived that rule would be a second answer to it.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// EnrichDepth is how much of the site a call reads. Its two values are two
// contract operations behind one verb, so the argument is what chooses between
// them rather than the client picking a route.
//
// A named type rather than a bare string, because the two doors reach this
// vocabulary from opposite directions: the tool parses one of two words out of
// its arguments, and the REST door has no word at all — its two routes ARE the
// two depths, so its decoders set the value structurally (commandrecord.go).
// A string would let the second of those be spelled wrong in a way every
// schema accepts (gradionhq/margince-poc-v1#928 task 7).
//
// EXPORTED because the implementation of CompanyEnricher lives in the
// composition layer and routes on it — a second spelling there would silently
// send a site read to the one-page path.
type EnrichDepth string

// The two depths, and the whole vocabulary: EnrichDepthPage reads one page and
// answers with the proposal, EnrichDepthSite queues a multi-page crawl and
// answers with the read's id and queue state.
const (
	EnrichDepthPage EnrichDepth = "page"
	EnrichDepthSite EnrichDepth = "site"
)

// CompanyEnricher reads a company's website and stages what it found. depth is
// EnrichDepthPage (one page, answers with the proposal) or EnrichDepthSite (a
// queued multi-page crawl, answers with the read's id and queue state).
type CompanyEnricher interface {
	EnrichCompany(ctx context.Context, orgID ids.UUID, overrideURL string, depth EnrichDepth) (json.RawMessage, error)
}

// RegisterEnrichTool wires the enrich verb over the site-read seam.
func RegisterEnrichTool(r *Registry, p datasource.SystemOfRecordProvider, enricher CompanyEnricher) {
	r.Register(enrichCompany{p: p, enricher: enricher})
}

type enrichArgs struct {
	OrganizationID ids.UUID    `json:"organization_id"`
	URL            string      `json:"url"`
	Depth          EnrichDepth `json:"depth"`
}

type enrichCompany struct {
	p        datasource.SystemOfRecordProvider
	enricher CompanyEnricher
}

func (t enrichCompany) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "enrich", Title: "Enrich an organization from its website", Version: toolVersionV1,
		Description:   enrichCopy.render(),
		RequiredScope: principal.ScopeEnrich, Tier: mcp.TierConfirmationRequired, Egress: true,
		OpenAPIOp: "scrapeCompany/deepReadCompany",
		InputSchema: schema(`{"type":"object","required":["organization_id"],"properties":{
			"organization_id":{"type":"string","format":"uuid","description":"The organization to enrich"},
			"url":{"type":"string","format":"uri",
				"description":"Absolute http(s) URL to read instead of the organization's own domain"},
			"depth":{"type":"string","enum":["page","site"],"default":"page",
				"description":"page reads one page and returns a staged proposal; site queues a multi-page crawl and returns its read id"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		// The other declared exception. This tool answers one of two different
		// things depending on the depth it was called with — a page read comes
		// back as a staged field proposal, a site read as the id of a crawl that
		// has not finished — and each half is the shape of the engine that
		// produced it, not of anything this module owns. A schema for one would
		// be wrong for the other, and a union of both would tell every caller to
		// handle a case its own arguments rule out.
		OutputSchema: schema(`{"type":"object"}`),
	}
}

// StageInfo decodes this door's arguments into the enrich command and
// delegates: the refusals and the staged subject live in the resolver
// (commandrecord.go), where the REST door reaches the same ones for the same
// operation.
//
// The depth is admitted HERE and typed by the command, which is the division
// this verb needs. This door receives a WORD and has to decide whether it is
// one of the two; the REST door receives no word at all — its two routes are
// the two depths — so the vocabulary check belongs to the door that has a
// string to check, and the command carries only the resolved value.
func (t enrichCompany) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	args, err := readEnrichArgs(in)
	if err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewEnrichCall(t.p, EnrichCommand(args)))
}

func (t enrichCompany) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := readEnrichArgs(in)
	if err != nil {
		return nil, err
	}
	// A crawl reads a website. Whatever it answers with came from outside the
	// workspace entirely, which is the plainest T2 there is.
	noteDerivedContent(ctx)
	noteEvidence(ctx, datasource.EntityOrganization, args.OrganizationID)
	return t.enricher.EnrichCompany(ctx, args.OrganizationID, args.URL, args.Depth)
}

// readEnrichArgs decodes, defaults the depth and admits the override URL in one
// place, so a call this refuses can never reach a human's inbox on the staging
// path and then be refused differently on the execution path.
//
// The URL admission is the resolver's own function (requireEnrichURL,
// commandrecord.go), not a second copy of it. It is asked TWICE on the staging
// path — once here, once in Guards — because StageInfo needs this function for
// the depth defaulting anyway; that redundancy is the price of Handle, which an
// approved retry re-enters without passing Guards, sharing the one spelling
// rather than keeping its own.
func readEnrichArgs(in json.RawMessage) (enrichArgs, error) {
	var args enrichArgs
	if err := decodeArgs(in, &args); err != nil {
		return enrichArgs{}, err
	}
	if args.Depth == "" {
		args.Depth = EnrichDepthPage
	}
	if args.Depth != EnrichDepthPage && args.Depth != EnrichDepthSite {
		return enrichArgs{}, &BadArgsError{Cause: fmt.Errorf("depth %q is not %q or %q",
			args.Depth, EnrichDepthPage, EnrichDepthSite)}
	}
	if err := requireEnrichURL(args.URL); err != nil {
		return enrichArgs{}, err
	}
	return args, nil
}
