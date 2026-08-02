// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The activities slice of the SoR-mode SystemOfRecordProvider
// (interfaces.md §3): read + log. Activities are deliberately absent
// from the search sweep — the timeline is reached through
// read_record/list on a named entity, not blind full-text sweep.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider answers the datasource verbs for activity.
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
	if r.Type != datasource.EntityActivity {
		return datasource.Record{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
	v, err := p.store.GetActivity(ctx, ids.From[ids.ActivityKind](r.ID), storekit.LiveOnly)
	if err != nil {
		return datasource.Record{}, err
	}
	return datasource.NewRecord(r, v, v.Version)
}

// Update patches an activity's own fields. What it deliberately does NOT
// serve is the links: an activity's associations move through the relink verb,
// which is a different governed action with its own audit action, and
// UpdateActivityRequest carries no link field at all. So this cannot change
// who an activity is about — only what it says and when it happened.
func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	if in.Ref.Type != datasource.EntityActivity {
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Ref.Type)}
	}
	raw, err := datasource.RawFields(in.Patch)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	var req crmcontracts.UpdateActivityRequest
	if err := datasource.StrictDecode(raw, &req); err != nil {
		return datasource.EntityRef{}, err
	}
	v, err := p.store.UpdateActivity(ctx, ids.From[ids.ActivityKind](in.Ref.ID),
		activityUpdateInput(req, in.IfVersion))
	return ref(datasource.EntityActivity, v.Id), err
}

func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	if in.EntityType != datasource.EntityActivity {
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.EntityType)}
	}
	raw, err := datasource.RawFields(in.Fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	var req crmcontracts.CreateActivityRequest
	if err := datasource.StrictDecode(raw, &req); err != nil {
		return datasource.EntityRef{}, err
	}
	req.Source = in.Source
	mapped, err := activityLogInput(req)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	v, _, err := p.store.LogActivity(ctx, mapped)
	return ref(datasource.EntityActivity, v.Id), err
}
