// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// sorModeCacheTTL bounds how long a resolved workspace.x_sor_mode answer
// is reused before Dispatcher re-checks the workspace row. A workspace
// flip (overlay.Service.Connect/Disconnect) is a rare, human-initiated
// admin action — not hot-path traffic — so a few seconds of dispatch
// lag after a flip is an acceptable, honestly-bounded cost, the same
// trade this build already makes for passport revocation ("every call
// re-authenticates... revocation binds mid-session", not instantly).
// Five seconds keeps the DB hit off every single Read/Search call
// (the reason to cache at all). The TTL is the backstop, not the only
// propagation path: the process that composes BOTH sides (server.go
// wires overlay.Service's mode-flip observer to Invalidate) drops the
// entry the moment a flip commits, so the admin who just connected
// reads the mirror on their very next request; the TTL covers every
// other process (a worker's own dispatcher, a second api replica)
// where no such local hook can exist.
const sorModeCacheTTL = 5 * time.Second

// sorModeCacheEntry caches one workspace's resolved x_sor_mode answer
// (overlay==true means workspace.x_sor_mode='overlay') until expiresAt.
type sorModeCacheEntry struct {
	overlay   bool
	expiresAt time.Time
}

// Dispatcher is the per-workspace System-of-Record router (design.md
// §4.2/§4.6): every datasource verb is forwarded to native (this
// process's own SoR modules) or to overlayProvider (the read-through
// mirror), chosen per call by the calling context's workspace.x_sor_mode
// — never guessed, never sticky across workspaces. It is itself a
// datasource.SystemOfRecordProvider, so it drops into every existing
// seam-injection point (registry.go, workflows.go, server.go's
// contractAPI) with no caller-side change beyond the constructor.
type Dispatcher struct {
	native  *Provider
	overlay *overlay.Provider
	pool    *pgxpool.Pool
	now     func() time.Time

	mu    sync.Mutex
	cache map[ids.UUID]sorModeCacheEntry
}

// NewDispatcher wires native and overlayProvider behind the per-workspace
// mode lookup, resolved against pool's workspace table.
func NewDispatcher(native *Provider, overlayProvider *overlay.Provider, pool *pgxpool.Pool) *Dispatcher {
	return newDispatcherWithClock(native, overlayProvider, pool, time.Now)
}

// newDispatcherWithClock is NewDispatcher with an injectable clock (T11:
// no real-clock reliance in a TTL-cache test) — used only by this
// package's own tests to exercise the TTL boundary without a
// time.Sleep.
func newDispatcherWithClock(native *Provider, overlayProvider *overlay.Provider, pool *pgxpool.Pool, now func() time.Time) *Dispatcher {
	return &Dispatcher{
		native: native, overlay: overlayProvider, pool: pool, now: now,
		cache: make(map[ids.UUID]sorModeCacheEntry),
	}
}

var _ datasource.SystemOfRecordProvider = (*Dispatcher)(nil)

// isOverlay answers whether ctx's workspace should dispatch to the
// overlay provider — returned as a bool rather than the provider itself
// (ireturn: a concrete-typed field selection at each of the 13 call
// sites below, never a lone interface handed back from a helper). A
// context with no workspace bound at all (a background/system context,
// e.g. a workflow starter running outside any one tenant's request) has
// no per-workspace mode to look up either — it honestly answers false
// (native), the mode every workspace starts in.
func (d *Dispatcher) isOverlay(ctx context.Context) (bool, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return false, nil
	}
	return d.overlayModeFor(ctx, wsID)
}

// Invalidate drops one workspace's cached x_sor_mode answer — called by
// the composition layer when overlay.Service commits a mode flip, so
// this process's next dispatch re-reads the row instead of serving the
// old mode for the remainder of the TTL.
func (d *Dispatcher) Invalidate(wsID ids.UUID) {
	d.mu.Lock()
	delete(d.cache, wsID)
	d.mu.Unlock()
}

// overlayModeFor answers whether wsID's workspace.x_sor_mode is
// 'overlay', served from the TTL cache when fresh and re-queried
// otherwise.

