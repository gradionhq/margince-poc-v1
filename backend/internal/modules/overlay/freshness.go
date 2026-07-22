// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// mirrorBudgetDegradedPayload builds the mirror.budget_degraded wire
// payload — the subject travels separately (the read's own entity ref,
// passed to storekit.EmitEventForEntity), since this event's entity is
// dynamic (whichever record class the degraded read was about).
func mirrorBudgetDegradedPayload(band string) crmcontracts.WebhookPayloadMirrorBudgetDegraded {
	return crmcontracts.WebhookPayloadMirrorBudgetDegraded{Band: band}
}

// FreshnessReader is the real force-fresh read-through (design.md §4.5
// S2): a Freshness call bypasses the mirror and re-reads the incumbent
// live, metered against the OVB budget meter (budgetmeter.go), degrading
// to mirror-with-staleness-warning — and emitting mirror.budget_degraded
// — once the meter reports the shed band. This is NOT deferrable: branch
// 1 ships live force-fresh reads and an always-on poller against the
// SAME shared HubSpot quota (the poller lane is metered too), so the
// degrade must ship with the live read or branch 1 can starve the
// customer's own quota.
type FreshnessReader struct {
	// resolveIncumbent builds the LIVE incumbent adapter for the request's
	// workspace, at read time. A force-fresh read needs THIS workspace's own
	// vaulted region+token, which the process-wide Dispatcher cannot bind at
	// construction — so the read path resolves it per request (compose wires
	// a resolver that reads the workspace's incumbent_connection + vault).
	// nil (or a resolver returning nil/err) is an honest degrade to the
	// mirror, never a silent authority claim: a role with no vault, or a
	// workspace with no active connection, simply has no live read to do.
	resolveIncumbent func(ctx context.Context) (Incumbent, error)
	ms               *MirrorStore
	meter            *Meter
	// toIncumbentClasses translates a CANONICAL entity-type name (e.g.
	// "person", the datasource.EntityRef.Type this reader is called
	// with) to the INCUMBENT's own object class (e.g. "contacts") — the
	// Incumbent seam is asymmetric by design: Backfill/Modified/Get take
	// an incumbent class as input, while Record.ObjectClass and the
	// mirror's own object_class column carry the canonical name as
	// output. Passing ref.Type straight into inc.Get without this
	// translation compiles and passes against any incumbent that keys
	// by whatever string it's given (a fake), but errors against a real
	// adapter (hubspot.Adapter.Get rejects any class it has no declared
	// mapping for). Compose wires this from the incumbent's own mapping
	// registry (e.g. hubspot.IncumbentClassesFor) when it constructs a
	// FreshnessReader over a real Adapter.
	//
	// It is PLURAL because a canonical type can map to more than one
	// incumbent class ("activity" ← the five v3 engagement classes,
	// OVA-MAP-1). A single-record force-fresh needs exactly ONE class, so
	// Read degrades a multi-source type to the mirror (the mirror row does
	// not record which engagement class it came from — a tracked follow-up;
	// no force-fresh caller exists yet).
	toIncumbentClasses func(canonical string) (incumbentClasses []string, ok bool)
}

// NewFreshnessReader constructs a FreshnessReader over resolveIncumbent
// (the per-request live-incumbent resolver — see the field doc), ms (the
// mirror fallback), meter (the OVB budget gate), and toIncumbentClasses (the
// canonical->incumbent class translator Read needs before every inc.Get).
// resolveIncumbent, meter, and toIncumbentClasses may each be nil in tests or
// roles that don't exercise the live path: a nil resolver degrades every
// read to the mirror, and a nil translator treats every canonical type as
// having no incumbent class (both honest degrades, never a silent
// authority claim or canonical-name pass-through).
func NewFreshnessReader(resolveIncumbent func(context.Context) (Incumbent, error), ms *MirrorStore, meter *Meter, toIncumbentClasses func(string) ([]string, bool)) *FreshnessReader {
	return &FreshnessReader{resolveIncumbent: resolveIncumbent, ms: ms, meter: meter, toIncumbentClasses: toIncumbentClasses}
}

// SetIncumbentResolver installs the per-request live-incumbent resolver
// after construction — the boot-time injection point compose uses once the
// vault the resolver needs is wired (WithKeyvault), after this reader was
// built with a nil resolver. Called at server assembly, before any request
// is served, so it never races a Read.
func (f *FreshnessReader) SetIncumbentResolver(resolveIncumbent func(context.Context) (Incumbent, error)) {
	f.resolveIncumbent = resolveIncumbent
}

