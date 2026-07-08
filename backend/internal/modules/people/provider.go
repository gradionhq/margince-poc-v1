// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The people slice of the SoR-mode SystemOfRecordProvider (interfaces.md
// §3): person, organization and lead verbs over the module store — the
// same entry points the HTTP handlers use, with the same RBAC, row
// scope, audit and event shape. The composition root assembles the
// module providers into the one datasource seam the MCP surface binds.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider answers the datasource verbs for person|organization|lead.
type Provider struct {
	store *Store
}

func NewProvider(pool *pgxpool.Pool) *Provider {
	return &Provider{store: NewStore(pool)}
}

func ref(t datasource.EntityType, id openapi_types.UUID) datasource.EntityRef {
	return datasource.EntityRef{Type: t, ID: ids.UUID(id)}
}

func (p *Provider) Read(ctx context.Context, r datasource.EntityRef) (datasource.Record, error) {
	switch r.Type {
	case datasource.EntityPerson:
		v, err := p.store.GetPerson(ctx, ids.From[ids.PersonKind](r.ID), storekit.LiveOnly)
		if err != nil {
			return datasource.Record{}, err
		}
		return datasource.NewRecord(r, v, v.Version)
	case datasource.EntityOrganization:
		v, err := p.store.GetOrganization(ctx, ids.From[ids.OrganizationKind](r.ID), storekit.LiveOnly)
		if err != nil {
			return datasource.Record{}, err
		}
		return datasource.NewRecord(r, v, v.Version)
	case datasource.EntityLead:
		v, err := p.store.GetLead(ctx, ids.From[ids.LeadKind](r.ID), storekit.LiveOnly)
		if err != nil {
			return datasource.Record{}, err
		}
		return datasource.NewRecord(r, v, v.Version)
	default:
		return datasource.Record{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
}

// SearchEntity lists one of this module's entity types under the shared
// search contract (text query, CAP-PAGE limit, per-entity keyset cursor).
func (p *Provider) SearchEntity(ctx context.Context, t datasource.EntityType, text *string, limit int, cursor *string) ([]datasource.Record, string, bool, error) {
	var (
		records []datasource.Record
		next    string
		more    bool
	)
	appendRec := func(r datasource.EntityRef, fields any, version *int64) error {
		rec, err := datasource.NewRecord(r, fields, version)
		if err != nil {
			return err
		}
		records = append(records, rec)
		return nil
	}
	switch t {
	case datasource.EntityPerson:
		rows, page, err := p.store.ListPeople(ctx, ListPeopleInput{Query: text, Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, "", false, err
		}
		for _, v := range rows {
			if err := appendRec(ref(datasource.EntityPerson, v.Id), v, v.Version); err != nil {
				return nil, "", false, err
			}
		}
		next, more = page.NextCursor, page.HasMore
	case datasource.EntityOrganization:
		rows, page, err := p.store.ListOrganizations(ctx, ListOrganizationsInput{Query: text, Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, "", false, err
		}
		for _, v := range rows {
			if err := appendRec(ref(datasource.EntityOrganization, v.Id), v, v.Version); err != nil {
				return nil, "", false, err
			}
		}
		next, more = page.NextCursor, page.HasMore
	case datasource.EntityLead:
		rows, page, err := p.store.ListLeads(ctx, ListLeadsInput{Query: text, Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, "", false, err
		}
		for _, v := range rows {
			if err := appendRec(ref(datasource.EntityLead, v.Id), v, v.Version); err != nil {
				return nil, "", false, err
			}
		}
		next, more = page.NextCursor, page.HasMore
	default:
		return nil, "", false, &datasource.UnsupportedEntityError{Type: string(t)}
	}
	return records, next, more, nil
}

func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	raw, err := datasource.RawFields(in.Fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	switch in.EntityType {
	case datasource.EntityPerson:
		var req crmcontracts.CreatePersonRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
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
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		mapped, err := organizationCreateInput(req)
		if err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.CreateOrganization(ctx, mapped)
		return ref(datasource.EntityOrganization, v.Id), err
	case datasource.EntityLead:
		var req crmcontracts.CreateLeadRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		req.Source = in.Source
		v, _, err := p.store.CreateLead(ctx, leadCreateInput(req))
		return ref(datasource.EntityLead, v.Id), err
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
	case datasource.EntityPerson:
		var req crmcontracts.UpdatePersonRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdatePerson(ctx, ids.From[ids.PersonKind](in.Ref.ID), personUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		var req crmcontracts.UpdateOrganizationRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateOrganization(ctx, ids.From[ids.OrganizationKind](in.Ref.ID), organizationUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityOrganization, v.Id), err
	case datasource.EntityLead:
		var req crmcontracts.UpdateLeadRequest
		if err := datasource.StrictDecode(raw, &req); err != nil {
			return datasource.EntityRef{}, err
		}
		v, err := p.store.UpdateLead(ctx, ids.From[ids.LeadKind](in.Ref.ID), leadUpdateInput(req, in.IfVersion))
		return ref(datasource.EntityLead, v.Id), err
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Ref.Type)}
	}
}

func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	switch r.Type {
	case datasource.EntityPerson:
		v, err := p.store.ArchivePerson(ctx, ids.From[ids.PersonKind](r.ID))
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		v, err := p.store.ArchiveOrganization(ctx, ids.From[ids.OrganizationKind](r.ID))
		return ref(datasource.EntityOrganization, v.Id), err
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
}

// Merge folds source into target for person/organization and returns the
// survivor's ref. The store owns the collision-aware relink, the
// restrictive consent rule, and the single audit transaction.
func (p *Provider) Merge(ctx context.Context, in datasource.MergeInput) (datasource.EntityRef, error) {
	switch in.Type {
	case datasource.EntityPerson:
		v, err := p.store.MergePerson(ctx, ids.From[ids.PersonKind](in.SourceID), ids.From[ids.PersonKind](in.TargetID))
		return ref(datasource.EntityPerson, v.Id), err
	case datasource.EntityOrganization:
		v, err := p.store.MergeOrganization(ctx, ids.From[ids.OrganizationKind](in.SourceID), ids.From[ids.OrganizationKind](in.TargetID))
		return ref(datasource.EntityOrganization, v.Id), err
	default:
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Type)}
	}
}

// PromoteLead exposes the features/01 §6.4 graduation to the tool surface
// (a provider extension: interfaces.md §3 has no promotion verb yet).
func (p *Provider) PromoteLead(ctx context.Context, id ids.UUID, trigger string, evidenceNote *string) (datasource.EntityRef, bool, error) {
	person, merged, err := p.store.PromoteLead(ctx, ids.From[ids.LeadKind](id), PromoteLeadInput{
		Trigger: trigger, EvidenceNote: evidenceNote,
	})
	return ref(datasource.EntityPerson, person.Id), merged, err
}
