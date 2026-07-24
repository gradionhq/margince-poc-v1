// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file is the overlay Provider's write half: the datasource
// SystemOfRecordProvider write verbs over the incumbent-first write-back
// path (design.md §4.5, OVA-MAP-W). Create/Update/Archive project the
// canonical write onto the incumbent BELOW the seam (the adapter's
// mapWrite), write incumbent-first, and re-mirror the incumbent's returned
// state; the drift check (AC-OV-4) lives in the adapter's Update/Archive.
// Merge, PromoteLead, and AdvanceDeal stay unsupported (OVA-MAP-W6 + the
// missing overlay stage-map) — see each method.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// errNoWriteIncumbent is the honest answer a supported write verb gives
// when the Provider has no incumbent write resolver wired, or the resolver
// reports no active incumbent (disconnect/config race) — a clear, actionable
// error rather than a nil-pointer panic, and NOT ErrUnsupportedBySoR
// (Create/Update/Archive are supported verbs; the incumbent is just absent).
func errNoWriteIncumbent() error {
	return fmt.Errorf("overlay: provider has no incumbent write resolver configured")
}

// writeIncumbent resolves the acting workspace's live incumbent for a write.
// It never returns a nil Incumbent without an error: resolveOverlayIncumbent
// legitimately answers (nil, nil) when the active connection is absent or not
// HubSpot, which must fail closed here rather than nil-panic downstream.
//
//nolint:ireturn // resolving the incumbent seam interface IS the purpose — the write path is incumbent-agnostic by design (design.md §4.5)
func (p *Provider) writeIncumbent(ctx context.Context) (Incumbent, error) {
	if p.resolveIncumbent == nil {
		return nil, errNoWriteIncumbent()
	}
	inc, err := p.resolveIncumbent(ctx)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, errNoWriteIncumbent()
	}
	return inc, nil
}

// writeContractTarget returns a fresh pointer to the contract request struct
// for (entityType, forUpdate) — the type StrictDecode validates the write
// payload against.
func writeContractTarget(entityType datasource.EntityType, forUpdate bool) (any, error) {
	switch entityType {
	case datasource.EntityPerson:
		if forUpdate {
			return &crmcontracts.UpdatePersonRequest{}, nil
		}
		return &crmcontracts.CreatePersonRequest{}, nil
	case datasource.EntityOrganization:
		if forUpdate {
			return &crmcontracts.UpdateOrganizationRequest{}, nil
		}
		return &crmcontracts.CreateOrganizationRequest{}, nil
	case datasource.EntityDeal:
		if forUpdate {
			return &crmcontracts.UpdateDealRequest{}, nil
		}
		return &crmcontracts.CreateDealRequest{}, nil
	case datasource.EntityLead:
		if forUpdate {
			return &crmcontracts.UpdateLeadRequest{}, nil
		}
		return &crmcontracts.CreateLeadRequest{}, nil
	case datasource.EntityActivity:
		if forUpdate {
			return &crmcontracts.UpdateActivityRequest{}, nil
		}
		return &crmcontracts.CreateActivityRequest{}, nil
	default:
		return nil, &datasource.UnsupportedEntityError{Type: string(entityType)}
	}
}