// Read answers ref's freshness. Under the meter's shed band it degrades
// to the mirror row (0 quota spent) and emits mirror.budget_degraded
// (OVA-EVT-3) so the shed is an observable, auditable fact rather than
// a silent quality drop. Otherwise it does a live incumbent read,
// spending 1 unit on the force_fresh lane and answering
// Authoritative:true — the one honest way this port can ever set that
// field true for an overlay-mode workspace (every other overlay read
// serves the mirror, DS-AC-7).
//
// A live-read error (the incumbent unreachable, rate-limited outside
// this meter's own accounting, etc.) degrades to the mirror the same
// way a shed band does, but WITHOUT emitting mirror.budget_degraded —
// that event names a budget decision, not an incumbent outage, and
// conflating the two would misattribute the cause to anyone reading the
// event stream. The error itself is never swallowed: it is logged at
// warn level (T2) before the honest fallback answer goes out, and if
// the fallback mirror read ALSO fails, both errors surface together.
func (f *FreshnessReader) Read(ctx context.Context, ref datasource.EntityRef) (datasource.FreshnessInfo, error) {
	if f.ms == nil {
		return datasource.FreshnessInfo{}, fmt.Errorf("overlay: freshness reader has no mirror store configured")
	}

	if f.meter != nil && f.meter.Band(ctx) == BandShed {
		return f.degradeForShed(ctx, ref)
	}

	// Read the mirror row ONCE, up front — before resolving the incumbent
	// (which unseals a credential from the vault) and before any live call.
	// This is BOTH the fail-closed visibility gate (MirrorStore.Get carries
	// the mirror_visibility deny-join, so an invisible or guessed id is
	// refused here — ErrNotFound, existence-hiding — and never triggers a
	// vault unseal or a live HTTP call) AND the source for every mirror
	// fallback below, so the degrade paths reuse this row rather than
	// re-reading it. A force-fresh answer must never reveal a record the
	// caller cannot already see.
	row, err := f.ms.Get(ctx, string(ref.Type), uuidToExternalID(ref.ID))
	if err != nil {
		return datasource.FreshnessInfo{}, err
	}
	mirror := datasource.FreshnessInfo{LastSyncedAt: row.LastSyncedAt, Authoritative: false}

	incumbentClass, ok := f.incumbentClassFor(string(ref.Type))
	if !ok {
		// No declared incumbent-class mapping for this canonical type — an
		// honest mirror fallback. Passing ref.Type straight to inc.Get would
		// be the silent canonical/incumbent confusion this step prevents.
		slog.WarnContext(ctx, "overlay: no incumbent class mapping for canonical type, degrading to the mirror",
			"entity_type", string(ref.Type))
		return mirror, nil
	}

	// Resolve THIS workspace's live incumbent (nil resolver ⇒ no live read
	// wired). A resolve error is an honest wiring/connection gap, not a
	// budget decision, so it degrades to the mirror WITHOUT emitting
	// mirror.budget_degraded — logged, never swallowed. A nil incumbent (no
	// active connection, no vault, or a non-HubSpot incumbent) is the same
	// honest fallback.
	var inc Incumbent
	if f.resolveIncumbent != nil {
		resolved, rErr := f.resolveIncumbent(ctx)
		if rErr != nil {
			slog.WarnContext(ctx, "overlay: resolving the live incumbent for force-fresh failed, degrading to the mirror",
				"entity_type", string(ref.Type), "err", rErr)
			return mirror, nil
		}
		inc = resolved
	}
	if inc == nil {
		return mirror, nil
	}

	// Reserve the force-fresh unit BEFORE the live call. Reserve is an
	// atomic band-check-and-record, so concurrent force-fresh reads cannot
	// all observe a non-shed band and collectively overshoot the budget;
	// and because the unit is reserved up front, a live read that later
	// FAILS still counts (its HTTP call spent quota either way). A shed
	// reservation (band flipped since the fast-path check above, or a
	// concurrent reserve pushed over) degrades and emits the budget event.
	if f.meter != nil {
		allowed, rErr := f.meter.Reserve(ctx, LaneForceFresh, 1)
		if rErr != nil {
			return datasource.FreshnessInfo{}, rErr
		}
		if !allowed {
			return f.degradeForShed(ctx, ref)
		}
	}

	rec, err := inc.Get(ctx, incumbentClass, uuidToExternalID(ref.ID))
	if err != nil {
		// The force-fresh unit is already reserved and stays spent — the
		// failed HTTP call consumed quota all the same. Degrade to the
		// already-read mirror row without a budget event (this is an
		// incumbent outage, not a budget decision).
		slog.WarnContext(ctx, "overlay: live force-fresh read failed, degrading to the mirror",
			"entity_type", string(ref.Type), "incumbent_class", incumbentClass, "entity_id", ref.ID.String(), "err", err)
		return mirror, nil
	}
	return datasource.FreshnessInfo{LastSyncedAt: rec.ModifiedAt, Authoritative: true}, nil
}

