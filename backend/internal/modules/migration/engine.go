// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// Connector names the engine's source kinds — the import_run.connector
// CHECK mirrors this set. Only mirror and bundle have engine callers
// today; the three migrate-in connectors are reserved for their own
// tickets (UC-E11-03).
const (
	ConnectorMirror     = "mirror"
	ConnectorBundle     = "bundle"
	ConnectorHubSpot    = "hubspot"
	ConnectorSalesforce = "salesforce"
	ConnectorCSV        = "csv"
)

// pageSize bounds one Source.Rows read: large enough to amortize the
// round-trip, small enough that a resumed run re-reads at most one page.
const pageSize = 200

// Row is one source record: the incumbent/external id, the canonical
// field map (keys are native column names — the mirror ingest projector
// already speaks this shape), and the record's last sync instant.
type Row struct {
	ExternalID   string
	Fields       map[string]any
	LastSyncedAt time.Time
}

// Assoc is one detangled source edge, applied after both endpoints
// landed (IEM-FORM-2: edges become FKs or typed relationship rows —
// the Writers implementation owns that mapping).
type Assoc struct {
	FromType string
	FromID   string
	ToType   string
	ToID     string
	Category string
	Label    string
}

// Source is one estate to import. Objects fixes the import order
// (parents before dependents); Rows pages a stable, deterministic
// ordering — the checkpoint's resume contract depends on it.
type Source interface {
	Objects() []string
	Counts(ctx context.Context) (map[string]int, error)
	Rows(ctx context.Context, object string, offset, limit int) ([]Row, error)
	Associations(ctx context.Context) ([]Assoc, error)
}

// EnsureResult reports what one Writers.Ensure did. Skips and
// disclosures are never silent — both land in the run report
// (AC-mode-flip-7: skipped rows carry reasons).
type EnsureResult struct {
	Created    bool
	Skipped    bool
	SkipReason string
	// Disclosure names a lossy-but-disclosed mapping decision (e.g. a
	// deal materialized onto the default pipeline because the source
	// stage identity did not resolve).
	Disclosure string
}

// Writers is the native-record seam: compose implements it over the
// people/deals/activities stores so this module never imports a sibling.
// Every method must be idempotent on the row's provenance key — the
// checkpointed run loop may replay the row after a crash, and a re-run
// of the whole source must converge (IEM-FORM-1's upsert-by-key).
type Writers interface {
	Exists(ctx context.Context, object, externalID string) (bool, error)
	Ensure(ctx context.Context, object string, row Row) (EnsureResult, error)
	Associate(ctx context.Context, a Assoc) error
}

// SkippedRow is one disclosed skip in the run report.
type SkippedRow struct {
	ExternalID string `json:"external_id"`
	Reason     string `json:"reason"`
}

// skipReasonEmptyPayload marks a source row with no fields at all — the
// "payload-less system entries" class the parity preview must disclose
// rather than silently drop (UC-E18-04 E2).
const skipReasonEmptyPayload = "empty_payload"

// ObjectReport is one object class's slice of the run/dry-run report.
type ObjectReport struct {
	Object      string       `json:"object"`
	MirrorCount int          `json:"mirror_count"`
	WillCreate  int          `json:"will_create"`
	WillUpdate  int          `json:"will_update"`
	Created     int          `json:"created"`
	Updated     int          `json:"updated"`
	Skipped     []SkippedRow `json:"skipped,omitempty"`
	Disclosures []string     `json:"disclosures,omitempty"`
}

// Report is the run (or dry-run) outcome: per-object dispositions plus
// the association total. The disposition table is the honest-disclosure
// surface — nothing is dropped without a line here.
type Report struct {
	Objects      []ObjectReport `json:"objects"`
	Associations int            `json:"associations"`
	Imported     int64          `json:"imported"`
}

// runRecords is what the loop needs from the run store — an interface so
// the loop's checkpoint/resume contract is provable without Postgres.
type runRecords interface {
	Get(ctx context.Context, id RunID) (Run, error)
	advanceCheckpoint(ctx context.Context, id RunID, checkpoint int) error
	complete(ctx context.Context, id RunID, rep Report) error
	failRun(ctx context.Context, id RunID, cause error) error
}

// Engine runs one Source through the Writers seam. It owns
// classification and the checkpointed loop; it owns no SQL of its own
// beyond the RunStore's run records.
type Engine struct {
	runs runRecords
	w    Writers
}

// NewEngine wires the engine over its two seams.
func NewEngine(runs *RunStore, w Writers) *Engine {
	return &Engine{runs: runs, w: w}
}

