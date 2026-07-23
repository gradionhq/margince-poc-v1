// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file is the overlay Provider's write half: the datasource
// SystemOfRecordProvider write verbs over the incumbent-first write-back
// path (design.md §4.5, OVA-MAP-W). Create/Update/Archive project the
// canonical write onto the incumbent BELOW the seam (the adapter's
// mapWrite), write incumbent-first, and re-mirror the incumbent's returned
// state; the drift check (AC-OV-4) lives in the adapter's Update. Merge,
// PromoteLead, and AdvanceDeal stay unsupported (OVA-MAP-W6 + the missing
// overlay stage-map) — see each method.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// errNoWriteIncumbent is the honest answer a supported write verb gives
// when the Provider has no incumbent write resolver wired (only the
// write-verb unit tests reach this) — a clear, actionable configuration
// error rather than a nil-pointer panic, and NOT ErrUnsupportedBySoR
// (Create/Update/Archive are supported verbs; the resolver is just absent).
func errNoWriteIncumbent() error {
	return fmt.Errorf("overlay: provider has no incumbent write resolver configured")
}

// writeIncumbent resolves the acting workspace's live incumbent for a
// write. It never returns nil without an error.
//
//nolint:ireturn // resolving the incumbent seam interface IS the purpose — the write path is incumbent-agnostic by design (design.md §4.5)
func (p *Provider) writeIncumbent(ctx context.Context) (Incumbent, error) {
	if p.resolveIncumbent == nil {
		return nil, errNoWriteIncumbent()
	}
	return p.resolveIncumbent(ctx)
}

// canonicalFields normalizes the frozen seam's typed write payload
// (CreateInput.Fields / UpdateInput.Patch — the contract request struct in
// process, raw agent JSON over MCP) into the canonical field bag the
// adapter's mapWrite projects onto incumbent properties. The keys are the
// contract's JSON field names (first_name, amount_minor, kind, …), exactly
// what mapWrite consumes. An empty payload is an empty bag, not an error —
// the adapter decides whether a no-writable-field write is a no-op (Update)
// or an error (Create).
//
//craft:ignore naked-any the frozen seam's write payload is declared any (ports/datasource); this normalizes it into the canonical bag
func canonicalFields(v any) (map[string]any, error) {
	raw, err := datasource.RawFields(v)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if len(raw) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("overlay: decoding write fields: %w", err)
	}
	return fields, nil
}

