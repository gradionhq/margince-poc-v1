package crmcore

// The SoR-mode SystemOfRecordProvider (interfaces.md §3): crm-core IS the
// system of record, so the seam binds straight onto the store — the same
// entry points the HTTP handlers use, with the same RBAC, row scope,
// audit and event shape. The MCP tool surface composes over this seam so
// it works unchanged when an overlay adapter replaces it (03e AC-OV-2).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/crm-core/internal/store"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider implements datasource.SystemOfRecordProvider over the store.
type Provider struct {
	store *store.Store
}

func NewProvider(pool *pgxpool.Pool) *Provider {
	return &Provider{store: store.New(pool)}
}

var _ datasource.SystemOfRecordProvider = (*Provider)(nil)

// searchable is the entity set Search sweeps when the query names none.
// Activities are deliberately absent: the timeline is reached through
// read_record/list on a named entity, not blind full-text sweep.
var searchable = []datasource.EntityType{datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityDeal, datasource.EntityLead}

func (p *Provider) Read(ctx context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	var (
		fields  any
		version *int64
		err     error
	)
	switch ref.Type {
	case datasource.EntityPerson:
		var v crmcontracts.Person
		v, err = p.store.GetPerson(ctx, ref.ID, false)
		fields, version = v, v.Version
	case datasource.EntityOrganization:
		var v crmcontracts.Organization
		v, err = p.store.GetOrganization(ctx, ref.ID, false)
		fields, version = v, v.Version
	case datasource.EntityDeal:
		var v crmcontracts.Deal
		v, err = p.store.GetDeal(ctx, ref.ID, false)
		fields, version = v, v.Version
	case datasource.EntityLead:
		var v crmcontracts.Lead
		v, err = p.store.GetLead(ctx, ref.ID, false)
		fields, version = v, v.Version
	case datasource.EntityActivity:
		var v crmcontracts.Activity
		v, err = p.store.GetActivity(ctx, ref.ID, false)
		fields, version = v, v.Version
	default:
		return datasource.Record{}, &UnsupportedEntityError{Type: string(ref.Type)}
	}
	if err != nil {
		return datasource.Record{}, err
	}
	return p.record(ref, fields, version)
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
			return datasource.SearchResult{}, errors.New("crmcore: search cursor requires exactly one entity_type")
		}
		cursor = &q.Cursor
	}

	out := datasource.SearchResult{Records: []datasource.Record{}}
	for _, t := range types {
		var (
			page store.Page
			err  error
		)
		switch t {
		case datasource.EntityPerson:
			var rows []crmcontracts.Person
			rows, page, err = p.store.ListPeople(ctx, store.ListPeopleInput{Query: text, Limit: &limit, Cursor: cursor})
			for _, v := range rows {
				rec, err := p.record(ref(datasource.EntityPerson, v.Id), v, v.Version)
				if err != nil {
					return datasource.SearchResult{}, err
				}
				out.Records = append(out.Records, rec)
			}
		case datasource.EntityOrganization:
			var rows []crmcontracts.Organization
			rows, page, err = p.store.ListOrganizations(ctx, store.ListOrganizationsInput{Query: text, Limit: &limit, Cursor: cursor})
			for _, v := range rows {
				rec, err := p.record(ref(datasource.EntityOrganization, v.Id), v, v.Version)
				if err != nil {
					return datasource.SearchResult{}, err
				}
				out.Records = append(out.Records, rec)
			}
		case datasource.EntityDeal:
			var rows []crmcontracts.Deal
			rows, page, err = p.store.ListDeals(ctx, store.ListDealsInput{Query: text, Limit: &limit, Cursor: cursor})
			for _, v := range rows {
				rec, err := p.record(ref(datasource.EntityDeal, v.Id), v, v.Version)
				if err != nil {
					return datasource.SearchResult{}, err
				}
				out.Records = append(out.Records, rec)
			}
		case datasource.EntityLead:
			var rows []crmcontracts.Lead
			rows, page, err = p.store.ListLeads(ctx, store.ListLeadsInput{Query: text, Limit: &limit, Cursor: cursor})
			for _, v := range rows {
				rec, err := p.record(ref(datasource.EntityLead, v.Id), v, v.Version)
				if err != nil {
					return datasource.SearchResult{}, err
				}
				out.Records = append(out.Records, rec)
			}
		default:
			return datasource.SearchResult{}, &UnsupportedEntityError{Type: string(t)}
		}
		if err != nil {
			return datasource.SearchResult{}, err
		}
		if len(types) == 1 {
			out.NextCursor, out.HasMore = page.NextCursor, page.HasMore
		}
	}
	return out, nil
}

func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	raw, err := rawFields(in.Fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	switch in.EntityType {
	case datasource.EntityPerson:
		var req crmcontracts.CreatePersonRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := personCreateInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.CreatePerson(ctx, mapped)
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		var req crmcontracts.CreateOrganizationRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := organizationCreateInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.CreateOrganization(ctx, mapped)
		return ref(datasource.EntityOrganization, v.Id), err
	case datasource.EntityDeal:
		var req crmcontracts.CreateDealRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := dealCreateInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.CreateDeal(ctx, mapped)
		return ref(datasource.EntityDeal, v.Id), err
	case datasource.EntityLead:
		var req crmcontracts.CreateLeadRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		v, _, err := p.store.CreateLead(ctx, leadCreateInput(req))
		return ref(datasource.EntityLead, v.Id), err
	case datasource.EntityActivity:
		var req crmcontracts.CreateActivityRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := activityLogInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, _, err := p.store.LogActivity(ctx, mapped)
		return ref(datasource.EntityActivity, v.Id), err
	default:
		return datasource.EntityRef{}, &UnsupportedEntityError{Type: string(in.EntityType)}
	}
}