// DryRun classifies every source row without writing one native record
// (AC-M5 / AC-mode-flip-7): per object it reports how many rows would
// create vs update, and which are skipped with reasons.
func (e *Engine) DryRun(ctx context.Context, src Source) (Report, error) {
	counts, err := src.Counts(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("migration dry-run: counting source rows: %w", err)
	}
	var rep Report
	for _, object := range src.Objects() {
		or := ObjectReport{Object: object, MirrorCount: counts[object]}
		for offset := 0; ; offset += pageSize {
			rows, err := src.Rows(ctx, object, offset, pageSize)
			if err != nil {
				return Report{}, fmt.Errorf("migration dry-run: reading %s rows at %d: %w", object, offset, err)
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if len(row.Fields) == 0 {
					or.Skipped = append(or.Skipped, SkippedRow{ExternalID: row.ExternalID, Reason: skipReasonEmptyPayload})
					continue
				}
				exists, err := e.w.Exists(ctx, object, row.ExternalID)
				if err != nil {
					return Report{}, fmt.Errorf("migration dry-run: classifying %s %s: %w", object, row.ExternalID, err)
				}
				if exists {
					or.WillUpdate++
				} else {
					or.WillCreate++
				}
			}
			if len(rows) < pageSize {
				break
			}
		}
		rep.Objects = append(rep.Objects, or)
	}
	assocs, err := src.Associations(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("migration dry-run: reading associations: %w", err)
	}
	rep.Associations = len(assocs)
	return rep, nil
}

// Run executes the import for an already-created run record, resuming
// from its checkpoint: rows are processed in the Source's stable order,
// and the checkpoint advances after every upsert (IEM-FORM-1), so a
// killed run re-entered with the same run id converges on the identical
// end state as an uninterrupted one. The association phase follows the
// row phase and is idempotent as a whole; the run completes (with its
// report persisted) or fails with the error recorded.
func (e *Engine) Run(ctx context.Context, runID RunID, src Source) (Report, error) {
	run, err := e.runs.Get(ctx, runID)
	if err != nil {
		return Report{}, err
	}
	if run.Status != StatusRunning {
		return Report{}, fmt.Errorf("migration run %s is %s, not %s: %w", runID, run.Status, StatusRunning, apperrors.ErrConflict)
	}
	counts, err := src.Counts(ctx)
	if err != nil {
		return Report{}, e.fail(ctx, runID, fmt.Errorf("migration run: counting source rows: %w", err))
	}

	rep := Report{}
	done := run.Checkpoint // rows already processed across the ordered objects
	seen := 0              // global index of the row about to be processed
	for _, object := range src.Objects() {
		or := ObjectReport{Object: object, MirrorCount: counts[object]}
		total := counts[object]
		// Skip whole already-processed prefixes without re-reading them —
		// a resumed run's report counts this attempt's work only; the
		// converged end state is what the parity assertions read.
		if done >= seen+total {
			seen += total
			rep.Objects = append(rep.Objects, or)
			continue
		}
		localOffset := max(done-seen, 0)
		seen += localOffset
		for offset := localOffset; ; {
			rows, err := src.Rows(ctx, object, offset, pageSize)
			if err != nil {
				return Report{}, e.fail(ctx, runID, fmt.Errorf("migration run: reading %s rows at %d: %w", object, offset, err))
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				res, err := e.ensureRow(ctx, object, row)
				if err != nil {
					return Report{}, e.fail(ctx, runID, err)
				}
				switch {
				case res.Skipped:
					or.Skipped = append(or.Skipped, SkippedRow{ExternalID: row.ExternalID, Reason: res.SkipReason})
				case res.Created:
					or.Created++
					rep.Imported++
				default:
					or.Updated++
					rep.Imported++
				}
				if res.Disclosure != "" {
					or.Disclosures = append(or.Disclosures, res.Disclosure)
				}
				seen++
				done = seen
				if err := e.runs.advanceCheckpoint(ctx, runID, done); err != nil {
					return Report{}, e.fail(ctx, runID, err)
				}
			}
			offset += len(rows)
			if len(rows) < pageSize {
				break
			}
		}
		rep.Objects = append(rep.Objects, or)
	}

	assocs, err := src.Associations(ctx)
	if err != nil {
		return Report{}, e.fail(ctx, runID, fmt.Errorf("migration run: reading associations: %w", err))
	}
	for _, a := range assocs {
		if err := e.w.Associate(ctx, a); err != nil {
			return Report{}, e.fail(ctx, runID, fmt.Errorf("migration run: applying association %s/%s→%s/%s: %w", a.FromType, a.FromID, a.ToType, a.ToID, err))
		}
	}
	rep.Associations = len(assocs)

	if err := e.runs.complete(ctx, runID, rep); err != nil {
		return Report{}, err
	}
	return rep, nil
}

func (e *Engine) ensureRow(ctx context.Context, object string, row Row) (EnsureResult, error) {
	if len(row.Fields) == 0 {
		return EnsureResult{Skipped: true, SkipReason: skipReasonEmptyPayload}, nil
	}
	res, err := e.w.Ensure(ctx, object, row)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("migration run: importing %s %s: %w", object, row.ExternalID, err)
	}
	return res, nil
}

// fail records the run as failed and returns the original error joined
// with any record-keeping failure — the caller always sees why the run
// stopped.
func (e *Engine) fail(ctx context.Context, runID RunID, cause error) error {
	if ferr := e.runs.failRun(ctx, runID, cause); ferr != nil {
		return fmt.Errorf("%w (and recording the failure failed: %v)", cause, ferr)
	}
	return cause
}
