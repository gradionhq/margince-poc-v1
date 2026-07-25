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
	"log/slog"
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
// already committed, the follow-up ingest did not — can mint a second
// incumbent object. The orphaned first object is not lost (the reconcile
// poller mirrors it on its next sweep), but a retried Create is NOT
// idempotent in V1. Retry-safe create (search-before-create or an
// alternate-key upsert, per S-E19.3/S-E20.3) is the prerequisite for ever
// declaring this verb supported.
func (p *Provider) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	if err := requireSupportedWrite(WriteCreate, in.EntityType); err != nil {
		return datasource.EntityRef{}, err
	}
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
	// After the mirror write, never before — see openWriteLedger on why the
	// two orderings are not symmetric.
	p.openWriteLedger(ctx, res)
	id, err := externalIDToUUID(res.Record.ExternalID)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	return datasource.EntityRef{Type: in.EntityType, ID: id}, nil
}

// Update applies a patch incumbent-first after the stored-baseline drift
// check (AC-OV-4): the mirror row supplies the baseline captured at
// mirror-read, and the adapter refuses with ErrVersionSkew (surfaced as a
// 409 version_skew by the transport) if the incumbent moved since — never a blind
// overwrite. On success the incumbent's returned state is re-mirrored.
//
// Overlay's optimistic concurrency IS this incumbent stored-baseline drift
// check, not a mirror row version: an overlay read carries no integer
// Version (recordFromRow), so in.IfVersion has nothing to compare against
// here and the incumbent's own record clock is the authority (design.md §4.5).
func (p *Provider) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	if err := requireSupportedWrite(WriteUpdate, in.Ref.Type); err != nil {
		return datasource.EntityRef{}, err
	}
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
	// The before image is read off the SAME mirror row that supplied the
	// drift baseline, so the audit trail's before/after pair describes the
	// exact state the drift check licensed this write against.
	before := beforeImage(row, fields)
	res, err := inc.Update(ctx, string(in.Ref.Type), externalID, fields, row.UpdatedAtBaseline)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	// The incumbent has committed. Everything past this line is the local
	// half, and it runs on a context that outlives the caller (see
	// afterIncumbentCommit).
	localCtx, cancel := afterIncumbentCommit(ctx)
	defer cancel()
	if err := p.commitUpdateWriteBack(localCtx, inc, res.Record, in.Ref, before, fields); err != nil {
		return datasource.EntityRef{}, writePathError(err)
	}
	p.openWriteLedger(localCtx, res)
	return in.Ref, nil
}