// decodeCanonical validates the frozen seam's typed write payload against the
// entity's contract request struct and normalizes it into the canonical field
// bag mapWrite consumes. StrictDecode rejects an unknown/misspelled field and
// a wrong-typed value with an actionable 422 (the same guard the native
// providers apply), rather than letting a typo silently no-op. The validated
// struct is then re-marshalled and decoded with UseNumber, so a large
// amount_minor survives as an exact integer instead of a lossy float64
// round-trip. The result is always a non-nil map (a JSON-null patch decodes
// to an empty struct → {}), so callers can inject fields without a nil-map
// panic.
func decodeCanonical(entityType datasource.EntityType, forUpdate bool, v any) (map[string]any, error) {
	raw, err := datasource.RawFields(v)
	if err != nil {
		return nil, err
	}
	target, err := writeContractTarget(entityType, forUpdate)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := datasource.StrictDecode(raw, target); err != nil {
			return nil, err
		}
		// A contract request struct with an AdditionalProperties catch-all
		// absorbs unknown keys instead of letting StrictDecode's
		// DisallowUnknownFields reject them, so an unknown/misspelled field
		// would route there and silently no-op. Reject a non-empty catch-all
		// so a typo is an actionable error, matching the native 422.
		if err := rejectExtraProperties(target); err != nil {
			return nil, err
		}
	}
	reencoded, err := json.Marshal(target)
	if err != nil {
		return nil, fmt.Errorf("overlay: re-encoding validated write payload: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(reencoded))
	dec.UseNumber()
	fields := map[string]any{}
	if err := dec.Decode(&fields); err != nil {
		return nil, fmt.Errorf("overlay: decoding write fields: %w", err)
	}
	return fields, nil
}

// rejectExtraProperties reports the unknown keys a contract request struct's
// AdditionalProperties catch-all absorbed (oapi-codegen routes non-schema keys
// there). An empty or absent catch-all is fine; a non-empty one is a
// caller-invalid write (a misspelled or unsupported field) surfaced as a
// FieldDecodeError so the transport answers 422, not a silent no-op.
func rejectExtraProperties(target any) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil
	}
	f := v.Elem().FieldByName("AdditionalProperties")
	if !f.IsValid() || f.Kind() != reflect.Map || f.Len() == 0 {
		return nil
	}
	keys := make([]string, 0, f.Len())
	for _, k := range f.MapKeys() {
		keys = append(keys, k.String())
	}
	sort.Strings(keys)
	return &datasource.FieldDecodeError{Cause: fmt.Errorf("unknown field(s): %v", keys)}
}

// Create writes a new record to the incumbent (incumbent-first, AC-OV-4)
// and mirrors the incumbent's returned state. The object RBAC gate runs
// FIRST — the same MCP-bypass closure the read verbs carry, since the MCP
// write path reaches the provider directly. The canonical write is
// projected onto incumbent properties BELOW the seam (the adapter's
// mapWrite, OVA-MAP-W); the Provider stays incumbent-agnostic.
//
// V1 retry-safety limitation: HubSpot's v3 object-create is a bare POST
// with no caller-supplied idempotency key (no hs_unique_creation_key), so a
// caller that retries after a mirror-write failure — the incumbent create
// already committed, the follow-up Ingest did not — can mint a second
// incumbent object. The orphaned first object is not lost (the reconcile
// poller mirrors it on its next sweep), but a retried Create is NOT
// idempotent in V1. Retry-safe create (search-before-create or an
// alternate-key upsert, per S-E19.3/S-E20.3) is a fast-follow; overlay
// write-back today is reached only through the 🟡 confirm-first agent path
// (AC-OV-5), whose approval is human-gated and audited, so an unattended
// retry storm is not the live exposure.
func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	if err := auth.Require(ctx, string(in.EntityType), principal.ActionCreate); err != nil {
		return datasource.EntityRef{}, err
	}
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	fields, err := decodeCanonical(in.EntityType, false, in.Fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	res, err := inc.Create(ctx, string(in.EntityType), fields)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.mirrorWriteResult(ctx, inc, res.Record); err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.openWriteLedger(ctx, res); err != nil {
		return datasource.EntityRef{}, err
	}
	id, err := externalIDToUUID(res.Record.ExternalID)
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
// here and the incumbent's own record clock is the authority (design.md §4.5).
func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	if err := auth.Require(ctx, string(in.Ref.Type), principal.ActionUpdate); err != nil {
		return datasource.EntityRef{}, err
	}
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	externalID := uuidToExternalID(in.Ref.ID)
	// The mirror row supplies both the row-scope visibility gate (Get is
	// visibility-joined) and the drift-check baseline. A row the actor
	// cannot see is ErrNotFound here, exactly as on read.
	row, err := p.ms.Get(ctx, string(in.Ref.Type), externalID)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	fields, err := decodeCanonical(in.Ref.Type, true, in.Patch)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.completeWritePatch(in.Ref.Type, fields, row); err != nil {
		return datasource.EntityRef{}, err
	}
	res, err := inc.Update(ctx, string(in.Ref.Type), externalID, fields, row.UpdatedAtBaseline)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.mirrorWriteResult(ctx, inc, res.Record); err != nil {
		return datasource.EntityRef{}, err
	}
	if err := p.openWriteLedger(ctx, res); err != nil {
		return datasource.EntityRef{}, err
	}
	return in.Ref, nil
}

