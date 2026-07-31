// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The engine's source kinds, as stored in import_run.connector. The
// migrate-in connectors (csv/hubspot/salesforce) are in the column's
// CHECK — the DDL is the chapter's pinned arrival shape — but they get
// their Go constants when their connectors land (UC-E11-03), not before.
const (
	// ConnectorMirror is the overlay→native flip: the frozen mirror snapshot.
	ConnectorMirror = "mirror"
	// ConnectorBundle is reconstruction from a pre-flip export bundle.
	ConnectorBundle = "bundle"
)

// pageSize bounds one Source.Rows read: large enough to amortize the
// round-trip, small enough that a resumed run re-reads at most one page.
const pageSize = 200

// Row is one source record: the incumbent/external id, the canonical
// field map (keys are native column names — the mirror ingest projector
// already speaks this shape), and the record's last sync instant.
type Row struct {
	ExternalID string
	Fields     map[string]any
	// OwnerExternalID is the source's owner id, kept OUT of Fields on
	// purpose: Fields is the canonical payload and its emptiness is what
	// the empty_payload skip reads. Carrying transport metadata in there
	// would make every owned-but-blank system entry look like a record
	// worth creating, and the writer would land a nameless native row.
	OwnerExternalID string
	LastSyncedAt    time.Time
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
	Created bool
	// Unchanged marks a row that already landed under this provenance
	// and was NOT rewritten — a resumed run replaying its last page, or
	// a re-run over the same frozen source. It is neither a create nor
	// an update: counting it as either would inflate the disposition
	// table with work that never happened.
	Unchanged  bool
	Skipped    bool
	SkipReason string
	// Disclosure names a lossy-but-disclosed mapping decision (e.g. a
	// deal materialized onto the default pipeline because the source
	// stage identity did not resolve).
	Disclosure string
}

// AssocResult reports what one Writers.Associate did — an edge whose
// endpoint never landed, or whose shape the native model has no target
// for, is DISCLOSED here rather than vanishing into a bare nil.
type AssocResult struct {
	Applied bool
	// Reason explains a non-applied edge; empty when Applied.
	Reason string
}

// Writers is the native-record seam: compose implements it over the
// people/deals/activities stores so this module never imports a sibling.
// Every method must be idempotent on the row's provenance key — the
// checkpointed run loop may replay the row after a crash, and a re-run
// of the whole source must converge (IEM-FORM-1's upsert-by-key).
type Writers interface {
	Exists(ctx context.Context, object, externalID string) (bool, error)
	Ensure(ctx context.Context, object string, row Row) (EnsureResult, error)
	Associate(ctx context.Context, a Assoc) (AssocResult, error)
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
	Object      string `json:"object"`
	MirrorCount int    `json:"mirror_count"`
	WillCreate  int    `json:"will_create"`
	WillUpdate  int    `json:"will_update"`
	Created     int    `json:"created"`
	Updated     int    `json:"updated"`
	// Unchanged counts rows already landed under their provenance that
	// this attempt did not rewrite (a resumed run's replayed page).
	Unchanged   int          `json:"unchanged,omitempty"`
	Skipped     []SkippedRow `json:"skipped,omitempty"`
	Disclosures []string     `json:"disclosures,omitempty"`
}

// Report is the run (or dry-run) outcome: per-object dispositions plus
// the association tally. The disposition table is the honest-disclosure
// surface — nothing is dropped without a line here.
type Report struct {
	Objects []ObjectReport `json:"objects"`
	// Associations counts edges APPLIED, not edges read; AssociationsSkipped
	// carries the rest with their reasons.
	Associations        int            `json:"associations"`
	AssociationsSkipped []SkippedAssoc `json:"associations_skipped,omitempty"`
	Imported            int64          `json:"imported"`
}

// SkippedAssoc is one edge the import did not materialize, disclosed.
type SkippedAssoc struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// runRecords is what the loop needs from the run store — an interface so
// the loop's checkpoint/resume contract is provable without Postgres.
type runRecords interface {
	Get(ctx context.Context, id RunID) (Run, error)
	advanceCheckpoint(ctx context.Context, id RunID, checkpoint int) error
	complete(ctx context.Context, id RunID, rep Report) error
	failRun(ctx context.Context, id RunID, rep Report, cause error) error
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
	// The dry-run writes nothing, so it cannot resolve endpoints: this is
	// the count of edges the source OFFERS, and the run's own report is
	// what says how many were applied.
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
		return Report{}, e.fail(ctx, runID, Report{}, fmt.Errorf("migration run: counting source rows: %w", err))
	}

	rep := Report{}
	done := run.Checkpoint // rows already processed across the ordered objects
	seen := 0              // global index of the row about to be processed
	for _, object := range src.Objects() {
		or, advanced, err := e.importObject(ctx, runID, src, object, counts[object], done, seen, &rep)
		if err != nil {
			return Report{}, err
		}
		rep.Objects = append(rep.Objects, or)
		rep.Imported += int64(or.Created + or.Updated)
		// Only the global cursor advances. `done` is the checkpoint this
		// attempt STARTED from and must stay put: reassigning it would
		// make the next class compute a zero local offset and re-walk
		// rows the run already landed, which the store's monotonic
		// cursor then refuses — wedging every retry of a crashed flip.
		seen = advanced
	}

	assocs, err := src.Associations(ctx)
	if err != nil {
		return Report{}, e.fail(ctx, runID, rep, fmt.Errorf("migration run: reading associations: %w", err))
	}
	for _, a := range assocs {
		res, err := e.w.Associate(ctx, a)
		if err != nil {
			return Report{}, e.fail(ctx, runID, rep, fmt.Errorf("migration run: applying association %s/%s→%s/%s: %w", a.FromType, a.FromID, a.ToType, a.ToID, err))
		}
		if res.Applied {
			rep.Associations++
			continue
		}
		rep.AssociationsSkipped = append(rep.AssociationsSkipped, SkippedAssoc{
			From:   a.FromType + "/" + a.FromID,
			To:     a.ToType + "/" + a.ToID,
			Reason: res.Reason,
		})
	}

	if err := e.runs.complete(ctx, runID, rep); err != nil {
		return Report{}, e.fail(ctx, runID, rep, fmt.Errorf("migration run: recording completion: %w", err))
	}
	return rep, nil
}

