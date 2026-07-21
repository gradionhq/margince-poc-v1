// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns the incumbent connection lifecycle (design.md §4.3):
// Connect/Get over incumbent_connection. Connect is a genuine
// system-of-record mutation — a workspace choosing its incumbent binding
// — so it carries the full write shape (storekit.Audit + storekit.Emit
// in the same transaction as the domain row), unlike mirror ingest
// (mirrorstore.go), which is a derived-cache refresh with no audit
// trail. Disconnect's teardown/purge/scrub lives in teardown.go — this
// file only flips the connection row and the workspace mode columns on
// the way in.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// hubspotIncumbent is the only incumbent branch-1 wires (design.md §2 —
// D2/D3): the connect path refuses any other value rather than silently
// accepting an incumbent name nothing implements.
const hubspotIncumbent = "hubspot"

// overlayConnectionObject is the identity/internal/policy RBAC object
// name Connect/Get/Disconnect gate on — spelled once so the service
// methods and the integration-test fixtures that grant it can't drift
// apart on a typo'd string.
const overlayConnectionObject = "overlay_connection"

// statusActive/statusRevoked are incumbent_connection.status's two
// branch-1 values (the CHECK also allows 'error', which no code path
// here sets yet) — named once since both Connect and Disconnect's audit
// snapshots reference them.
const (
	statusActive  = "active"
	statusRevoked = "revoked"
)

// auditFieldIncumbent/auditFieldRegion name the columns Connect's and
// Disconnect's audit before/after snapshots carry, spelled once so the
// two call sites (and teardown.go's) can't drift apart on a typo'd key.
const (
	auditFieldIncumbent = "incumbent"
	auditFieldRegion    = "region"
	auditFieldStatus    = "status"
)

// leastPrivilegeHubSpotScopes is the fixed, server-determined scope set
// Connect records — never client-supplied (a caller cannot widen its own
// incumbent grant by asking for more). contacts/companies/deals.read plus
// crm.schemas.*.read serve the mirror's own reads; crm.objects.owners.read
// is required for mirror_user_map's hubspot_owner_id→email resolution
// (design.md §4.3/§7 — the Owners API 403'd without it in the spike).
// Custom-schema-write and every other write scope stay unrequested: the
// bounded-capability manifest declares them unsupported_by_sor and this
// connection never asks for them.
var leastPrivilegeHubSpotScopes = []string{
	"crm.objects.contacts.read",
	"crm.objects.companies.read",
	"crm.objects.deals.read",
	"crm.schemas.contacts.read",
	"crm.schemas.companies.read",
	"crm.schemas.deals.read",
	"crm.objects.owners.read",
}

// Connection is the incumbent_connection row as read back — the
// credential itself never rides this shape (it lives sealed in the
// vault, addressed by an opaque ref this type never carries).
type Connection struct {
	Incumbent   string
	Region      string
	Status      string
	ConnectedAt time.Time
	Scopes      []string
}

// ConnectInput is Connect's request: the incumbent name, its region
// (EU-region routing, design.md §4.3), and the private-app token to
// seal. Scopes are never part of the input — Connect always records
// leastPrivilegeHubSpotScopes.
type ConnectInput struct {
	Incumbent string
	Region    string
	Token     string
}

// Service owns the incumbent connection lifecycle and the mirror
// teardown Disconnect drives (teardown.go). ms is threaded through so
// the sync-status/budget/reconcile handlers can share this one
// construction site rather than re-plumbing compose wiring later.
// meter and toIncumbentClass are both optional (nil-safe): meter backs
// GetOverlayBudget's Snapshot read — it MUST be
// the SAME *Meter instance FreshnessReader's force-fresh lane consumes
// against (compose/overlay.go wires this explicitly), or the budget read
// would answer an always-empty window nothing ever fed. toIncumbentClass
// answers SyncStatus's per-object backfillComplete lookup (overlay_
// backfill_cursor is keyed by the INCUMBENT class name, while overlay_
// mirror — and this Service's own canonical-facing callers — are keyed
// by the CANONICAL entity type; see freshness.go's identical seam for
// why this package cannot resolve that translation itself: the concrete
// mapping registry lives in the overlay/hubspot subpackage, which
// imports THIS package, so the reverse import would cycle).
type Service struct {
	pool             *pgxpool.Pool
	vault            keyvault.Vault
	ms               *MirrorStore
	meter            *Meter
	toIncumbentClass func(canonical string) (incumbentClass string, ok bool)
	incumbent        func(region, token string) Incumbent
	log              *slog.Logger
	// modeFlipped observes a committed x_sor_mode flip (Connect →
	// overlay, Disconnect → native) so a mode-caching read dispatcher
	// can drop its entry instead of serving the OLD mode for a cache
	// TTL. nil means no observer is composed — the flip still commits.
	modeFlipped func(workspaceID ids.UUID)
}

