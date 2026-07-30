// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay→native flip's orchestration (B-E18.26/27, ADR-0071): the
// one place the overlay module's preflight primitives, the migration
// engine, and the native writer seam meet. The runner implements
// overlay.FlipRunner and is injected into the overlay handlers — the
// module keeps its transport, this file keeps the cross-module wiring
// (the compose charter).
//
// Cutover semantics (OVA-AC-6):
//   - fresh_sync (the default): every readiness check must hold — an
//     unreachable incumbent, an unconverged sync, draining writes, or a
//     missing pre-flip export each block honestly with a named reason,
//     the mirror unseals (F1's no-op return to a healthy overlay), and
//     nothing is partially migrated.
//   - emergency: the ADR-0071 last-known-mirror cutover. Available ONLY
//     while the incumbent is unreachable (refused otherwise — never a
//     substitute in either direction), confirm-first via the same typed
//     phrase, disclosed-lossy: the 202 carries the snapshot's staleness
//     and the unverifiable-parity notice.
//
// The import runs synchronously behind the 202 (the DisconnectOverlay
// precedent, handlers.go): the run record's checkpoint makes a killed
// request resumable by executing again — the same run continues rather
// than restarting.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// flipConfirmationPhrase is the typed-phrase gate (AC-mode-flip-5).
const flipConfirmationPhrase = "FLIP TO SOR"

// flipUnverifiableParityNotice is the OVA-AC-6(b) disclosure: an
// emergency cutover's parity cannot be re-verified against a live
// incumbent — stated, not implied.
const flipUnverifiableParityNotice = "Cut over from the last-known mirror snapshot: record parity cannot be re-verified against the incumbent, which is unreachable. Data changed in the incumbent after the last sync is not included."

// flipRunner implements overlay.FlipRunner over the overlay preflight
// primitives + the migration engine.
type flipRunner struct {
	pool *pgxpool.Pool
	svc  *overlay.Service
	ms   *overlay.MirrorStore
	runs *migration.RunStore
	log  *slog.Logger
}

var _ overlay.FlipRunner = (*flipRunner)(nil)

func newFlipRunner(pool *pgxpool.Pool, svc *overlay.Service, ms *overlay.MirrorStore, log *slog.Logger) *flipRunner {
	return &flipRunner{pool: pool, svc: svc, ms: ms, runs: migration.NewRunStore(pool), log: log}
}

// flipVerdict is the runner's internal readiness read: the raw checks
// plus the derived blocking reasons for a fresh-sync flip.
type flipVerdict struct {
	checks   overlay.FlipChecks
	blocking []crmcontracts.OverlayFlipPreflightBlocking
}

func (f *flipRunner) verdict(ctx context.Context) (flipVerdict, error) {
	checks, err := f.svc.FlipChecks(ctx)
	if err != nil {
		return flipVerdict{}, err
	}
	v := flipVerdict{checks: checks}
	// The same guard the direct importer runs (one constant, one rule):
	// a live-read import cannot pass with the connection revoked/error.
	if err := migration.GuardIncumbentSource(checks.ConnectionStatus); err != nil {
		v.blocking = append(v.blocking, crmcontracts.IncumbentUnreachable)
	}
	if !checks.ForceFreshDone {
		v.blocking = append(v.blocking, crmcontracts.ForceFreshIncomplete)
	}
	if checks.PendingSyncCount > 0 {
		v.blocking = append(v.blocking, crmcontracts.PendingSyncDraining)
	}
	exported, err := f.exportSince(ctx, checks.LastSyncedAt)
	if err != nil {
		return flipVerdict{}, err
	}
	if !exported {
		v.blocking = append(v.blocking, crmcontracts.ExportMissing)
	}
	return v, nil
}

