// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay connection lifecycle, assembled: overlay.Service over the
// pool and the secret vault, wired into overlay.Handlers — composed here
// so overlay never imports keyvault's concrete provider selection (the
// same posture capture.go documents for NewCaptureRegistry). This also
// wires the sync-status/budget/reconcile surface: the shared OVB meter
// every force-fresh read and the budget read must agree on, the
// canonical->incumbent class translator SyncStatus's backfill-
// completeness lookup needs, and the on-demand Reconciler ReconcileOverlay
// drives.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// incumbentHubSpot is the connection.Incumbent discriminator for HubSpot —
// the one spelling the reconcile poller, the re-fetch worker, the webhook
// binder, and the human-read shadow all gate on, so a role never routes an
// overlay connection to the wrong adapter by a mistyped literal.
const incumbentHubSpot = "hubspot"

// failClosedOverlayMeter is the OVB meter a surface with no Redis-backed
// meter uses: nil client, so every band sheds and every reservation is
// declined (a role never spends live quota it cannot account for). The
// REST read surface (server.go) and the poller (jobs.go) receive their
// live Redis-backed meter from cmd via WithOverlayMeter / JobRunnerConfig;
// the MCP-tool and workflow surfaces carry no live-incumbent resolver, so
// they never charge a meter and this fail-closed placeholder is all they
// need. Building the meter here (rather than taking a *redis.Client
// parameter) keeps the raw-Redis dependency in the cmd/platform tiers,
// never in compose.
func failClosedOverlayMeter() *overlaybudget.Meter {
	return overlaybudget.New(nil, nil)
}

// OverlayBudgetConfig maps the deployment's per-incumbent OVB config
// (deployconfig.EffectiveOverlayBudget, already validated + default-filled
// at load) onto the platform meter's own Config shape. Compose owns this
// translation so the meter package stays free of any deployconfig import
// (it is a generic platform component, not a margince.yaml reader).
func OverlayBudgetConfig(cfg deployconfig.OverlayBudget) overlaybudget.Config {
	out := make(overlaybudget.Config, len(cfg))
	for name, ib := range cfg {
		out[name] = overlaybudget.IncumbentConfig{
			Search:       overlaybudget.WindowConfig{Ceiling: ib.Search.Ceiling, Cap: ib.Search.Cap},
			REST:         overlaybudget.WindowConfig{Ceiling: ib.REST.Ceiling, Cap: ib.REST.Cap},
			WarnFraction: ib.WarnFraction,
			ShedFraction: ib.ShedFraction,
		}
	}
	return out
}

// NewOverlayHandlers builds the overlay module's connection-lifecycle
// and sync-status/budget/reconcile handlers over pool, vault (the
// credential custodian Connect/Disconnect/Reconcile
// need), meter (GetOverlayBudget's read — see NewOverlayMeter's doc), and
// log (Reconcile's own per-class failure logging). Called from
// WithKeyvault, mirroring NewCaptureRegistry's vault-gated wiring:
// without a vault the overlay surface stays its declared 501 by
// omission, same as capture's connect path.
func NewOverlayHandlers(pool *pgxpool.Pool, vault keyvault.Vault, meter *overlaybudget.Meter, log *slog.Logger, backfillLimit int, onModeFlip func(workspaceID ids.UUID)) overlay.Handlers {
	ms := overlay.NewMirrorStore(pool, unresolvedOwnerEmails{})
	incumbent := overlayIncumbentFactory(backfillLimit)
	svc := overlay.NewService(pool, vault, ms).
		WithBudgetMeter(meter).
		WithIncumbentClassesTranslator(hubspot.IncumbentClassesFor).
		WithIncumbentFactory(incumbent).
		WithModeFlipObserver(onModeFlip).
		WithLogger(log)
	reconciler := overlayReconciler{pool: pool, vault: vault, ms: ms, meter: meter, log: log, newIncumbent: incumbent}
	return overlay.NewHandlers(svc).WithReconciler(reconciler)
}

// hubspotIncumbentFactory builds a live HubSpot adapter over one
// connection's own region + vaulted token — the per-connection seam
// Connect's mirror_user_map seeding resolves the owners directory
// through. It is the ONE place compose binds the concrete incumbent for
// the connection lifecycle (the reconcile poller builds its own the same
// way, jobs.go's reconcileConnection); the overlay module never selects
// an incumbent itself (ADR-0054 §8 — concrete choice injected at compose).
//
//nolint:ireturn // returns the overlay.Incumbent seam by design — it is injected as a per-connection factory the module holds behind the interface, so tests substitute a fake.
func hubspotIncumbentFactory(region, token string) overlay.Incumbent {
	return hubspot.NewAdapter(hubspot.NewClient(region, token))
}

