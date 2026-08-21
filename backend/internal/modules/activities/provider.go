// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The activities slice of the SoR-mode SystemOfRecordProvider
// (interfaces.md §3): read + log. Activities are deliberately absent
// from the search sweep — the timeline is reached through
// read_record/list on a named entity, not blind full-text sweep.

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider answers the datasource verbs for activity.
type Provider struct {
	store *Store
}

// NewProvider builds this module's system-of-record provider over a
// workspace-bound handle.
func NewProvider(db *database.DB) *Provider {
	return &Provider{store: NewStore(db)}
}

// WithTranscriptEnqueue lets a transcript created through this seam start its
// own reading, the same way one created over REST does. Without it the tool
// surface can store a transcript and nothing reads it — which is how the
// extraction lane came to hold zero rows while being fully built.
func (p *Provider) WithTranscriptEnqueue(enqueue TranscriptReadEnqueue) *Provider {
	p.store = p.store.WithTranscriptEnqueue(enqueue)
	return p
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

// Update patches an activity's own fields. UpdateActivityRequest carries no
// link field, so this cannot change who an activity is about — only what it
// says and when it happened; associations are the relink verb's, with its own
// audit action.
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

// Archive retires an activity, the same soft archive DELETE /v1/activities/{id}
// performs — the contract has always declared archiveActivity as archive_record's
// (agentPolicies), and the seam is what was missing: the tool staged a
// confirmation, a human approved it, and the call then died at this switch with
// `unsupported_entity_type`. An approval spent on a verb that could never run.
func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	return p.ArchiveAt(ctx, datasource.ArchiveInput{Ref: r})
}

// ArchivableTypes is datasource.RecordArchiverV2's: this module archives the
// one entity it owns.
func (p *Provider) ArchivableTypes(context.Context) ([]datasource.EntityType, error) {
	return []datasource.EntityType{datasource.EntityActivity}, nil
}

// RefuseArchive is datasource.RecordArchiverV2's stage-time half: the store's
// own authority probes, run without the write.
func (p *Provider) RefuseArchive(ctx context.Context, r datasource.EntityRef) error {
	if r.Type != datasource.EntityActivity {
		return &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
	return p.store.RefuseArchiveActivity(ctx, ids.From[ids.ActivityKind](r.ID))
}

// ArchiveAt is Archive carrying the version the caller's authority named.
func (p *Provider) ArchiveAt(ctx context.Context, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	if in.Ref.Type != datasource.EntityActivity {
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(in.Ref.Type)}
	}
	v, err := p.store.ArchiveActivity(ctx, ids.From[ids.ActivityKind](in.Ref.ID), in.IfVersion)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	return ref(datasource.EntityActivity, v.Id), nil
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
	mapped, err := LogActivityInputFrom(req)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	v, _, err := p.store.LogActivity(ctx, mapped)
	return ref(datasource.EntityActivity, v.Id), err
}