// openWriteLedger records the echo-suppression ledger entries for a completed
// write (OVA-DDL-6) — one per property the incumbent write actually sent, keyed
// so the webhook receiver recognizes this write's own echo. It runs right after
// mirrorWriteResult and is surfaced the same way: a failure returns to the
// caller (it is a local write against the same pool the mirror ingest just used,
// so a failure here signals the same local-store trouble). No ledger wired (the
// write-verb unit tests) or a read-only-only write (no WrittenProps) is a no-op.
func (p *Provider) openWriteLedger(ctx context.Context, res WriteResult) error {
	if p.ledger == nil || len(res.WrittenProps) == 0 {
		return nil
	}
	return p.ledger.OpenEntries(ctx, res.IncumbentClass, res.Record.ExternalID, res.WrittenProps)
}

// completeWritePatch fills in the cross-field context a partial patch needs to
// project correctly:
//   - Deal money is a PAIR — amount_minor scales to the incumbent's decimal
//     `amount` only under its currency's exponent. A patch that carries one
//     without the other is rejected rather than silently dropping the money
//     change or reinterpreting the stored amount under a new exponent
//     (both present, or neither).
//   - An activity's kind is immutable and never in the patch (UpdateActivity
//     has no kind field), so mapWriteActivity's class selector is carried
//     forward from the mirror row — consistent with the namespaced external_id
//     by construction (the read mapping set both from the source class).
func (p *Provider) completeWritePatch(t datasource.EntityType, fields map[string]any, row Row) error {
	switch t {
	case datasource.EntityDeal:
		_, hasAmount := fields["amount_minor"]
		_, hasCurrency := fields["currency"]
		if hasAmount != hasCurrency {
			return fmt.Errorf("%w: a deal amount change must set amount_minor and currency together (the currency's exponent scales the amount)", apperrors.ErrConflict)
		}
	case datasource.EntityActivity:
		if _, ok := fields["kind"]; !ok {
			if kind, ok := row.Fields["kind"]; ok {
				fields["kind"] = kind
			}
		}
	}
	return nil
}

// archivableTypes are the entity types overlay Archive supports — the same
// set the native provider archives (person/organization/deal). A lead is
// retired through its own lifecycle verbs, and an activity is not archivable
// through this seam; both are refused before any incumbent call, matching the
// frozen contract rather than issuing a destructive write the native path
// would reject.
var archivableTypes = map[datasource.EntityType]bool{
	datasource.EntityPerson:       true,
	datasource.EntityOrganization: true,
	datasource.EntityDeal:         true,
}

// Archive removes a record from the incumbent (its own archive/delete) after
// the stored-baseline drift check, then purges the mirror row so it stops
// being readable rather than lingering visible until the next sync.
func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	if !archivableTypes[r.Type] {
		return datasource.EntityRef{}, &datasource.UnsupportedEntityError{Type: string(r.Type)}
	}
	if err := auth.Require(ctx, string(r.Type), principal.ActionDelete); err != nil {
		return datasource.EntityRef{}, err
	}
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	externalID := uuidToExternalID(r.ID)
	// Row-scope gate + drift baseline: a record the actor cannot see is
	// ErrNotFound, never archived on their behalf.
	row, err := p.ms.Get(ctx, string(r.Type), externalID)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := inc.Archive(ctx, string(r.Type), externalID, row.UpdatedAtBaseline); err != nil {
		return datasource.EntityRef{}, err
	}
	// Purge through the disconnect fence so a teardown racing the archive
	// cannot leave the row readable, matching the sync path.
	if _, err := p.ms.WithFence().PurgeRecord(ctx, Deletion{ObjectClass: string(r.Type), ExternalID: externalID}); err != nil {
		return datasource.EntityRef{}, err
	}
	return r, nil
}

// mirrorWriteResult ingests the incumbent's post-write state into the mirror
// so a follow-up read sees the write without waiting for the sync poller.
// It binds the store to the LIVE incumbent (WithResolver) so Ingest's owner
// re-validation resolves against the real adapter — not the read-path
// placeholder that always fails — and engages the disconnect fence
// (WithFence) so a write landing after a Disconnect cannot repopulate the
// purged mirror (it aborts with ErrConnectionGone). Ingest's staleness guard
// admits the row (the write bumped the incumbent's baseline past the
// mirror's), and the mirror stays non-authoritative (T2) — the incumbent
// remains the system of record.
func (p *Provider) mirrorWriteResult(ctx context.Context, inc Incumbent, rec Record) error {
	if p.ms == nil {
		return errNoMirrorStore()
	}
	return p.ms.WithResolver(inc).WithFence().Ingest(ctx, rec)
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
