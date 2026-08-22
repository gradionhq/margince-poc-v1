// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// read_project_360 (🟢): the project page, readable. Every section is a read
// the agent already holds through read_record, list_records and
// search_records, so withholding the assembled page while granting all of
// its parts is the distinction A139 refused to draw for the brief. It is
// assembled by the same service the HTTP page is (compose/project360), under
// the same per-section grants and row scope, and it writes nothing.
//
// The answer is this module's OWN shape rather than the contract's. The
// contract types carry custom-field maps through their own marshaller, which
// the schema deriver can only describe as a string; the flat shape below is
// what the deriver can hold a result to, and it carries the fields an agent
// acts on rather than every column the page renders.

import (
	"context"
	"encoding/json"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Project360Reader serves one project's assembled page under the caller's
// gates. Compose binds it to the page's own service.
type Project360Reader func(ctx context.Context, projectID ids.UUID) (crmcontracts.Project360, error)

// RegisterProject360Tool joins read_project_360 to the surface once a reader
// exists — the conditional registration the other injected-engine tools take.
func RegisterProject360Tool(r *Registry, read Project360Reader) {
	if read == nil {
		return
	}
	r.Register(readProject360{read: read})
}

type readProject360 struct{ read Project360Reader }

func (t readProject360) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_project_360", Title: "Read a project's page", Version: toolVersionV1,
		Description:   readProject360Copy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getProject360",
		InputSchema: schema(`{"type":"object","required":["project_id"],"properties":{
			"project_id":{"type":"string","format":"uuid","description":"The project to read"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[Project360Result](),
	}
}

func (t readProject360) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ProjectID ids.UUID `json:"project_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	page, err := t.read(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	result := project360Result(page)
	// The page carries names, subjects and reasons read off rows this call
	// does not hand over whole, so the answer is tainted with their content;
	// and every record it NAMES is charged, at the point it is handed over.
	noteDerivedContent(ctx)
	chargeProject360(ctx, result)
	if result.truncated() {
		noteWarning(ctx, warningSweepTruncated, project360TruncatedMessage)
	}
	return json.Marshal(result)
}

// project360TruncatedMessage is the rule every bounded read on this surface
// states: a section cut at its cap is a page of the collection, not the
// collection, and the owning list verb has the rest.
const project360TruncatedMessage = "At least one section of this page was cut at 25 rows. Treat it as a " +
	"page, not the whole collection — list_records and search_records page the rest."

// chargeProject360 charges every record the page names: the project, the
// company, each deal, each seated person, each open task and each timeline
// row. Naming a record to an agent is handing that record over.
func chargeProject360(ctx context.Context, r Project360Result) {
	noteEvidence(ctx, datasource.EntityProject, r.Project.ProjectID)
	if r.Organization != nil {
		noteEvidence(ctx, datasource.EntityOrganization, r.Organization.OrganizationID)
	}
	if r.Deals != nil {
		for _, d := range r.Deals.Items {
			noteEvidence(ctx, datasource.EntityDeal, d.DealID)
		}
	}
	if r.Stakeholders != nil {
		for _, s := range r.Stakeholders.Items {
			noteEvidence(ctx, datasource.EntityPerson, s.PersonID)
		}
	}
	if r.Commitments != nil {
		for _, c := range r.Commitments.Items {
			noteEvidence(ctx, datasource.EntityActivity, c.TaskID)
		}
	}
	if r.Activities != nil {
		for _, a := range r.Activities.Items {
			noteEvidence(ctx, datasource.EntityActivity, a.ActivityID)
		}
	}
}