// openWriteLedger records the echo-suppression ledger entries for a completed
// write (OVA-DDL-6) — one per property the incumbent write actually sent, keyed
// so the webhook receiver recognizes this write's own echo.
//
// It is FAIL-OPEN: the incumbent write has already committed, so a ledger
// failure must never propagate as a write failure — a caller told the write
// failed could retry and mint a duplicate incumbent record. A missed entry only
// costs a redundant re-fetch when the echo arrives (idempotent, poller-healed),
// so the failure is logged and swallowed here. No ledger wired (the write-verb
// unit tests) or a read-only-fields write (no WrittenProps) is a no-op.
//
// It runs AFTER the mirror write commits, never before. Opening earlier would
// narrow the window in which our own echo outruns its ledger entry — but it
// makes the far worse failure possible: entries committed, mirror write
// failed. The echo webhook is then classified as ours and DROPPED, so the one
// signal that would have healed the mirror is suppressed, the mirror serves
// the pre-write value behind a pre-write baseline, and every retry answers
// 409 version_skew against our own write until the next poller sweep. The
// costs are not symmetric: opening late costs one redundant re-fetch, opening
// early costs a frozen record. So the ledger follows the commit.
func (p *Provider) openWriteLedger(ctx context.Context, res WriteResult) {
	if p.ledger == nil || len(res.WrittenProps) == 0 {
		return
	}
	if err := p.ledger.OpenEntries(ctx, res.IncumbentClass, res.Record.ExternalID, res.WrittenProps); err != nil {
		log := p.log
		if log == nil {
			log = slog.Default()
		}
		log.WarnContext(ctx, "overlay: opening echo-suppression ledger entries failed; the write's echo may cost a redundant re-fetch",
			"class", res.IncumbentClass, "external_id", res.Record.ExternalID, "err", err)
	}
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

// WriteVerb names the three record-write verbs the SoR seam exposes.
type WriteVerb string

// The three WriteVerb values, one per Provider write method.
const (
	WriteCreate  WriteVerb = "create"
	WriteUpdate  WriteVerb = "update"
	WriteArchive WriteVerb = "archive"
)

// requireSupportedWrite refuses a verb SupportsWrite does not declare for
// et — the ONE enforcement point for that declaration, applied inside each
// write verb rather than at any one transport.
//
// The transports are not enough on their own: the composition layer's HTTP
// write guard (compose/overlaywrite.go) covers REST, but the agent tool
// surface and the automation engine reach these verbs through the datasource
// seam directly, with no guard in between. A capability declared unsupported
// that a seam caller can still execute is not declared at all, so the refusal
// belongs where every caller passes.
func requireSupportedWrite(verb WriteVerb, et datasource.EntityType) error {
	if SupportsWrite(verb, et) {
		return nil
	}
	if _, err := writeContractTarget(et, verb == WriteUpdate); err != nil {
		// An entity the mirror does not carry at all — the honest answer is
		// "no such entity here", not "this verb is unsupported".
		return &datasource.UnsupportedEntityError{Type: string(et)}
	}
	return apperrors.ErrUnsupportedBySoR
}

// SupportsWrite reports whether the overlay provider can serve verb for et.
// It is the provider's own capability, enforced by requireSupportedWrite
// inside every write verb and read by the composition layer's write guard and
// write shadows, so no transport can disagree with it about what the mirror
// can do — a disagreement would let an unsupported write fall through to a
// native handler and commit to the empty native table.
//
// Create is unsupported for every type: the write mapping declares owner_id
// read-only, so a created incumbent record is unowned, and the NULL-OWNER RULE
// (visibility.go) writes no visibility row for an unowned record — the create
// would succeed at the incumbent and then be invisible to everyone, including
// its author. Owner-on-create is the prerequisite, and it is a mapping
// decision, not a transport one.
//
// Two obligations come with ever flipping WriteCreate to true, both already
// carried by the verbs that ARE supported: retry-safe create (Create's own
// doc) and the write-back audit shape (writeaudit.go's commitUpdateWriteBack,
// which Create does not call because it cannot run).
func SupportsWrite(verb WriteVerb, et datasource.EntityType) bool {
	switch verb {
	case WriteCreate:
		return false
	case WriteUpdate:
		_, err := writeContractTarget(et, true)
		return err == nil
	case WriteArchive:
		return archivableTypes[et]
	default:
		return false
	}
}

// Archive removes a record from the incumbent (its own archive/delete) after
// the stored-baseline drift check, then purges the mirror row so it stops
// being readable rather than lingering visible until the next sync.
func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	if err := requireSupportedWrite(WriteArchive, r.Type); err != nil {
		return datasource.EntityRef{}, err
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
	// The incumbent has archived the record; the local half runs detached
	// from the caller (afterIncumbentCommit) because a closed tab must not
	// leave a record that no longer exists at the incumbent still listed and
	// still readable here until the next full reconcile sweep.
	//
	// The purge goes through the disconnect fence so a teardown racing the
	// archive cannot leave the row readable, matching the sync path — and the
	// archive's audit_log and event_outbox rows commit in that same
	// transaction, so a record removed from the customer's own CRM can never
	// be missing from the ledger that answers who removed it.
	localCtx, cancel := afterIncumbentCommit(ctx)
	defer cancel()
	if err := p.commitArchiveWriteBack(localCtx, Deletion{ObjectClass: string(r.Type), ExternalID: externalID}, r); err != nil {
		return datasource.EntityRef{}, writePathError(err)
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