// NewService constructs a Service over pool, vault (the credential
// custodian), and ms (the mirror store teardown purges).
func NewService(pool *pgxpool.Pool, vault keyvault.Vault, ms *MirrorStore) *Service {
	return &Service{pool: pool, vault: vault, ms: ms, log: slog.Default()}
}

// WithModeFlipObserver wires the committed-mode-flip observer (the
// compose dispatcher's cache invalidation) — called after Connect's and
// Disconnect's transactions commit, never on a rolled-back attempt.
// Returns s so compose can chain it onto NewService's result.
func (s *Service) WithModeFlipObserver(fn func(workspaceID ids.UUID)) *Service {
	s.modeFlipped = fn
	return s
}

// notifyModeFlip reports a committed x_sor_mode flip to the composed
// observer, if any.
func (s *Service) notifyModeFlip(workspaceID ids.UUID) {
	if s.modeFlipped != nil {
		s.modeFlipped(workspaceID)
	}
}

// WithBudgetMeter wires the OVB meter GetOverlayBudget reads — see the
// Service doc for why this must be the compose layer's ONE shared
// instance, not a freshly minted one. Returns s so compose can chain it
// onto NewService's result at the construction site.
func (s *Service) WithBudgetMeter(meter *Meter) *Service {
	s.meter = meter
	return s
}

// WithIncumbentClassTranslator wires the canonical->incumbent class
// translator (e.g. hubspot.IncumbentClassFor) SyncStatus's backfill-
// completeness lookup needs — see the Service doc's cycle note on why
// this package cannot hold that mapping itself.
func (s *Service) WithIncumbentClassTranslator(fn func(string) (string, bool)) *Service {
	s.toIncumbentClass = fn
	return s
}

// WithIncumbentFactory wires the per-connection incumbent adapter builder
// (region + token → Incumbent, e.g. hubspot.NewAdapter over a fresh
// client) Connect uses to seed mirror_user_map from the owners directory
// the moment an overlay is connected. compose injects it — the module
// never selects a concrete incumbent itself (the same posture
// WithIncumbentClassTranslator takes for the class mapping). Without it
// Connect skips connect-time seeding by omission; the reconcile poller's
// own per-sweep seeding still fills mirror_user_map on its next tick.
func (s *Service) WithIncumbentFactory(fn func(region, token string) Incumbent) *Service {
	s.incumbent = fn
	return s
}

// WithLogger sets the logger Connect's best-effort seeding reports a
// non-fatal owners-directory failure through. Defaults to slog.Default().
func (s *Service) WithLogger(log *slog.Logger) *Service {
	s.log = log
	return s
}

// Connect seals in.Token into the vault, then — in one transaction —
// inserts the incumbent_connection row (write shape: domain row + Audit
// + Emit) and flips workspace.x_sor_mode/x_incumbent together (the
// x_overlay_iff_incumbent CHECK demands both change in the same
// statement). Gated by auth.Require("overlay_connection", ActionCreate):
// connecting is destructive workspace-wide config (it will later purge
// the mirror on Disconnect and flips sor_mode for every seat), so it is
// admin/ops-only (identity/internal/policy), the same posture as quota.
//
// UNIQUE(workspace_id) means a second Connect on an already-connected
// workspace answers apperrors.ErrIncumbentAlreadyConnected. existingConnection
// checks for that BEFORE sealing anything, so the common duplicate-connect
// case never touches the vault; the vault.Put below still runs ahead of the
// insert (put-then-commit, the same posture capture.Registry.Connect
// documents), so a genuine concurrent-Connect race can still lose the
// INSERT after sealing — that path deletes its own orphaned ref rather
// than leaving it unreferenced.
func (s *Service) Connect(ctx context.Context, in ConnectInput) (Connection, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionCreate); err != nil {
		return Connection{}, err
	}
	if err := in.validate(); err != nil {
		return Connection{}, err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return Connection{}, errors.New("overlay: connect called outside a workspace context")
	}

	if exists, err := s.hasConnection(ctx); err != nil {
		return Connection{}, err
	} else if exists {
		return Connection{}, apperrors.ErrIncumbentAlreadyConnected
	}

	ref, err := s.vault.Put(ctx, ids.From[ids.WorkspaceKind](ws), []byte(in.Token))
	if err != nil {
		return Connection{}, fmt.Errorf("overlay: sealing the incumbent credential: %w", err)
	}

	out, err := s.insertConnection(ctx, in, ref)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return Connection{}, s.cleanupOrphanedRef(ctx, ws, ref)
		}
		return Connection{}, err
	}
	s.notifyModeFlip(ws)
	s.seedUserMapOnConnect(ctx, in)
	return out, nil
}