// Create writes a new record to the incumbent (incumbent-first, AC-OV-4)
// and mirrors the incumbent's returned state. The object RBAC gate runs
// FIRST — the same MCP-bypass closure the read verbs carry, since the MCP
// write path reaches the provider directly. The canonical write is
// projected onto incumbent properties BELOW the seam (the adapter's
// mapWrite, OVA-MAP-W); the Provider stays incumbent-agnostic.
func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	if err := auth.Require(ctx, string(in.EntityType), principal.ActionCreate); err != nil {
		return datasource.EntityRef{}, err
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	// Assert the mirror store up front — like Update/Archive — so a mis-wired
	// provider fails BEFORE POSTing to the incumbent, never leaving an
	// orphaned incumbent record that was never mirrored.
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	fields, err := canonicalFields(in.Fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	rec, err := inc.Create(ctx, string(in.EntityType), fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.mirrorWriteResult(ctx, rec); err != nil {
		return datasource.EntityRef{}, err
	}
	id, err := externalIDToUUID(rec.ExternalID)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	return datasource.EntityRef{Type: in.EntityType, ID: id}, nil
}

// Update applies a patch incumbent-first after the stored-baseline drift
// check (AC-OV-4): the mirror row supplies the baseline captured at
// mirror-read, and the adapter refuses with ErrVersionSkew (surfaced as a
// 412 by the transport) if the incumbent moved since — never a blind
// overwrite. On success the incumbent's returned state is re-mirrored.
//
// Overlay's optimistic concurrency IS this incumbent stored-baseline drift
// check, not a mirror row version: an overlay read carries no integer
// Version (recordFromRow), so in.IfVersion has nothing to compare against
// here and the incumbent's own record clock is the authority (design.md
// §4.5).
func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	if err := auth.Require(ctx, string(in.Ref.Type), principal.ActionUpdate); err != nil {
		return datasource.EntityRef{}, err
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	externalID := uuidToExternalID(in.Ref.ID)
	// The mirror row supplies both the row-scope visibility gate (Get is
	// visibility-joined) and the drift-check baseline. A row the actor
	// cannot see is ErrNotFound here, exactly as on read.
	row, err := p.ms.Get(ctx, string(in.Ref.Type), externalID)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	fields, err := canonicalFields(in.Patch)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	// An activity patch selects its incumbent engagement class by kind
	// (OVA-MAP-W3); a partial patch may omit it, so carry the mirror row's
	// kind forward — it is consistent with the namespaced external_id by
	// construction (the read mapping set both from the source class).
	if in.Ref.Type == datasource.EntityActivity {
		if _, ok := fields["kind"]; !ok {
			if kind, ok := row.Fields["kind"]; ok {
				fields["kind"] = kind
			}
		}
	}
	rec, err := inc.Update(ctx, string(in.Ref.Type), externalID, fields, row.UpdatedAtBaseline)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.mirrorWriteResult(ctx, rec); err != nil {
		return datasource.EntityRef{}, err
	}
	return in.Ref, nil
}

// Archive removes a record from the incumbent (its own archive/delete) and
// purges the mirror row so it stops being readable, rather than lingering
// visible until the next sync.
func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	if err := auth.Require(ctx, string(r.Type), principal.ActionDelete); err != nil {
		return datasource.EntityRef{}, err
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	externalID := uuidToExternalID(r.ID)
	// Row-scope gate before the destructive incumbent call: a record the
	// actor cannot see is ErrNotFound, never archived on their behalf.
	if _, err := p.ms.Get(ctx, string(r.Type), externalID); err != nil {
		return datasource.EntityRef{}, err
	}
	if err := inc.Archive(ctx, string(r.Type), externalID); err != nil {
		return datasource.EntityRef{}, err
	}
	if _, err := p.ms.PurgeRecord(ctx, Deletion{ObjectClass: string(r.Type), ExternalID: externalID}); err != nil {
		return datasource.EntityRef{}, err
	}
	return r, nil
}

// mirrorWriteResult ingests the incumbent's post-write state into the
// mirror so a follow-up read sees the write without waiting for the sync
// poller. Ingest's staleness guard admits it (the write bumped the
// incumbent's baseline past the mirror's), and the mirror stays
// non-authoritative (T2) — the incumbent remains the system of record.
func (p *Provider) mirrorWriteResult(ctx context.Context, rec Record) error {
	if p.ms == nil {
		return errNoMirrorStore()
	}
	return p.ms.Ingest(ctx, rec)
}

// AdvanceDeal is unsupported in overlay V1: advancing an overlay deal
// resolves its target through the overlay stage-mapping to the incumbent
// dealstage (OVA-MAP-W4), but that incumbent stage-map substrate does not
// exist yet — it is the SAME missing source StageSemantic declares
// unsupported. Implemented together with the stage-map, not faked with a
// native UUID overlay deals never carry.
func (p *Provider) AdvanceDeal(_ context.Context, _ datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// Merge is unsupported in overlay (OVA-MAP-W6): a cross-aggregate
// lifecycle orchestration with no single incumbent-first projection —
// staged for V1 rather than a partial, non-atomic incumbent write.
func (p *Provider) Merge(_ context.Context, _ datasource.MergeInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// PromoteLead is unsupported in overlay (OVA-MAP-W6): like Merge, a
// cross-type materialization with no atomic incumbent-first projection.
func (p *Provider) PromoteLead(_ context.Context, _ ids.UUID, _ string, _ *string) (datasource.EntityRef, bool, error) {
	return datasource.EntityRef{}, false, apperrors.ErrUnsupportedBySoR
}