// exportSince answers whether a workspace export bundle was written
// after the mirror's freshest watermark — the preflight's "honest-scope
// export available" check (B-E18.26). The export audit row is the
// bundle writer's own (export.go); a bundle older than the mirror's
// last change no longer captures the estate the flip will migrate.
func (f *flipRunner) exportSince(ctx context.Context, since time.Time) (bool, error) {
	var ok bool
	err := database.WithWorkspaceTx(ctx, f.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM audit_log
				WHERE entity_type = 'workspace' AND action = 'export' AND occurred_at >= $1)`,
			since,
		).Scan(&ok)
	})
	if err != nil {
		return false, fmt.Errorf("flip preflight: checking for a pre-flip export: %w", err)
	}
	return ok, nil
}

// Preflight is OVA-WIRE-7: {ready, blocking[], unresolved_conflicts[]}
// plus the sealed snapshot and parity preview when green, the emergency
// disclosure when the incumbent is unreachable. Gated by the
// overlay_connection UPDATE grant and human-only at the transport (a
// green preflight SEALS the mirror — a durable state change that halts
// incumbent sync — and the mode-flip screen is owner-gated, UC-E18-04
// E3), so neither a read-only role nor an agent passport reaches it.
//
// The seal is all-or-nothing: it survives ONLY a fully green verdict
// whose parity preview also succeeded. Every other exit — a blocker, a
// dry-run failure, a mid-flight error — unseals, so a preflight can
// never strand the workspace frozen (UC-E18-04 F1's no-op return to a
// healthy overlay).
func (f *flipRunner) Preflight(ctx context.Context) (verdictOut crmcontracts.OverlayFlipPreflight, err error) {
	if err := auth.Require(ctx, "overlay_connection", principal.ActionUpdate); err != nil {
		return crmcontracts.OverlayFlipPreflight{}, err
	}
	// The contract's human-only annotation is enforced at the transport;
	// this is the second lock on a one-way door (the same shape
	// overlay's requireUserMapAdmin uses), so the op does not depend on
	// route-pattern resolution alone.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.OverlayFlipPreflight{}, err
	}
	// The same claim the execute takes: a preflight that unseals while
	// an import is mid-run would let the mirror drift under a positional
	// cursor, silently dropping one estate row per concurrent insert.
	// Holding it does NOT make this look like a running migration —
	// FlipImportProbe requires a live run row too.
	unlock, err := f.claimFlip(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipPreflight{}, err
	}
	defer unlock()

	sealed := false
	defer func() {
		if sealed {
			return
		}
		unsealErr := f.svc.UnsealFlipSnapshot(ctx)
		if unsealErr == nil {
			return
		}
		if err == nil {
			verdictOut, err = crmcontracts.OverlayFlipPreflight{}, unsealErr
			return
		}
		// The preflight already failed AND the unseal failed: the
		// workspace is frozen with nobody told, which is exactly the
		// state this defer exists to prevent. The original failure is
		// what the caller needs, so the unseal failure is logged rather
		// than swallowed or substituted.
		f.log.Error("overlay flip: the mirror stayed frozen after a failed preflight — unsealing failed",
			"preflight_err", err, "unseal_err", unsealErr)
	}()

	v, err := f.verdict(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipPreflight{}, err
	}
	out := crmcontracts.OverlayFlipPreflight{
		Ready:               len(v.blocking) == 0,
		Blocking:            v.blocking,
		UnresolvedConflicts: []crmcontracts.OverlayFlipUnresolvedConflict{},
	}
	if !out.Ready {
		if blockingContains(v.blocking, crmcontracts.IncumbentUnreachable) {
			out.Emergency = wireEmergency(v.checks)
		}
		return out, nil
	}

	snap, err := f.svc.SealFlipSnapshot(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipPreflight{}, err
	}
	out.Snapshot = &crmcontracts.OverlayFlipSnapshot{FrozenAt: snap.FrozenAt, Id: snap.ID}

	parity, err := f.parityPreview(ctx, v.checks.Incumbent)
	if err != nil {
		return crmcontracts.OverlayFlipPreflight{}, err
	}
	out.Parity = &parity
	sealed = true
	return out, nil
}

// parityPreview runs the migration engine's zero-write dry-run over the
// sealed mirror (AC-mode-flip-7): counts per object, skips with reasons.
func (f *flipRunner) parityPreview(ctx context.Context, incumbent string) ([]crmcontracts.OverlayFlipParityEntry, error) {
	// The dry-run writes nothing, so it needs neither a run id to
	// attribute identities to nor an operator to inherit records.
	writers := newFlipWriters(f.pool, f.ms, incumbent)
	rep, err := migration.NewEngine(f.runs, writers).DryRun(ctx, mirrorFlipSource{ms: f.ms})
	if err != nil {
		return nil, fmt.Errorf("flip preflight: parity dry-run: %w", err)
	}
	out := make([]crmcontracts.OverlayFlipParityEntry, 0, len(rep.Objects))
	for _, or := range rep.Objects {
		entry := crmcontracts.OverlayFlipParityEntry{
			MirrorCount: or.MirrorCount, Object: or.Object,
			WillCreate: or.WillCreate, WillUpdate: or.WillUpdate,
		}
		if len(or.Skipped) > 0 {
			skipped := make([]crmcontracts.OverlayFlipParitySkip, 0, len(or.Skipped))
			for _, s := range or.Skipped {
				skipped = append(skipped, crmcontracts.OverlayFlipParitySkip{ExternalId: s.ExternalID, Reason: s.Reason})
			}
			entry.Skipped = &skipped
		}
		out = append(out, entry)
	}
	return out, nil
}

// Execute is OVA-WIRE-8. The typed phrase gates both modes; fresh_sync
// re-validates every check and refuses with 409 overlay_flip_blocked
// naming the reasons; emergency refuses while the incumbent is
// reachable. On success the workspace is native and the 202 carries the
// run id (and the emergency disclosure when lossy).
func (f *flipRunner) Execute(ctx context.Context, req crmcontracts.OverlayFlipRequest) (crmcontracts.OverlayFlipAccepted, error) {
	if err := auth.Require(ctx, "overlay_connection", principal.ActionUpdate); err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}
	// See Preflight: the human-only class is enforced here too, not only
	// by the transport gate.
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}
	mode, err := parseFlipRequest(req)
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}

	// One flip at a time per workspace, claimed BEFORE the verdict:
	// admitMode may unseal, and an unseal racing another request's
	// running import is exactly the drift the freeze exists to prevent.
	// Without the claim two overlapping executes would also re-enter the
	// same run with independent writer caches and import the estate
	// twice.
	unlock, err := f.claimFlip(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}
	defer unlock()

	v, err := f.verdict(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}

	if err := f.admitMode(ctx, mode, v); err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}

	// Freeze (idempotent — a green preflight already sealed).
	snap, err := f.svc.SealFlipSnapshot(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}

	run, err := f.resumeOrCreateRun(ctx, snap, string(mode))
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}

	operator, err := flipOperator(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}
	source := mirrorFlipSource{ms: f.ms}
	writers := newFlipWriters(f.pool, f.ms, v.checks.Incumbent).forRun(run.ID, operator)
	assocs, err := source.Associations(ctx)
	if err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}
	writers.SetAssociations(assocs)

	rep, err := migration.NewEngine(f.runs, writers).Run(ctx, run.ID, source)
	if err != nil {
		// The run record holds the failure + checkpoint: executing again
		// resumes it. The mirror stays frozen — the estate must not
		// drift between attempts. The cause is logged, not wrapped onto
		// the wire: the engine's chain names internal call sites and can
		// carry store sentinels that would remap the status.
		f.log.Error("overlay flip: migration run failed", "run_id", run.ID.String(), "mode", string(mode), "err", err)
		return crmcontracts.OverlayFlipAccepted{}, fmt.Errorf(
			"the flip's migration run did not complete; it is resumable — re-run the flip to continue from its checkpoint (run %s): %w",
			run.ID, apperrors.ErrConflict)
	}

	if err := f.svc.CompleteFlip(ctx, run.ID, string(mode)); err != nil {
		return crmcontracts.OverlayFlipAccepted{}, err
	}
	f.log.Info("overlay flip completed", "run_id", run.ID.String(), "mode", string(mode), "imported", rep.Imported)

	imported := rep.Imported
	out := crmcontracts.OverlayFlipAccepted{
		RunId:           openapi_types.UUID(run.ID),
		Mode:            crmcontracts.OverlayFlipAcceptedMode(mode),
		RecordsImported: &imported,
	}
	if mode == crmcontracts.OverlayFlipRequestModeEmergency {
		out.EmergencyDisclosure = emergencyDisclosure(v.checks.LastSyncedAt)
	}
	return out, nil
}

// parseFlipRequest is the confirm-first gate on the request itself: the
// exact typed phrase (AC-mode-flip-5), and a mode the contract knows.
// An absent body decodes to the zero request, which fails the phrase
// check — the same refusal a wrong phrase gets.
func parseFlipRequest(req crmcontracts.OverlayFlipRequest) (crmcontracts.OverlayFlipRequestMode, error) {
	if req.ConfirmationPhrase != flipConfirmationPhrase {
		return "", httperr.Validation("confirmation_phrase", "confirmation_phrase_mismatch",
			fmt.Sprintf("type the exact phrase %q to run the flip", flipConfirmationPhrase))
	}
	if req.Mode == nil {
		return crmcontracts.OverlayFlipRequestModeFreshSync, nil
	}
	if !req.Mode.Valid() {
		return "", httperr.Validation("mode", "invalid_mode", "mode must be fresh_sync or emergency")
	}
	return *req.Mode, nil
}

// admitMode is the per-mode gate: fresh_sync requires every readiness
// check (and unseals when one fails, so a refused execute leaves a
// healthy overlay), emergency requires the opposite — an unreachable
// incumbent — plus a mirror to cut over from and the pre-flip export
// that keeps the rebuild promise real even on the lossy path.
func (f *flipRunner) admitMode(ctx context.Context, mode crmcontracts.OverlayFlipRequestMode, v flipVerdict) error {
	if mode == crmcontracts.OverlayFlipRequestModeFreshSync {
		if len(v.blocking) == 0 {
			return nil
		}
		if err := f.svc.UnsealFlipSnapshot(ctx); err != nil {
			return err
		}
		return flipBlocked(v.blocking)
	}
	// Never a substitute: the emergency path is refused while a
	// fresh-sync flip is possible (OVA-AC-6 b).
	if migration.GuardIncumbentSource(v.checks.ConnectionStatus) == nil {
		return fmt.Errorf("the incumbent is reachable — run the fresh-sync flip; the emergency cutover is only for a lost incumbent: %w", apperrors.ErrOverlayFlipBlocked)
	}
	if v.checks.MirrorRows == 0 {
		return fmt.Errorf("no mirror snapshot exists to cut over from: %w", apperrors.ErrOverlayFlipBlocked)
	}
	if blockingContains(v.blocking, crmcontracts.ExportMissing) {
		// Reversibility-as-reconstruction needs the pre-flip export even
		// on the lossy path — the mirror is static, so the export is
		// still producible before cutting over.
		return flipBlocked([]crmcontracts.OverlayFlipPreflightBlocking{crmcontracts.ExportMissing})
	}
	return nil
}

// resumeOrCreateRun continues an interrupted flip run for the SAME
// sealed snapshot (checkpoint intact — never from zero, never past it),
// or records a fresh one.
func (f *flipRunner) resumeOrCreateRun(ctx context.Context, snap overlay.FlipSnapshot, mode string) (migration.Run, error) {
	latest, err := f.runs.Latest(ctx, migration.ConnectorMirror)
	switch {
	case err == nil && latest.SourceRef == snap.ID && latest.Status == migration.StatusFailed:
		if err := f.runs.Resume(ctx, latest.ID); err != nil {
			return migration.Run{}, err
		}
		return f.runs.Get(ctx, latest.ID)
	case err == nil && latest.SourceRef == snap.ID && latest.Status == migration.StatusRunning:
		// A crashed request left the run marked running; re-enter it.
		return latest, nil
	case err != nil && !errors.Is(err, apperrors.ErrNotFound):
		return migration.Run{}, err
	}
	return f.runs.Create(ctx, migration.CreateRunInput{
		Connector: migration.ConnectorMirror,
		SourceRef: snap.ID,
		Source:    "overlay:flip:" + mode,
	})
}

// flipBlocked is the ErrOverlayFlipBlocked producer: the 409's detail
// names every unsatisfied gate.
func flipBlocked(blocking []crmcontracts.OverlayFlipPreflightBlocking) error {
	reasons := make([]string, 0, len(blocking))
	for _, b := range blocking {
		reasons = append(reasons, string(b))
	}
	return fmt.Errorf("the flip preflight is unsatisfied: %s: %w", strings.Join(reasons, ", "), apperrors.ErrOverlayFlipBlocked)
}

func blockingContains(blocking []crmcontracts.OverlayFlipPreflightBlocking, want crmcontracts.OverlayFlipPreflightBlocking) bool {
	for _, b := range blocking {
		if b == want {
			return true
		}
	}
	return false
}

// emergencyDisclosure is the 202's disclosed-lossy block: what the
// operator is accepting when they cut over from a stale mirror.
func emergencyDisclosure(lastSynced time.Time) *struct {
	LastSyncedAt             *time.Time `json:"last_synced_at"`
	StalenessSeconds         *int64     `json:"staleness_seconds,omitempty"`
	UnverifiableParityNotice string     `json:"unverifiable_parity_notice"`
} {
	out := &struct {
		LastSyncedAt             *time.Time `json:"last_synced_at"`
		StalenessSeconds         *int64     `json:"staleness_seconds,omitempty"`
		UnverifiableParityNotice string     `json:"unverifiable_parity_notice"`
	}{UnverifiableParityNotice: flipUnverifiableParityNotice}
	out.LastSyncedAt, out.StalenessSeconds = staleness(lastSynced)
	return out
}

// staleness renders a watermark as the disclosure pair both emergency
// blocks carry: how old the snapshot is, or nothing at all when the
// mirror never synced (never a fabricated zero).
func staleness(lastSynced time.Time) (*time.Time, *int64) {
	if lastSynced.IsZero() {
		return nil, nil
	}
	last := lastSynced
	seconds := int64(time.Since(last) / time.Second)
	return &last, &seconds
}

// wireEmergency builds the preflight's emergency block (OVA-AC-6 b):
// offered only while the incumbent is unreachable, and only when a
// mirror exists to cut over from.
func wireEmergency(checks overlay.FlipChecks) *struct {
	Available                bool       `json:"available"`
	LastSyncedAt             *time.Time `json:"last_synced_at"`
	StalenessSeconds         *int64     `json:"staleness_seconds,omitempty"`
	UnverifiableParityNotice string     `json:"unverifiable_parity_notice"`
} {
	out := &struct {
		Available                bool       `json:"available"`
		LastSyncedAt             *time.Time `json:"last_synced_at"`
		StalenessSeconds         *int64     `json:"staleness_seconds,omitempty"`
		UnverifiableParityNotice string     `json:"unverifiable_parity_notice"`
	}{
		Available:                checks.MirrorRows > 0,
		UnverifiableParityNotice: flipUnverifiableParityNotice,
	}
	out.LastSyncedAt, out.StalenessSeconds = staleness(checks.LastSyncedAt)
	return out
}