// seedUserMapOnConnect populates mirror_user_map from the incumbent's
// owners directory the moment an overlay is connected, so a matched user
// sees the already-mirrored rows without waiting for the first reconcile
// sweep. It is best-effort: the connection is already committed and the
// reconcile poller re-seeds every tick, so a directory-fetch or per-owner
// match failure is logged, never surfaced as a Connect failure (which
// would falsely tell the admin the connection did not take). It binds the
// store to THIS connection's live incumbent adapter so UpsertUserMap's
// email re-verification resolves against the incumbent's current owner
// emails. Skipped by omission when no incumbent factory was wired.
func (s *Service) seedUserMapOnConnect(ctx context.Context, in ConnectInput) {
	if s.incumbent == nil {
		return
	}
	inc := s.incumbent(in.Region, in.Token)
	owners, err := inc.Owners(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "overlay connect: fetching the owners directory to seed mirror_user_map failed",
			"incumbent", in.Incumbent, "err", err)
		return
	}
	if err := s.ms.WithResolver(inc).SeedUserMap(ctx, in.Incumbent, owners); err != nil {
		s.log.WarnContext(ctx, "overlay connect: seeding mirror_user_map from the owners directory failed",
			"incumbent", in.Incumbent, "err", err)
	}
}

// validate rejects an unsupported incumbent or a missing region/token
// before Connect touches the vault or the database.
func (in ConnectInput) validate() error {
	if in.Incumbent != hubspotIncumbent {
		return fmt.Errorf("overlay: incumbent %q is not supported in branch 1: %w", in.Incumbent, apperrors.ErrUnsupportedBySoR)
	}
	if in.Region == "" {
		return errors.New("overlay: connect requires a region")
	}
	if in.Token == "" {
		return errors.New("overlay: connect requires a private-app token")
	}
	return nil
}

