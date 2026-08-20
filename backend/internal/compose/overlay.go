// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay connection lifecycle, assembled: overlay.Service over the
// pool and the secret vault, wired into overlay.Handlers — composed here
// so overlay never imports keyvault's concrete provider selection (the
// same posture capture.go documents for NewCaptureRegistry). This also
// wires the sync-status/budget surface: the shared OVB meter every
// force-fresh read and the budget read must agree on, the
// canonical->incumbent class translator SyncStatus's backfill-
// completeness lookup needs, and the mapping registry's current projection
// fingerprints, which the flip preflight compares every mirror row against.
// All three are injected here because the overlay module must never import
// its own hubspot subpackage — that subpackage imports IT.
// ReconcileOverlay's on-demand sweep request
// (overlay.Service.RequestSweep) needs none of this compose-level wiring —
// it only marks the workspace due, which the worker's own periodic sweep
// (jobs_overlay.go) then picks up.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
// no tool or workflow path reaches Dispatcher.Freshness, the only route to a
// force-fresh reservation on this provider, so those surfaces never charge a
// meter and this fail-closed placeholder is all they need. The live
// reservations and charges live in the refetch and reconcile pollers, which
// take their own Redis-backed meters.
//
// Building the meter here (rather than taking a *redis.Client parameter)
// keeps the raw-Redis dependency in the cmd/platform tiers, never in
// compose.
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

// NewOverlayHandlers builds the overlay module's connection-lifecycle,
// sync-status/budget, and reconcile handlers over pool, vault (the
// credential custodian Connect/Disconnect need), meter (GetOverlayBudget's
// read — see NewOverlayMeter's doc), and log. Called from WithKeyvault,
// mirroring NewCaptureRegistry's vault-gated wiring: without a vault the
// overlay surface stays its declared 501 by omission, same as capture's
// connect path. ReconcileOverlay itself (overlay.Service.RequestSweep)
// only marks the workspace due — the live sweep runs in the worker
// (jobs_overlay.go's overlayReconcileWorker), which builds its own
// incumbent adapter from the same overlayIncumbentFactory.
func NewOverlayHandlers(pool *pgxpool.Pool, vault keyvault.Vault, meter *overlaybudget.Meter, log *slog.Logger, backfillLimit int, onModeFlip func(workspaceID ids.UUID)) overlay.Handlers {
	ms := overlay.NewMirrorStore(InstallationDB(pool), unresolvedOwnerEmails{})
	incumbent := overlayIncumbentFactory(backfillLimit)
	svc := overlay.NewService(InstallationDB(pool), vault, ms).
		WithBudgetMeter(meter).
		WithIncumbentClassesTranslator(hubspot.IncumbentClassesFor).
		WithProjectionFingerprints(OverlayProjectionFingerprints()).
		WithIncumbentFactory(incumbent).
		WithModeFlipObserver(onModeFlip).
		WithFlipImportProbe(FlipImportProbe).
		WithLogger(log)
	return overlay.NewHandlers(svc).WithFlipRunner(newFlipRunner(pool, svc, ms, log))
}

// OverlayProjectionFingerprints answers the current declaration fingerprint of
// every incumbent class this composition mirrors — what NewOverlayHandlers
// injects, and therefore what the flip preflight judges every mirror row
// against. It is exported because this package is the ONE sanctioned place
// that names the concrete incumbent (AC-OV-1): a suite that seeds the mirror
// through the fake incumbent still needs to stamp its rows with the
// declaration the composed server recognises as current, and asks HERE rather
// than reaching for the adapter itself.
func OverlayProjectionFingerprints() map[string]string {
	return hubspot.ProjectionFingerprints()
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
	return NewOverlayProviderFor(InstallationDB(pool), meter, resolveIncumbent)
}

// NewOverlayProviderFor is NewOverlayProvider over a handle whose workspace is
// already decided — the harness half of the same wiring. A server resolves the
// installation's singleton; a suite that seeds a second workspace to prove one
// tenant cannot read another names the one it means (ADR-0091 §9 step 3).
func NewOverlayProviderFor(db *database.DB, meter *overlaybudget.Meter, resolveIncumbent func(context.Context) (overlay.Incumbent, error)) *overlay.Provider {
	ms := overlay.NewMirrorStore(db, unresolvedOwnerEmails{})
	ff := overlay.NewFreshnessReader(resolveIncumbent, ms, meter, hubspot.IncumbentClassesFor)
	p := overlay.NewProvider(ms, ff)
	// Wire the write-back path's incumbent resolver too — NewProvider stores
	// only ms+ff, so without this the Provider's Create/Update/Archive verbs
	// answer errNoWriteIncumbent even when a resolver was supplied. A caller
	// that passes nil (the sorDispatch, which is wired later by WithKeyvault)
	// leaves it unset here and SetOverlayIncumbentResolver installs it then.
	p.SetFreshnessIncumbentResolver(resolveIncumbent)
	// Wire the echo-suppression ledger's producer half (OVA-DDL-6): each
	// write-back opens ledger entries so the webhook receiver can drop its echo.
	p.SetWriteLedger(overlay.NewWriteLedger(db), slog.Default())
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