func (d *Dispatcher) overlayModeFor(ctx context.Context, wsID ids.UUID) (bool, error) {
	now := d.now()
	d.mu.Lock()
	entry, ok := d.cache[wsID]
	d.mu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.overlay, nil
	}

	isOverlay, err := d.queryOverlayMode(ctx, wsID)
	if err != nil {
		return false, err
	}
	d.mu.Lock()
	d.cache[wsID] = sorModeCacheEntry{overlay: isOverlay, expiresAt: now.Add(sorModeCacheTTL)}
	d.mu.Unlock()
	return isOverlay, nil
}

// queryOverlayMode reads workspace.x_sor_mode straight from the
// workspace row. workspace is the one non-tenant table (identity's own
// ResolveWorkspace doc comment), so this rides WithInfraTx rather than
// the RLS-bound WithWorkspaceTx — there is no workspace_id column on
// workspace itself to scope by.
func (d *Dispatcher) queryOverlayMode(ctx context.Context, wsID ids.UUID) (bool, error) {
	var mode string
	err := database.WithInfraTx(ctx, d.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT x_sor_mode FROM workspace WHERE id = $1`, wsID).Scan(&mode)
	})
	if err != nil {
		return false, fmt.Errorf("compose: resolving workspace sor_mode for dispatch: %w", err)
	}
	return mode == "overlay", nil
}

// Read dispatches to the overlay mirror or the native SoR modules per
// ctx's workspace.x_sor_mode.
func (d *Dispatcher) Read(ctx context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.Record{}, err
	}
	if ov {
		return d.overlay.Read(ctx, ref)
	}
	return d.native.Read(ctx, ref)
}

// Search dispatches to the overlay mirror or the native SoR modules per
// ctx's workspace.x_sor_mode.
func (d *Dispatcher) Search(ctx context.Context, q datasource.SearchQuery) (datasource.SearchResult, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.SearchResult{}, err
	}
	if ov {
		return d.overlay.Search(ctx, q)
	}
	return d.native.Search(ctx, q)
}

// ListObjects dispatches to the overlay mirror or the native SoR
// modules per ctx's workspace.x_sor_mode.
func (d *Dispatcher) ListObjects(ctx context.Context) ([]datasource.ObjectDef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return nil, err
	}
	if ov {
		return d.overlay.ListObjects(ctx)
	}
	return d.native.ListObjects(ctx)
}

// ListFields dispatches to the overlay mirror or the native SoR modules
// per ctx's workspace.x_sor_mode.
func (d *Dispatcher) ListFields(ctx context.Context, entity datasource.EntityType) ([]datasource.FieldDef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return nil, err
	}
	if ov {
		return d.overlay.ListFields(ctx, entity)
	}
	return d.native.ListFields(ctx, entity)
}

// RunReport dispatches to the overlay mirror or the native SoR modules
// per ctx's workspace.x_sor_mode; overlay has no incumbent analogue and
// always answers apperrors.ErrUnsupportedBySoR (design.md §4.5).
func (d *Dispatcher) RunReport(ctx context.Context, plan datasource.ReportPlan) (datasource.ReportResult, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.ReportResult{}, err
	}
	if ov {
		return d.overlay.RunReport(ctx, plan)
	}
	return d.native.RunReport(ctx, plan)
}

// StageSemantic dispatches to the overlay mirror or the native SoR
// modules per ctx's workspace.x_sor_mode.
func (d *Dispatcher) StageSemantic(ctx context.Context, stageID ids.UUID) (string, ids.UUID, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return "", ids.UUID{}, err
	}
	if ov {
		return d.overlay.StageSemantic(ctx, stageID)
	}
	return d.native.StageSemantic(ctx, stageID)
}

// Create dispatches to the overlay mirror or the native SoR modules per
// ctx's workspace.x_sor_mode; overlay has no write-back path yet and
// always answers apperrors.ErrUnsupportedBySoR until branch 2 lands it.
func (d *Dispatcher) Create(ctx context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if ov {
		return d.overlay.Create(ctx, in)
	}
	return d.native.Create(ctx, in)
}

// Update dispatches to the overlay mirror or the native SoR modules per
// ctx's workspace.x_sor_mode; see Create's doc on overlay's write gap.
func (d *Dispatcher) Update(ctx context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if ov {
		return d.overlay.Update(ctx, in)
	}
	return d.native.Update(ctx, in)
}

// AdvanceDeal dispatches to the overlay mirror or the native SoR
// modules per ctx's workspace.x_sor_mode; see Create's doc on overlay's
// write gap.
func (d *Dispatcher) AdvanceDeal(ctx context.Context, in datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if ov {
		return d.overlay.AdvanceDeal(ctx, in)
	}
	return d.native.AdvanceDeal(ctx, in)
}

// Archive dispatches to the overlay mirror or the native SoR modules
// per ctx's workspace.x_sor_mode; see Create's doc on overlay's write
// gap.
func (d *Dispatcher) Archive(ctx context.Context, ref datasource.EntityRef) (datasource.EntityRef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if ov {
		return d.overlay.Archive(ctx, ref)
	}
	return d.native.Archive(ctx, ref)
}

// Merge dispatches to the overlay mirror or the native SoR modules per
// ctx's workspace.x_sor_mode; see Create's doc on overlay's write gap.
func (d *Dispatcher) Merge(ctx context.Context, in datasource.MergeInput) (datasource.EntityRef, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if ov {
		return d.overlay.Merge(ctx, in)
	}
	return d.native.Merge(ctx, in)
}

// PromoteLead dispatches to the overlay mirror or the native SoR
// modules per ctx's workspace.x_sor_mode; see Create's doc on overlay's
// write gap.
func (d *Dispatcher) PromoteLead(ctx context.Context, id ids.UUID, trigger string, evidenceNote *string) (datasource.EntityRef, bool, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.EntityRef{}, false, err
	}
	if ov {
		return d.overlay.PromoteLead(ctx, id, trigger, evidenceNote)
	}
	return d.native.PromoteLead(ctx, id, trigger, evidenceNote)
}

// Freshness dispatches to the overlay mirror or the native SoR modules
// per ctx's workspace.x_sor_mode; overlay's own Freshness is a metered
// force-fresh read, native's is trivially authoritative.
func (d *Dispatcher) Freshness(ctx context.Context, ref datasource.EntityRef) (datasource.FreshnessInfo, error) {
	ov, err := d.isOverlay(ctx)
	if err != nil {
		return datasource.FreshnessInfo{}, err
	}
	if ov {
		return d.overlay.Freshness(ctx, ref)
	}
	return d.native.Freshness(ctx, ref)
}

// ContractSearchResults maps one datasource.SearchResult onto the
// contract wire shape (crmcontracts.SearchResult) — the ONE place a
// datasource record crosses into the typed contract surface for search.
// The T2 trust-tier tag (design.md §4.6, AC's "overlay-served results
// carry TrustTier=external") is stamped HERE, from the record's own
// Freshness.Authoritative flag Dispatcher's chosen provider already set
// honestly (overlay.Provider always answers false; the native Provider
// always answers true) — never guessed from the caller's workspace mode
// a second time. A native/authoritative record is left untagged
// (TrustTier nil), matching search/handlers.go's own FTS-path
// convention of only ever emitting the "authoritative" tier for
// same-store hits; this function's only difference is it also emits
// "external" when the record didn't come from the native store.
func ContractSearchResults(res datasource.SearchResult) []crmcontracts.SearchResult {
	out := make([]crmcontracts.SearchResult, 0, len(res.Records))
	for _, rec := range res.Records {
		r := crmcontracts.SearchResult{
			Id:   openapi_types.UUID(rec.Ref.ID),
			Type: crmcontracts.SearchResultType(rec.Ref.Type),
		}
		if !rec.Freshness.Authoritative {
			tt := crmcontracts.SearchResultTrustTierExternal
			r.TrustTier = &tt
		}
		out = append(out, r)
	}
	return out
}
