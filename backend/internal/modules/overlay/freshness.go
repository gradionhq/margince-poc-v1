// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

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
	inc   Incumbent
	ms    *MirrorStore
	meter *Meter
	// toIncumbentClass translates a CANONICAL entity-type name (e.g.
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
	// registry (e.g. hubspot.IncumbentClassFor) when it constructs a
	// FreshnessReader over a real Adapter.
	toIncumbentClass func(canonical string) (incumbentClass string, ok bool)
}

// NewFreshnessReader constructs a FreshnessReader over inc (the live
// incumbent read), ms (the mirror fallback), meter (the OVB budget
// gate), and toIncumbentClass (the canonical->incumbent class
// translator Read needs before every inc.Get — see the type doc). meter
// and toIncumbentClass may both be nil only in tests that don't exercise
// the live path at all: Read treats a nil translator as "this incumbent
// class is unknown for every canonical type" (an honest degrade, never
// a silent canonical-name pass-through).
func NewFreshnessReader(inc Incumbent, ms *MirrorStore, meter *Meter, toIncumbentClass func(string) (string, bool)) *FreshnessReader {
	return &FreshnessReader{inc: inc, ms: ms, meter: meter, toIncumbentClass: toIncumbentClass}
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

	if f.inc == nil {
		// No live incumbent wired at all — the same honest fallback the
		// shed band takes, but this is a wiring gap, not a budget
		// decision, so it does not emit mirror.budget_degraded either.
		return f.mirrorFreshness(ctx, ref)
	}

	incumbentClass, ok := f.incumbentClassFor(string(ref.Type))
	if !ok {
		// No declared incumbent-class mapping for this canonical type —
		// the same honest wiring-gap fallback f.inc == nil takes above.
		// Passing ref.Type straight to inc.Get here would be exactly the
		// silent canonical/incumbent confusion this translation step
		// exists to prevent.
		slog.WarnContext(ctx, "overlay: no incumbent class mapping for canonical type, degrading to the mirror",
			"entity_type", string(ref.Type))
		return f.mirrorFreshness(ctx, ref)
	}

	rec, err := f.inc.Get(ctx, incumbentClass, uuidToExternalID(ref.ID))
	if err != nil {
		slog.WarnContext(ctx, "overlay: live force-fresh read failed, degrading to the mirror",
			"entity_type", string(ref.Type), "incumbent_class", incumbentClass, "entity_id", ref.ID.String(), "err", err)
		info, mErr := f.mirrorFreshness(ctx, ref)
		if mErr != nil {
			return datasource.FreshnessInfo{}, fmt.Errorf("overlay: live freshness read failed (%w) and the mirror fallback also failed: %v", err, mErr)
		}
		return info, nil
	}

	if f.meter != nil {
		if cErr := f.meter.Consume(ctx, LaneForceFresh, 1); cErr != nil {
			return datasource.FreshnessInfo{}, cErr
		}
	}
	return datasource.FreshnessInfo{LastSyncedAt: rec.ModifiedAt, Authoritative: true}, nil
}

// incumbentClassFor answers f's translator for canonical, treating a nil
// translator (no constructor argument given) the same as a translator
// that declares no mapping for anything: ok=false, never a fabricated
// pass-through of canonical itself.
func (f *FreshnessReader) incumbentClassFor(canonical string) (string, bool) {
	if f.toIncumbentClass == nil {
		return "", false
	}
	return f.toIncumbentClass(canonical)
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
		if err := storekit.Emit(ctx, tx, logID, "mirror.budget_degraded", string(ref.Type), ref.ID,
			map[string]any{"band": BandShed}); err != nil {
			return fmt.Errorf("overlay: emitting mirror.budget_degraded: %w", err)
		}
		return nil
	})
}
