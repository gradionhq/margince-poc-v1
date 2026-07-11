// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package compose is the composition layer the process roles share
// (ADR-0054, amended §2): it assembles the module providers into the one
// datasource.SystemOfRecordProvider seam the MCP surface binds, and (via
// server.go) the module transports into the contract HTTP surface.
// Modules never see each other; every cross-module edge is wired here.
package compose

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider dispatches each datasource verb to the module that owns the
// entity type. It IS the system of record, so freshness is trivially
// authoritative (03e §2.3 — the overlay adapter is where that earns its
// keep).
type Provider struct {
	people     *people.Provider
	deals      *deals.Provider
	activities *activities.Provider
	reports    *reportEngine
}

func NewProvider(pool *pgxpool.Pool) *Provider {
	return &Provider{
		// The fieldcatalog seam mirrors the HTTP wiring (server.go): the
		// MCP surface's record verbs carry cf_* values too.
		people:     people.NewProvider(pool).WithFieldCatalog(customfields.NewService(pool, nil)),
		deals:      deals.NewProvider(pool),
		activities: activities.NewProvider(pool),
		reports:    newReportEngine(pool),
	}
}

var _ datasource.SystemOfRecordProvider = (*Provider)(nil)

// searchable is the entity set Search sweeps when the query names none.
// Activities are deliberately absent: the timeline is reached through
// read_record/list on a named entity, not blind full-text sweep.
var searchable = []datasource.EntityType{datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityDeal, datasource.EntityLead}

func (p *Provider) Read(ctx context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	switch ref.Type {
	case datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityLead:
		return p.people.Read(ctx, ref)
	case datasource.EntityDeal:
		return p.deals.Read(ctx, ref)
	case datasource.EntityActivity:
		return p.activities.Read(ctx, ref)
	default:
		return datasource.Record{}, &datasource.UnsupportedEntityError{Type: string(ref.Type)}
	}
}

func (p *Provider) Search(ctx context.Context, q datasource.SearchQuery) (datasource.SearchResult, error) {
	types := q.EntityTypes
	if len(types) == 0 {
		types = searchable
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	text := &q.Text
	if q.Text == "" {
		text = nil
	}
	// Keyset cursors are per-entity streams; a cross-type cursor would
	// have to interleave four of them. Honest bound: cursor pagination
	// needs exactly one entity type, multi-type queries get page one.
	var cursor *string
	if q.Cursor != "" {
		if len(types) != 1 {
			return datasource.SearchResult{}, errors.New("compose: search cursor requires exactly one entity_type")
		}
		cursor = &q.Cursor
	}

	out := datasource.SearchResult{Records: []datasource.Record{}}
	for _, t := range types {
		var (
			records []datasource.Record
			next    string
			more    bool
			err     error
		)
		switch t {
		case datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityLead:
			records, next, more, err = p.people.SearchEntity(ctx, t, text, limit, cursor)
		case datasource.EntityDeal:
			records, next, more, err = p.deals.SearchEntity(ctx, t, text, limit, cursor)
		default:
			return datasource.SearchResult{}, &datasource.UnsupportedEntityError{Type: string(t)}
		}
		if err != nil {
			return datasource.SearchResult{}, err
		}
		out.Records = append(out.Records, records...)
		if len(types) == 1 {
			out.NextCursor, out.HasMore = next, more
		}
	}
	return out, nil
}

func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	switch in.EntityType {
	case datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityLead:
		return p.people.Create(ctx, in)
	case datasource.EntityDeal:
		return p.deals.Create(ctx, in)
	case datasource.EntityActivity:
		return p.activities.Create(ctx, in)
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.EntityType)}
	}
}

func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	switch in.Ref.Type {
	case datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityLead:
		return p.people.Update(ctx, in)
	case datasource.EntityDeal:
		return p.deals.Update(ctx, in)
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Ref.Type)}
	}
}

func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	switch r.Type {
	case datasource.EntityPerson, datasource.EntityOrganization:
		return p.people.Archive(ctx, r)
	case datasource.EntityDeal:
		return p.deals.Archive(ctx, r)
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
}

func (p *Provider) Merge(ctx context.Context, in datasource.MergeInput) (datasource.EntityRef, error) {
	switch in.Type {
	case datasource.EntityPerson, datasource.EntityOrganization:
		return p.people.Merge(ctx, in)
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Type)}
	}
}

func (p *Provider) AdvanceDeal(ctx context.Context, in datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	return p.deals.AdvanceDeal(ctx, in)
}

// StageSemantic feeds the advance_deal tier resolver (interfaces.md
// §2.1) — part of the frozen v1 seam, resolved from pipeline config,
// never from labels or request args.
func (p *Provider) StageSemantic(ctx context.Context, stageID ids.UUID) (semantic string, pipelineID ids.UUID, err error) {
	return p.deals.StageSemantic(ctx, stageID)
}

// PromoteLead is the features/01 §6.4 graduation — a cross-module
// orchestration verb of the frozen v1 seam (interfaces.md §3), owned by
// the people module's transaction and dispatched here.
func (p *Provider) PromoteLead(ctx context.Context, id ids.UUID, trigger string, evidenceNote *string) (datasource.EntityRef, bool, error) {
	return p.people.PromoteLead(ctx, id, trigger, evidenceNote)
}

// Freshness in SoR-mode is trivially authoritative: there is no mirror
// to go stale.
func (p *Provider) Freshness(_ context.Context, _ datasource.EntityRef) (datasource.FreshnessInfo, error) {
	return datasource.FreshnessInfo{LastSyncedAt: time.Now().UTC(), Authoritative: true}, nil
}

// ListObjects/ListFields expose the SoR-mode schema descriptors
// (interfaces.md §3): static, versioned with the code (P11).
func (p *Provider) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	return schemaObjects, nil
}

func (p *Provider) ListFields(_ context.Context, entity datasource.EntityType) ([]datasource.FieldDef, error) {
	fields, ok := schemaFields(entity)
	if !ok {
		return nil, &datasource.UnsupportedEntityError{Type: string(entity)}
	}
	return fields, nil
}

// RunReport executes a seam-level plan against the descriptor
// vocabulary — the same engine the HTTP surface and the run_report
// tool ride.
func (p *Provider) RunReport(ctx context.Context, plan datasource.ReportPlan) (datasource.ReportResult, error) {
	return p.reports.runAdHocPlan(ctx, plan)
}
