// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The deals slice of the SoR-mode SystemOfRecordProvider (interfaces.md
// §3): deal verbs plus the stage-semantic probe the advance_deal tier
// resolver needs. The composition root assembles the module providers
// into the one datasource seam the MCP surface binds.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// Provider answers the datasource verbs for deal.
type Provider struct {
	store *Store
}

func NewProvider(pool *pgxpool.Pool) *Provider {
	return &Provider{store: NewStore(pool)}
}

// WithFieldCatalog wires the workspace custom-field catalog into the
// provider's store (see Store.WithFieldCatalog), so the MCP surface's
// record verbs carry cf values exactly like REST.
func (p *Provider) WithFieldCatalog(catalog fieldcatalog.Reader) *Provider {
	p.store = p.store.WithFieldCatalog(catalog)
	return p
}

func ref(t datasource.EntityType, id openapi_types.UUID) datasource.EntityRef {
	return datasource.EntityRef{Type: t, ID: ids.UUID(id)}
}

func (p *Provider) Read(ctx context.Context, r datasource.EntityRef) (datasource.Record, error) {
	switch r.Type {
	case datasource.EntityDeal:
		v, err := p.store.GetDeal(ctx, ids.From[ids.DealKind](r.ID), storekit.LiveOnly)
		if err != nil {
			return datasource.Record{}, err
		}
		return datasource.NewRecord(r, v, v.Version)
	case datasource.EntityProject:
		v, err := p.store.GetProject(ctx, ids.From[ids.ProjectKind](r.ID), storekit.LiveOnly)
		if err != nil {
			return datasource.Record{}, err
		}
		return datasource.NewRecord(r, v, v.Version)
	default:
		return datasource.Record{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
}

// SearchEntity lists deals under the shared search contract.
func (p *Provider) SearchEntity(ctx context.Context, t datasource.EntityType, text *string, limit int, cursor *string) ([]datasource.Record, string, bool, error) {
	switch t {
	case datasource.EntityDeal:
		rows, page, err := p.store.ListDeals(ctx, ListDealsInput{Query: text, Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, "", false, err
		}
		records := make([]datasource.Record, 0, len(rows))
		for _, v := range rows {
			rec, err := datasource.NewRecord(ref(datasource.EntityDeal, v.Id), v, v.Version)
			if err != nil {
				return nil, "", false, err
			}
			records = append(records, rec)
		}
		return records, page.NextCursor, page.HasMore, nil
	case datasource.EntityProject:
		rows, page, err := p.store.ListProjects(ctx, ListProjectsInput{Query: text, Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, "", false, err
		}
		records := make([]datasource.Record, 0, len(rows))
		for _, v := range rows {
			rec, err := datasource.NewRecord(ref(datasource.EntityProject, v.Id), v, v.Version)
			if err != nil {
				return nil, "", false, err
			}
			records = append(records, rec)
		}
		return records, page.NextCursor, page.HasMore, nil
	default:
		return nil, "", false, &datasource.UnsupportedEntityError{Type: string(t)}
	}
}

func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	raw, err := datasource.RawFields(in.Fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	switch in.EntityType {
	case datasource.EntityDeal:
		var req crmcontracts.CreateDealRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := dealCreateInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.CreateDeal(ctx, mapped)
		return ref(datasource.EntityDeal, v.Id), err
	case datasource.EntityProject:
		var req crmcontracts.CreateProjectRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := projectCreateInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.CreateProject(ctx, mapped)
		return ref(datasource.EntityProject, v.Id), err
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.EntityType)}
	}
}

func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	raw, err := datasource.RawFields(in.Patch)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	switch in.Ref.Type {
	case datasource.EntityDeal:
		var req crmcontracts.UpdateDealRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateDeal(ctx, ids.From[ids.DealKind](in.Ref.ID), dealUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityDeal, v.Id), err
	case datasource.EntityProject:
		var req crmcontracts.UpdateProjectRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		update, err := projectUpdateInput(req, in.IfVersion)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateProject(ctx, ids.From[ids.ProjectKind](in.Ref.ID), update)
		return ref(datasource.EntityProject, v.Id), err
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Ref.Type)}
	}
}

func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	switch r.Type {
	case datasource.EntityDeal:
		v, err := p.store.ArchiveDeal(ctx, ids.From[ids.DealKind](r.ID))
		return ref(datasource.EntityDeal, v.Id), err
	case datasource.EntityProject:
		// No If-Match on the seam's archive verb: the agent surface has no
		// version to carry, so the archive runs unguarded exactly as the
		// deal's does.
		v, err := p.store.ArchiveProject(ctx, ids.From[ids.ProjectKind](r.ID), nil)
		return ref(datasource.EntityProject, v.Id), err
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
}

func (p *Provider) AdvanceDeal(ctx context.Context, in datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	v, err := p.store.AdvanceDeal(ctx, ids.From[ids.DealKind](in.DealID), AdvanceDealInput{
		ToStageID:  ids.From[ids.StageKind](in.ToStageID),
		LostReason: in.LostReason,
		IfVersion:  in.IfVersion,
	})
	return ref(datasource.EntityDeal, v.Id), err
}

// StageSemantic feeds the advance_deal tier resolver (interfaces.md
// §2.1): won/lost is read from the target stage's configuration. Not part
// of the sor interface — the gate needs it before the provider verb runs.
func (p *Provider) StageSemantic(ctx context.Context, stageID ids.UUID) (semantic string, pipelineID ids.UUID, err error) {
	return p.store.StageSemantic(ctx, stageID)
}
