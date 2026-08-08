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
	"net/url"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The depth vocabulary: how much of the site a call reads. The two values are
// two contract operations behind one verb, so the argument is what chooses
// between them rather than the client picking a route. EXPORTED because the
// implementation of CompanyEnricher lives in the composition layer and routes
// on them — a second spelling there would silently send a site read to the
// one-page path.
const (
	EnrichDepthPage = "page"
	EnrichDepthSite = "site"
)

// CompanyEnricher reads a company's website and stages what it found. depth is
// EnrichDepthPage (one page, answers with the proposal) or EnrichDepthSite (a
// queued multi-page crawl, answers with the read's id and queue state).
type CompanyEnricher interface {
	EnrichCompany(ctx context.Context, orgID ids.UUID, overrideURL, depth string) (json.RawMessage, error)
}

// RegisterEnrichTool wires the enrich verb over the site-read seam.
func RegisterEnrichTool(r *Registry, p datasource.SystemOfRecordProvider, enricher CompanyEnricher) {
	r.Register(enrichCompany{p: p, enricher: enricher})
}

type enrichArgs struct {
	OrganizationID ids.UUID `json:"organization_id"`
	URL            string   `json:"url"`
	Depth          string   `json:"depth"`
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

func (t enrichCompany) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	args, err := readEnrichArgs(in)
	if err != nil {
		return StageInfo{}, err
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityOrganization, ID: args.OrganizationID})
	if err != nil {
		return StageInfo{}, err
	}
	if err := refuseStagingElsewhere(rec); err != nil {
		return StageInfo{}, err
	}
	target := "its own domain"
	if args.URL != "" {
		target = args.URL
	}
	return StageInfo{
		TargetType: string(datasource.EntityOrganization), TargetID: args.OrganizationID, TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Read %s from %s and propose enrichment of %s",
			args.Depth, target, recordLabel(rec)),
	}, nil
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
	if args.URL != "" {
		// The same admission the REST route applies before it fetches: a
		// scheme-less or hostless target is a bad argument, not a thin page.
		parsed, err := url.Parse(args.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return enrichArgs{}, &BadArgsError{Cause: fmt.Errorf("url %q must be an absolute http(s) URL", args.URL)}
		}
	}
	return args, nil
}
