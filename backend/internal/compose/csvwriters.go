// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// csvWriters implements migration.Writers for a delimited upload.
//
// It is NOT the flip's writers with a different source, and the difference is
// the whole reason this type exists. flipWriters answers "already landed" with
// Unchanged and writes nothing, which is correct there because its source is a
// FROZEN snapshot — a re-imported row cannot carry different values than the
// row that already landed. **An uploaded file is editable.** The customer
// fixes a column and uploads the corrected file, and that same shortcut would
// report "unchanged" and write nothing, silently. So a match here compares the
// mapped fields and updates the ones that differ.
type csvWriters struct {
	pool       *pgxpool.Pool
	people     *people.Store
	identities *migration.RunStore
	runID      migration.RunID
	object     string

	// nativeIDs caches external key → native id within one run. A resumed run
	// rebuilds it lazily through lookup, which falls back to the engine-owned
	// identity map.
	nativeIDs map[string]ids.UUID
	// updated counts the rows this run rewrote. The engine's EnsureResult has
	// no "updated" member — the frozen-source model it was built for had no
	// such outcome — so the count rides here and the report reads it.
	updated int
}

var _ migration.Writers = (*csvWriters)(nil)

func newCSVWriters(db *database.DB, runID migration.RunID, object string) *csvWriters {
	return &csvWriters{
		pool:       db.Pool(),
		people:     people.NewStore(db),
		identities: migration.NewRunStore(db),
		runID:      runID,
		object:     object,
		nativeIDs:  map[string]ids.UUID{},
	}
}

// csvSourceSystem namespaces the source_system imported rows carry. The
// reserved prefix is refused at the wire mappers, so a client cannot pre-plant
// a row under a guessed import id and have the store hand it back as already
// existing; the engine-owned identity map remains the authority for "already
// imported".
func csvSourceSystem() string {
	return provenance.ReservedSourceSystemPrefix + migration.ConnectorCSV
}

// provenanceOf is the imported row's source stamp: <source>:<object>:<id>, the
// UC-E11-03 convention every importer writes.
func (w *csvWriters) provenanceOf(externalID string) string {
	return fmt.Sprintf("%s%s:%s:%s", provenance.ReservedSourceSystemPrefix, migration.ConnectorCSV, w.object, externalID)
}

// Updated reports how many rows this run rewrote, for the run report.
func (w *csvWriters) Updated() int { return w.updated }

// Exists answers whether this external id already landed, through the
// engine-owned identity map — never by reading a row's own provenance, which
// outside the reserved namespace is client-writable.
func (w *csvWriters) Exists(ctx context.Context, object, externalID string) (bool, error) {
	_, found, err := w.lookup(ctx, object, externalID)
	return found, err
}

// ReconcileIdentities has nothing to repair: every landing below commits the
// record and its identity row in ONE transaction, so a crash leaves neither
// rather than a record the resume cannot name. The seam documents exactly this
// answer for a writer that lands both together.
func (w *csvWriters) ReconcileIdentities(context.Context) error { return nil }

// Associate discloses rather than applies. A flat file carries no edges, so an
// edge reaching here came from somewhere this writer does not understand, and
// swallowing it as applied would report work that never happened.
func (w *csvWriters) Associate(_ context.Context, a migration.Assoc) (migration.AssocResult, error) {
	return migration.AssocResult{
		Applied: false,
		Reason:  fmt.Sprintf("a delimited import carries no edges; %s→%s was not applied", a.FromType, a.ToType),
	}, nil
}

func (w *csvWriters) lookup(ctx context.Context, object, externalID string) (ids.UUID, bool, error) {
	if object != w.object {
		return ids.UUID{}, false, fmt.Errorf("import: this run carries %q, not %q", w.object, object)
	}
	if id, ok := w.nativeIDs[externalID]; ok {
		return id, true, nil
	}
	id, found, err := w.identities.LookupIdentity(ctx, csvSourceSystem(), object, externalID)
	if err != nil {
		return ids.UUID{}, false, err
	}
	if found {
		w.nativeIDs[externalID] = id
	}
	return id, found, nil
}

// predictedOutcome is what a commit would do with one row, decided exactly the
// way Ensure decides it — same lookup, same comparison — so the dry run cannot
// promise something the commit then does differently.
type predictedOutcome int

const (
	predictCreate predictedOutcome = iota
	predictUpdate
	predictUnchanged
)

// Predict answers what Ensure would do, without writing.
func (w *csvWriters) Predict(ctx context.Context, row migration.Row) (predictedOutcome, error) {
	id, found, err := w.lookup(ctx, w.object, row.ExternalID)
	if err != nil {
		return predictCreate, err
	}
	if !found {
		return predictCreate, nil
	}
	current, err := w.read(ctx, id)
	if err != nil {
		return predictCreate, err
	}
	changed, err := changedFields(current, textFields(row.Fields))
	if err != nil {
		return predictCreate, err
	}
	if len(changed) == 0 {
		return predictUnchanged, nil
	}
	return predictUpdate, nil
}