// insertConnection runs Connect's write-shape transaction: the domain
// row + Audit + Emit + the workspace mode flip, all in one
// database.WithWorkspaceTx.
func (s *Service) insertConnection(ctx context.Context, in ConnectInput, ref keyvault.Ref) (Connection, error) {
	var out Connection
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id ids.UUID
		var connectedAt time.Time
		if scanErr := tx.QueryRow(
			ctx, `
			INSERT INTO incumbent_connection (workspace_id, incumbent, region, credential_ref, scopes)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			RETURNING id, connected_at`,
			in.Incumbent, in.Region, string(ref), leastPrivilegeHubSpotScopes,
		).Scan(&id, &connectedAt); scanErr != nil {
			return scanErr
		}

		after := map[string]any{
			auditFieldIncumbent: in.Incumbent,
			auditFieldRegion:    in.Region,
			"scopes":            leastPrivilegeHubSpotScopes,
			auditFieldStatus:    statusActive,
		}
		auditID, auditErr := storekit.Audit(ctx, tx, "create", "incumbent_connection", id, nil, after)
		if auditErr != nil {
			return fmt.Errorf("overlay: auditing the incumbent connection: %w", auditErr)
		}
		if emitErr := storekit.Emit(ctx, tx, auditID, "incumbent.connected", "incumbent_connection", id, after); emitErr != nil {
			return fmt.Errorf("overlay: emitting incumbent.connected: %w", emitErr)
		}

		if _, updErr := tx.Exec(ctx, `
			UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = $1
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
			in.Incumbent); updErr != nil {
			return fmt.Errorf("overlay: flipping the workspace to overlay mode: %w", updErr)
		}

		out = Connection{
			Incumbent:   in.Incumbent,
			Region:      in.Region,
			Status:      statusActive,
			ConnectedAt: connectedAt,
			Scopes:      leastPrivilegeHubSpotScopes,
		}
		return nil
	})
	if err != nil {
		return Connection{}, err
	}
	return out, nil
}

// cleanupOrphanedRef deletes a vault ref this Connect attempt sealed but
// lost the race to persist (the INSERT hit UNIQUE(workspace_id) after
// vault.Put already ran) — Delete is idempotent, so this is safe to
// retry. A cleanup failure is surfaced rather than masked, but never
// shadows the ErrIncumbentAlreadyConnected the caller actually needs.
func (s *Service) cleanupOrphanedRef(ctx context.Context, ws ids.UUID, ref keyvault.Ref) error {
	if delErr := s.vault.Delete(ctx, ids.From[ids.WorkspaceKind](ws), ref); delErr != nil {
		return fmt.Errorf("overlay: connect lost a concurrent race (already connected) and failed to clean up its orphaned vault entry: %w", delErr)
	}
	return apperrors.ErrIncumbentAlreadyConnected
}

// hasConnection reports whether the workspace already has an
// incumbent_connection row (any status) — Connect's pre-flight check so
// the common duplicate-connect case answers ErrIncumbentAlreadyConnected
// before ever calling vault.Put.
func (s *Service) hasConnection(ctx context.Context) (bool, error) {
	var exists bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(
			ctx, `
			SELECT EXISTS(SELECT 1 FROM incumbent_connection
				WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)`,
		).Scan(&exists)
	})
	if err != nil {
		return false, fmt.Errorf("overlay: checking for an existing incumbent connection: %w", err)
	}
	return exists, nil
}

// Get reads the workspace's current incumbent connection. Gated by
// auth.Require("overlay_connection", ActionRead) — every role holds this
// grant (identity/internal/policy), so any authenticated seat may check
// whether overlay mode is live, the same posture as a quota's attainment
// read. No EnsureVisible probe runs: like quota, incumbent_connection is
// a workspace-shared singleton governed by the object grant alone, never
// row-scoped (there is exactly one row per tenant; RLS alone already
// walls off other tenants).
// apperrors.ErrNotFound means no connection row was ever inserted for
// this workspace — a revoked connection still reads back (its status
// column carries that fact, and Disconnect never deletes the lifecycle
// row itself).
func (s *Service) Get(ctx context.Context) (Connection, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionRead); err != nil {
		return Connection{}, err
	}
	var out Connection
	var connectedAt time.Time
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT incumbent, region, status, connected_at, scopes
			FROM incumbent_connection
			WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`).
			Scan(&out.Incumbent, &out.Region, &out.Status, &connectedAt, &out.Scopes)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, apperrors.ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	out.ConnectedAt = connectedAt
	return out, nil
}

// DueOverlayConnection names one active overlay incumbent connection to
// sweep — the poller's per-tenant enumeration unit (jobs.go's worker),
// mirroring capture.DueConnection
// (registry_connections.go): workspace + credential ref + region,
// everything the poller needs to build a live incumbent adapter without
// reaching into incumbent_connection's columns itself.
type DueOverlayConnection struct {
	Workspace     ids.WorkspaceID
	Incumbent     string
	Region        string
	CredentialRef keyvault.Ref
}

// DueOverlayConnections lists every workspace with an ACTIVE incumbent
// connection, fleet-wide — the same rls-exempt fleet-walk shape
// capture.Registry.DueConnections uses (workspace is not itself
// workspace-scoped, so this reads every tenant before entering each
// one's own GUC to read its own incumbent_connection row). One
// workspace's read failure is joined into the returned error but does
// not stop the rest of the fleet from being enumerated.
func DueOverlayConnections(ctx context.Context, pool *pgxpool.Pool) ([]DueOverlayConnection, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL AND x_sor_mode = 'overlay' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("overlay: listing overlay-mode workspaces: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}

	var due []DueOverlayConnection
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		ws := ids.From[ids.WorkspaceKind](wsID)
		err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			var incumbent, region, ref string
			scanErr := tx.QueryRow(wsCtx, `
				SELECT incumbent, region, credential_ref FROM incumbent_connection
				WHERE status = $1`, statusActive).Scan(&incumbent, &region, &ref)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				// x_sor_mode='overlay' with no active connection row is a
				// transient state (mid-teardown, or a row inserted but not
				// yet committed) — the poller simply has nothing to sweep
				// for this workspace this tick, not an error.
				return nil
			}
			if scanErr != nil {
				return scanErr
			}
			due = append(due, DueOverlayConnection{
				Workspace: ws, Incumbent: incumbent, Region: region, CredentialRef: keyvault.Ref(ref),
			})
			return nil
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("overlay: reading the incumbent connection in workspace %s: %w", wsID, err))
		}
	}
	return due, errs
}