// incumbentClassFor answers the SINGLE incumbent class a force-fresh of
// canonical must fetch from. A nil translator (no constructor argument
// given) declares no mapping for anything (ok=false, never a fabricated
// pass-through of canonical itself). A canonical type backed by MORE than
// one incumbent class ("activity" ← the five engagement classes) is
// under-determined for a single-record fetch — the mirror row does not
// record which class it came from — so it also answers ok=false and Read
// degrades to the mirror rather than guessing a class.
func (f *FreshnessReader) incumbentClassFor(canonical string) (string, bool) {
	if f.toIncumbentClasses == nil {
		return "", false
	}
	classes, ok := f.toIncumbentClasses(canonical)
	if !ok || len(classes) != 1 {
		return "", false
	}
	return classes[0], true
}

// mirrorFreshness answers ref's freshness from the mirror row alone,
// honestly labelled Authoritative:false (T2 — the mirror never claims
// the authority only a live incumbent read carries).
func (f *FreshnessReader) mirrorFreshness(ctx context.Context, ref datasource.EntityRef) (datasource.FreshnessInfo, error) {
	row, err := f.ms.Get(ctx, string(ref.Type), uuidToExternalID(ref.ID))
	if err != nil {
		return datasource.FreshnessInfo{}, err
	}
	return datasource.FreshnessInfo{LastSyncedAt: row.LastSyncedAt, Authoritative: false}, nil
}

// degradeForShed answers the mirror fallback AND records the shed
// decision as a mirror.budget_degraded event — the two must not
// diverge (an emitted event with no matching degrade, or a degrade with
// no event, would each be a different kind of dishonest), so this is
// the ONE call site that does both.
func (f *FreshnessReader) degradeForShed(ctx context.Context, ref datasource.EntityRef) (datasource.FreshnessInfo, error) {
	info, err := f.mirrorFreshness(ctx, ref)
	if err != nil {
		return datasource.FreshnessInfo{}, err
	}
	if err := f.emitBudgetDegraded(ctx, ref); err != nil {
		return datasource.FreshnessInfo{}, err
	}
	return info, nil
}

// emitBudgetDegraded stages mirror.budget_degraded (events catalog,
// OVA-EVT-3) in its own short transaction over the mirror store's pool.
// A budget-shed decision mutates no domain record — it is a genuine,
// non-entity SYSTEM event (storekit's own distinction: LogSystem is "the
// ledger for a ... non-entity operational event that mutates no record
// and so has no place in audit_log"), so this pairs LogSystem (the
// system_log ledger row) with Emit, exactly the shape LogSystem's own
// doc comment anticipates: "it returns the row id so an entity-less
// pipeline event can carry it as trace.audit_log_id." mirror.
// budget_degraded is NOT itself entity-less (events.Validate rejects an
// incomplete EntityRef for it — it isn't in the pipelineEventTypes
// exemption list), so Emit still carries ref's own entity type/id: the
// event names the record the degraded read was ABOUT, while the ledger
// row it traces to is a system_log row rather than an audit_log row,
// because no domain row was mutated to audit.
func (f *FreshnessReader) emitBudgetDegraded(ctx context.Context, ref datasource.EntityRef) error {
	return database.WithWorkspaceTx(ctx, f.ms.pool, func(tx pgx.Tx) error {
		logID, err := storekit.LogSystem(ctx, tx, "mirror.budget_degraded", map[string]any{
			"entity_type": string(ref.Type),
			"entity_id":   ref.ID.String(),
			"band":        BandShed,
		})
		if err != nil {
			return fmt.Errorf("overlay: logging the budget-degrade system event: %w", err)
		}
		if err := storekit.EmitEventForEntity(ctx, tx, logID, string(ref.Type), ref.ID,
			mirrorBudgetDegradedPayload(BandShed)); err != nil {
			return fmt.Errorf("overlay: emitting mirror.budget_degraded: %w", err)
		}
		return nil
	})
}