func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	raw, err := rawFields(in.Patch)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	switch in.Ref.Type {
	case datasource.EntityPerson:
		var req crmcontracts.UpdatePersonRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdatePerson(ctx, in.Ref.ID, personUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		var req crmcontracts.UpdateOrganizationRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateOrganization(ctx, in.Ref.ID, organizationUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityOrganization, v.Id), err
	case datasource.EntityDeal:
		var req crmcontracts.UpdateDealRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateDeal(ctx, in.Ref.ID, dealUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityDeal, v.Id), err
	case datasource.EntityLead:
		var req crmcontracts.UpdateLeadRequest
		if err := strictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateLead(ctx, in.Ref.ID, leadUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityLead, v.Id), err
	default:
		return datasource.EntityRef{}, &UnsupportedEntityError{Type: string(in.Ref.Type)}
	}
}

func (p *Provider) AdvanceDeal(ctx context.Context, in datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	v, err := p.store.AdvanceDeal(ctx, in.DealID, store.AdvanceDealInput{
		ToStageID:  in.ToStageID,
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

// Freshness in SoR-mode is trivially authoritative: there is no mirror to
// go stale (03e §2.3 — the overlay adapter is where this earns its keep).
func (p *Provider) Freshness(_ context.Context, _ datasource.EntityRef) (datasource.FreshnessInfo, error) {
	return datasource.FreshnessInfo{LastSyncedAt: time.Now().UTC(), Authoritative: true}, nil
}

// Schema introspection and the report engine are not built yet; the seam
// answers loudly rather than with an empty success.
func (p *Provider) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	return nil, errors.New("crmcore: ListObjects is not implemented yet")
}

func (p *Provider) ListFields(context.Context, datasource.EntityType) ([]datasource.FieldDef, error) {
	return nil, errors.New("crmcore: ListFields is not implemented yet")
}

func (p *Provider) RunReport(context.Context, datasource.ReportPlan) (datasource.ReportResult, error) {
	return datasource.ReportResult{}, errors.New("crmcore: RunReport is not implemented yet (the compiled report engine is a later work package)")
}

// UnsupportedEntityError maps to 422 on every surface.
type UnsupportedEntityError struct{ Type string }

func (e *UnsupportedEntityError) Error() string {
	return "entity_type " + e.Type + " is not person|organization|deal|lead|activity"
}

func (p *Provider) record(r datasource.EntityRef, fields any, version *int64) (datasource.Record, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return datasource.Record{}, err
	}
	rec := datasource.Record{Ref: r, Fields: raw, Freshness: datasource.FreshnessInfo{LastSyncedAt: time.Now().UTC(), Authoritative: true}}
	if version != nil {
		rec.Version = *version
	}
	return rec, nil
}

// ref converts a contract id into the seam's reference shape.
func ref(t datasource.EntityType, id openapi_types.UUID) datasource.EntityRef {
	return datasource.EntityRef{Type: t, ID: ids.UUID(id)}
}

// rawFields normalizes the seam's `any` payload: tools hand over the
// agent's raw JSON; in-process callers may hand the typed request struct.
func rawFields(v any) (json.RawMessage, error) {
	switch f := v.(type) {
	case json.RawMessage:
		return f, nil
	case []byte:
		return f, nil
	default:
		return json.Marshal(v)
	}
}

// strictDecode rejects unknown fields — an agent misspelling an argument
// gets a 422 naming it, never a silent drop.
func strictDecode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &FieldDecodeError{Cause: err}
	}
	return nil
}

// FieldDecodeError maps to 422 on every surface.
type FieldDecodeError struct{ Cause error }

func (e *FieldDecodeError) Error() string { return "fields: " + e.Cause.Error() }
func (e *FieldDecodeError) Unwrap() error { return e.Cause }

func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	switch r.Type {
	case datasource.EntityPerson:
		v, err := p.store.ArchivePerson(ctx, r.ID)
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		v, err := p.store.ArchiveOrganization(ctx, r.ID)
		return ref(datasource.EntityOrganization, v.Id), err
	case datasource.EntityDeal:
		v, err := p.store.ArchiveDeal(ctx, r.ID)
		return ref(datasource.EntityDeal, v.Id), err
	default:
		return datasource.EntityRef{}, &UnsupportedEntityError{Type: string(r.Type)}
	}
}

// Merge folds source into target for person/organization and returns the
// survivor's ref. The store owns the collision-aware relink, the
// restrictive consent rule, and the single audit transaction.
func (p *Provider) Merge(ctx context.Context, in datasource.MergeInput) (datasource.EntityRef, error) {
	switch in.Type {
	case datasource.EntityPerson:
		v, err := p.store.MergePerson(ctx, in.SourceID, in.TargetID)
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		v, err := p.store.MergeOrganization(ctx, in.SourceID, in.TargetID)
		return ref(datasource.EntityOrganization, v.Id), err
	default:
		return datasource.EntityRef{}, &UnsupportedEntityError{Type: string(in.Type)}
	}
}

// PromoteLead exposes the features/01 §6.4 graduation to the tool surface
// (a provider extension: interfaces.md §3 has no promotion verb yet —
// noted alongside the Archive gap in fable feedback/17).
func (p *Provider) PromoteLead(ctx context.Context, id ids.UUID, trigger string, evidenceNote *string) (datasource.EntityRef, bool, error) {
	person, merged, err := p.store.PromoteLead(ctx, id, store.PromoteLeadInput{
		Trigger: trigger, EvidenceNote: evidenceNote,
	})
	return ref(datasource.EntityPerson, person.Id), merged, err
}