// Ensure lands one row: created the first time, updated when the file has
// since changed, unchanged when it has not.
func (w *csvWriters) Ensure(ctx context.Context, object string, row migration.Row) (migration.EnsureResult, error) {
	id, found, err := w.lookup(ctx, object, row.ExternalID)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	if found {
		return w.reconcile(ctx, id, row)
	}
	switch object {
	case migration.ObjectLead:
		return w.createLead(ctx, row)
	case migration.ObjectOrganization:
		return w.createOrganization(ctx, row)
	default:
		return migration.EnsureResult{}, fmt.Errorf("import: %q is not an importable object", object)
	}
}

// reconcile brings an already-landed record up to what the file now says.
func (w *csvWriters) reconcile(ctx context.Context, id ids.UUID, row migration.Row) (migration.EnsureResult, error) {
	current, err := w.read(ctx, id)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	changed, err := changedFields(current, textFields(row.Fields))
	if err != nil {
		return migration.EnsureResult{}, err
	}
	if len(changed) == 0 {
		// Counted as neither a create nor an update: reporting work that never
		// happened would inflate both the disposition table and the audit log.
		return migration.EnsureResult{Unchanged: true}, nil
	}
	if err := w.apply(ctx, id, changed); err != nil {
		return migration.EnsureResult{}, err
	}
	w.updated++
	return migration.EnsureResult{Disclosure: fmt.Sprintf("row %s: %d field(s) rewritten from the file", row.ExternalID, len(changed))}, nil
}

// read answers the stored record as its own JSON — the surface changedFields
// compares against, and the same shape the contract serves for it.
func (w *csvWriters) read(ctx context.Context, id ids.UUID) ([]byte, error) {
	switch w.object {
	case migration.ObjectLead:
		lead, err := w.people.GetLead(ctx, ids.From[ids.LeadKind](id), storekit.LiveOnly)
		if err != nil {
			return nil, err
		}
		return encodeRecord(lead)
	case migration.ObjectOrganization:
		org, err := w.people.GetOrganization(ctx, ids.From[ids.OrganizationKind](id), storekit.LiveOnly)
		if err != nil {
			return nil, err
		}
		return encodeRecord(org)
	default:
		return nil, fmt.Errorf("import: %q is not an importable object", w.object)
	}
}

// encodeRecord renders one stored record as JSON. Generic so neither wire type
// is widened to an empty interface on the way through.
func encodeRecord[T crmcontracts.Lead | crmcontracts.Organization](record T) ([]byte, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("import: reading the stored record: %w", err)
	}
	return encoded, nil
}

func (w *csvWriters) apply(ctx context.Context, id ids.UUID, changed map[string]string) error {
	switch w.object {
	case migration.ObjectLead:
		_, err := w.people.UpdateLead(ctx, ids.From[ids.LeadKind](id), leadUpdateFrom(changed))
		return err
	case migration.ObjectOrganization:
		_, err := w.people.UpdateOrganization(ctx, ids.From[ids.OrganizationKind](id), organizationUpdateFrom(changed))
		return err
	default:
		return fmt.Errorf("import: %q is not an importable object", w.object)
	}
}

// errImportReplayed aborts a landing that wrote nothing: the store answered
// with a record that already existed under its natural key. Rolling back keeps
// the identity row out of the map — adopting a record this run did not create
// would make the next attempt resolve it as already-imported, turning a
// one-shot disclosure into none.
var errImportReplayed = errors.New("import: the record replayed under its natural key")

func (w *csvWriters) createLead(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	in := leadCreateFrom(textFields(row.Fields), csvSourceSystem(), row.ExternalID, w.provenanceOf(row.ExternalID))
	err := w.land(ctx, row.ExternalID, func(tx pgx.Tx) (ids.UUID, error) {
		lead, created, err := w.people.CreateLeadTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("import: creating lead %s: %w", row.ExternalID, err)
		}
		if !created {
			return ids.UUID{}, errImportReplayed
		}
		return ids.UUID(lead.Id), nil
	})
	if errors.Is(err, errImportReplayed) {
		return migration.EnsureResult{Skipped: true, SkipReason: skipReasonNaturalKeyTaken}, nil
	}
	if err != nil {
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true}, nil
}

func (w *csvWriters) createOrganization(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	in := organizationCreateFrom(textFields(row.Fields), w.provenanceOf(row.ExternalID))
	if in.DisplayName == "" {
		return migration.EnsureResult{Skipped: true, SkipReason: "the mapped display_name is empty, so the row names no company"}, nil
	}
	err := w.land(ctx, row.ExternalID, func(tx pgx.Tx) (ids.UUID, error) {
		org, err := w.people.CreateOrganizationTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("import: creating organization %s: %w", row.ExternalID, err)
		}
		return ids.UUID(org.Id), nil
	})
	if err != nil {
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true}, nil
}

// land commits one native record and its identity-map row in ONE transaction,
// then caches the binding — after the commit, never inside it: an entry for a
// landing that then rolled back would make this run's later pages resolve an id
// that does not exist, and lookup answers from the cache before it asks the map.
func (w *csvWriters) land(ctx context.Context, externalID string, create func(tx pgx.Tx) (ids.UUID, error)) error {
	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		var err error
		if id, err = create(tx); err != nil {
			return err
		}
		return w.identities.RecordIdentityTx(ctx, tx, w.runID, csvSourceSystem(), w.object, externalID, id)
	}); err != nil {
		return err
	}
	w.nativeIDs[externalID] = id
	return nil
}