// importObject runs one object class's rows, resuming past the prefix
// an earlier attempt already landed. It returns the class's disposition
// and the new global cursor — the checkpoint advances after every row,
// so a kill mid-class resumes from the next one.
func (e *Engine) importObject(ctx context.Context, runID RunID, src Source, object string, total, done, seen int, rep *Report) (ObjectReport, int, error) {
	or := ObjectReport{Object: object, MirrorCount: total}
	// A whole already-processed class is skipped without re-reading it.
	if done >= seen+total {
		return or, seen + total, nil
	}
	localOffset := max(done-seen, 0)
	cursor := seen + localOffset
	for offset := localOffset; ; {
		rows, err := src.Rows(ctx, object, offset, pageSize)
		if err != nil {
			return ObjectReport{}, 0, e.fail(ctx, runID, withPartial(*rep, or), fmt.Errorf("migration run: reading %s rows at %d: %w", object, offset, err))
		}
		if len(rows) == 0 {
			return or, cursor, nil
		}
		for _, row := range rows {
			res, err := e.ensureRow(ctx, object, row)
			if err != nil {
				return ObjectReport{}, 0, e.fail(ctx, runID, withPartial(*rep, or), err)
			}
			or.record(row.ExternalID, res)
			cursor++
			if err := e.runs.advanceCheckpoint(ctx, runID, cursor); err != nil {
				return ObjectReport{}, 0, e.fail(ctx, runID, withPartial(*rep, or), err)
			}
		}
		offset += len(rows)
		if len(rows) < pageSize {
			return or, cursor, nil
		}
	}
}

// record folds one row's outcome into the class's disposition.
func (or *ObjectReport) record(externalID string, res EnsureResult) {
	switch {
	case res.Skipped:
		or.Skipped = append(or.Skipped, SkippedRow{ExternalID: externalID, Reason: res.SkipReason})
	case res.Unchanged:
		or.Unchanged++
	case res.Created:
		or.Created++
	default:
		or.Updated++
	}
	if res.Disclosure != "" {
		or.Disclosures = append(or.Disclosures, res.Disclosure)
	}
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

// fail records the run as failed — with the dispositions this attempt
// managed before it stopped — and returns the original error joined with
// any record-keeping failure, so the caller always sees why the run
// stopped. The partial report matters because a resumed run only ever
// reports its own attempt: without persisting this one, every record
// landed before the crash vanishes from the operator's final count.
func (e *Engine) fail(ctx context.Context, runID RunID, rep Report, cause error) error {
	if ferr := e.runs.failRun(ctx, runID, rep, cause); ferr != nil {
		return fmt.Errorf("%w (and recording the failure failed: %v)", cause, ferr)
	}
	return cause
}

// withPartial is the report as it stands plus the class currently being
// imported — what an attempt has actually landed at the moment it dies.
// The object is appended rather than merged: the run loop only appends a
// class once it finishes, so a crashed class has no entry yet.
func withPartial(rep Report, or ObjectReport) Report {
	rep.Objects = append(append([]ObjectReport(nil), rep.Objects...), or)
	rep.Imported += int64(or.Created + or.Updated)
	return rep
}

// mergedWith folds an earlier attempt's dispositions into this one's, so
// a run resumed across several crashes still reports the whole estate it
// imported rather than only its final leg. Counts add and per-object
// entries fold by class; the checkpoint guarantees no row is walked
// twice, so addition cannot double-count.
func (r Report) mergedWith(next Report) Report {
	out := Report{
		Associations:        r.Associations + next.Associations,
		AssociationsSkipped: append(append([]SkippedAssoc(nil), r.AssociationsSkipped...), next.AssociationsSkipped...),
		Imported:            r.Imported + next.Imported,
	}
	at := map[string]int{}
	for _, or := range append(append([]ObjectReport(nil), r.Objects...), next.Objects...) {
		i, seen := at[or.Object]
		if !seen {
			at[or.Object] = len(out.Objects)
			out.Objects = append(out.Objects, or)
			continue
		}
		into := &out.Objects[i]
		into.Created += or.Created
		into.Updated += or.Updated
		into.Unchanged += or.Unchanged
		into.WillCreate += or.WillCreate
		into.WillUpdate += or.WillUpdate
		// MirrorCount is the source's size, not a tally: both attempts saw
		// the same frozen snapshot, so the later read stands.
		into.MirrorCount = or.MirrorCount
		into.Skipped = append(into.Skipped, or.Skipped...)
		into.Disclosures = append(into.Disclosures, or.Disclosures...)
	}
	return out
}