// NewOverlayProvider builds the overlay-mode read seam Dispatcher routes
// to: a MirrorStore over pool plus a FreshnessReader wired with the
// canonical->incumbent translator (hubspot.IncumbentClassesFor) and meter
// (the shared OVB accounting — see NewOverlayMeter's doc on which meter
// instance a caller must pass).
//
// resolveIncumbent is the per-request live-incumbent resolver
// FreshnessReader's force-fresh lane reads through: given the request's
// workspace context it returns a live adapter built from THAT workspace's
// own vaulted region+token. A process-wide Dispatcher cannot bind that at
// construction (each workspace has its own credential), so the read path
// resolves it per call. The api server wires a vault-backed resolver
// (server.go); a role with no vault, or a caller that passes nil, degrades
// force-fresh to the mirror honestly (freshness.go's own doc) — never a
// faked authority claim.
func NewOverlayProvider(pool *pgxpool.Pool, meter *overlaybudget.Meter, resolveIncumbent func(context.Context) (overlay.Incumbent, error)) *overlay.Provider {
	ms := overlay.NewMirrorStore(pool, unresolvedOwnerEmails{})
	ff := overlay.NewFreshnessReader(resolveIncumbent, ms, meter, hubspot.IncumbentClassesFor)
	p := overlay.NewProvider(ms, ff)
	// Wire the write-back path's incumbent resolver too — NewProvider stores
	// only ms+ff, so without this the Provider's Create/Update/Archive verbs
	// answer errNoWriteIncumbent even when a resolver was supplied. A caller
	// that passes nil (the sorDispatch, which is wired later by WithKeyvault)
	// leaves it unset here and SetOverlayIncumbentResolver installs it then.
	p.SetFreshnessIncumbentResolver(resolveIncumbent)
	return p
}

// unresolvedOwnerEmails is the construction-time placeholder
// overlay.OwnerEmailResolver every process-wide MirrorStore is built
// with: resolving an incumbent owner to its live email needs that
// connection's own per-workspace credential, which is not available when
// the server is wired. Every code path that actually resolves an owner —
// Connect seeding (connection.go) and the reconcile sweep
// (jobs.go's reconcileConnection) — rebinds the store to that
// connection's live adapter via MirrorStore.WithResolver BEFORE calling
// SeedUserMap/UpsertUserMap/Ingest, so this placeholder is never reached
// for a real resolution: the read path (NewOverlayProvider's Read/Search)
// never resolves an owner at all, and the write-seeding paths always
// resolve through the live adapter. It answers a clear error rather than
// a fabricated email so any path that DID reach it unrebound fails
// loudly (fail-closed) instead of silently mismatching.
type unresolvedOwnerEmails struct{}

func (unresolvedOwnerEmails) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	return "", fmt.Errorf("overlay: owner-email resolution reached the construction placeholder — a resolving path must rebind the store to a live adapter first (owner %s)", ownerExternalID)
}

// overlayReconciler implements overlay.Reconciler (handlers.go) —
// ReconcileOverlay's on-demand sweep. It resolves the CALLING request's
// own workspace's active incumbent connection (never the whole fleet:
// this is an admin asking "sync my workspace now," not a scheduled
// sweep) and drives reconcileConnection (jobs.go) exactly like the
// periodic worker does per connection, refactored into that one shared
// function so neither call site duplicates the "resolve the vaulted
// token, build a live adapter, sweep every object class" sequence. It
// deliberately reuses ctx as-is (the caller's own already-bound actor +
// correlation from the authenticated HTTP request) rather than
// synthesizing a system principal the way jobs.go's periodic worker
// must (a scheduled tick has no human caller to attribute the sweep's
// mirror.conflict events to; an on-demand admin call already has one).
type overlayReconciler struct {
	pool         *pgxpool.Pool
	vault        keyvault.Vault
	ms           *overlay.MirrorStore
	meter        *overlaybudget.Meter
	log          *slog.Logger
	newIncumbent func(region, token string) overlay.Incumbent
}

func (r overlayReconciler) Reconcile(ctx context.Context) error {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return fmt.Errorf("compose: reconcile called outside a workspace context")
	}
	// DueOverlayConnections is a fleet-wide, rls-exempt enumerator (it has
	// to be: workspace is not itself workspace-scoped) — filtered down to
	// the ONE connection this request's own workspace owns, rather than
	// sweeping every tenant on an admin's single-workspace request.
	due, err := overlay.DueOverlayConnections(ctx, r.pool)
	if err != nil {
		return fmt.Errorf("compose: reconcile: listing overlay-mode workspaces: %w", err)
	}
	for _, d := range due {
		if d.Workspace.UUID == wsID {
			err := reconcileConnection(ctx, r.vault, r.ms, r.meter, r.log, d, r.newIncumbent)
			if errors.Is(err, overlay.ErrConnectionGone) {
				// The connection was revoked between the due-scan above and the
				// sweep's first fenced write (a disconnect racing this on-demand
				// reconcile). That is the same mode-question the fallthrough
				// below answers — not an opaque 500 — so collapse it here.
				return apperrors.ErrModeNotOverlay
			}
			return err
		}
	}
	// No active connection for THIS workspace — the same mode-question
	// GetSyncStatus/GetBudget answer with ErrModeNotOverlay, since a
	// reconcile sweep is meaningless without one.
	return apperrors.ErrModeNotOverlay
}

// overlayMetricsSection answers the /metrics overlay section for srv,
// nil when this role never wired a vault (WithKeyvault absent) — the
// same "declared or absent" posture the rest of /metrics/readyz already
// follows for the outbox bus/blobstore/schema pool. It is a plain
// function, not a Server method, so server.go's operationalMux (which
// already reads every other optional probe off srv) stays the one place
// that assembles the /metrics wiring.
func overlayMetricsSection(srv Server, pool *pgxpool.Pool) *httpserver.OverlayMetrics {
	if srv.vault == nil {
		return nil
	}
	return &httpserver.OverlayMetrics{
		SourceLag: func(ctx context.Context) (map[string]time.Duration, error) {
			return overlay.SourceLagByClass(ctx, pool, time.Now)
		},
		SyncedTotal:   overlay.MirrorSyncedTotal,
		ConflictTotal: overlay.MirrorConflictTotal,
		DeletedTotal:  overlay.MirrorDeletedTotal,
	}
}
