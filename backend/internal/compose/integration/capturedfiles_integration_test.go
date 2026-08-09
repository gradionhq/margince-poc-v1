// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A captured message's files, over a real database and a real object store.
//
// In compose because the write crosses two modules: capture drives the
// transaction and the timeline store owns the attachment table, joined by the
// keeper compose injects. Proving it inside either module would prove one half.
//
// Two things can only be shown here. Idempotency is a unique index, so a test
// that supplies its own rows proves nothing about it — the second pull has to
// meet the first one's row. And the account roll-up is read from the activity's
// own link, written in the same transaction, so it cannot be staged by hand
// either.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// captureWorkspace seeds a workspace and returns a context bound to it under
// the per-user mail connector principal the sync loop mints.
func captureWorkspace(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	pool := SchemaPool(t)
	ws := ids.NewV7()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Captured Files', $2, 'EUR')`,
		ws, "captured-files-"+ws.String()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   "connector:imap",
		Permissions: principal.Permissions{
			RoleKeys: []string{"capture"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true},
				"person":   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	// The tag makes every source id unique to this run. The test database is
	// shared and long-lived, so a fixed id would meet rows an earlier run of
	// this same suite left behind and count them as this run's.
	return principal.WithCorrelationID(ctx, ids.NewV7()), pool, ws.String()
}

// mailRecord is one captured mail record, shaped the way mailmap produces one.
func mailRecord(sourceID string) connector.NormalizedRecord {
	const counterparty = "her@example.com"
	return connector.NormalizedRecord{
		EntityType: "activity",
		NaturalKey: connector.NaturalKey{SourceSystem: "imap", SourceID: sourceID},
		Fields: capture.ActivityFields{
			Kind:      "email",
			Subject:   "The signed contract",
			Body:      "Attached, as promised.",
			Direction: connector.DirectionInbound,
		},
		Source:     "imap:" + sourceID,
		CapturedBy: "connector:imap",
		Raw:        []byte("From: " + counterparty + "\r\n\r\nBody."),
		Counterparty: connector.Counterparty{
			Email:     counterparty,
			Domain:    "example.com",
			Direction: connector.DirectionInbound,
		},
	}
}

// capturedFile is what the attachment row says about one file that arrived.
type capturedFile struct {
	filename     string
	contentType  *string
	declaredType *string
	category     string
	storageKey   string
	byteSize     int64
	partID       *string
	sourceID     *string
	organization *string
}

func withFiles(rec connector.NormalizedRecord, parts ...connector.Part) connector.NormalizedRecord {
	rec.Parts = parts
	return rec
}

func onePDF() connector.Part {
	return connector.Part{
		Ordinal:      1,
		Filename:     "contract.pdf",
		ContentType:  "application/pdf",
		DeclaredType: "application/octet-stream",
		Body:         []byte("%PDF-1.4 signed"),
	}
}

func filesFor(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sourceID string) []capturedFile {
	t.Helper()
	var out []capturedFile
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT filename, content_type, declared_type, category, storage_key,
			       byte_size, external_part_id, external_source_id, organization_id::text
			  FROM attachment
			 WHERE external_source_id = $1
			 ORDER BY external_part_id`, "imap:"+sourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f capturedFile
			if err := rows.Scan(&f.filename, &f.contentType, &f.declaredType, &f.category,
				&f.storageKey, &f.byteSize, &f.partID, &f.sourceID, &f.organization); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read captured files: %v", err)
	}
	return out
}

func TestACapturedMessagesFileBecomesAnAttachment(t *testing.T) {
	ctx, pool, tag := captureWorkspace(t)
	blob := blobstore.NewMemory()
	sink := capture.NewSink(pool).WithFileKeeper(fileKeeper(pool, blob))

	rec := withFiles(mailRecord("msg-with-file-"+tag), onePDF())
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}

	files := filesFor(ctx, t, pool, "msg-with-file-"+tag)
	if len(files) != 1 {
		t.Fatalf("stored %d files, want 1", len(files))
	}
	got := files[0]
	if got.filename != "contract.pdf" {
		t.Errorf("filename = %q", got.filename)
	}
	if got.category != "email_attachment" {
		t.Errorf("category = %q, want email_attachment — this is how it arrived", got.category)
	}
	if got.byteSize != int64(len(onePDF().Body)) {
		t.Errorf("byte_size = %d, want the part's own length", got.byteSize)
	}
	if got.declaredType == nil || *got.declaredType != "application/octet-stream" {
		t.Errorf("declared_type = %v, want the sender's disagreeing claim kept", got.declaredType)
	}
	// The stored identity NAMES THE ADAPTER. A bare Message-ID is not unique
	// across adapters, so the same mailbox pulled by two of them would collide
	// on the unique index and the second file would be dropped in silence.
	if got.sourceID == nil || *got.sourceID != "imap:msg-with-file-"+tag {
		t.Errorf("external_source_id = %v, want the adapter named alongside the message", got.sourceID)
	}
	// And the bytes are actually there. A row pointing at an object that was
	// never written is exactly the failure the blob-before-row order exists to
	// prevent, and only reading it back can tell the two apart.
	body, _, err := blob.Get(ctx, got.storageKey)
	if err != nil {
		t.Fatalf("the stored row points at bytes the object store does not have: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("closing the stored object: %v", err)
	}
}

// DOC-AC-8: the same provider message pulled twice produces one row per part.
func TestTheSameMessagePulledTwiceStoresItsFileOnce(t *testing.T) {
	ctx, pool, tag := captureWorkspace(t)
	sink := capture.NewSink(pool).WithFileKeeper(fileKeeper(pool, blobstore.NewMemory()))

	rec := withFiles(mailRecord("msg-pulled-twice-"+tag), onePDF())
	for pull := 1; pull <= 2; pull++ {
		if _, err := sink.Upsert(ctx, rec); err != nil {
			t.Fatalf("pull %d: %v", pull, err)
		}
	}

	if files := filesFor(ctx, t, pool, "msg-pulled-twice-"+tag); len(files) != 1 {
		t.Errorf("stored %d files after two pulls of one message, want 1", len(files))
	}
}

// The test above passes on the MESSAGE's natural key: the second pull finds the
// activity already there and writes nothing further. That is the ordinary path
// and worth keeping, but it means it would still pass if the part-level
// guarantee were removed — so the guarantee gets its own proof here, against
// the database rather than against the code that usually avoids it.
//
// This is what holds when two pulls of one mailbox overlap in time and both
// reach the insert.
func TestTheDatabaseRefusesASecondRowForTheSameProviderPart(t *testing.T) {
	ctx, pool, tag := captureWorkspace(t)
	sink := capture.NewSink(pool).WithFileKeeper(fileKeeper(pool, blobstore.NewMemory()))

	rec := withFiles(mailRecord("msg-racing-pulls-"+tag), onePDF())
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}
	stored := filesFor(ctx, t, pool, "msg-racing-pulls-"+tag)
	if len(stored) != 1 {
		t.Fatalf("stored %d files, want 1 before the duplicate is attempted", len(stored))
	}

	// The same (message, part) identity, written as a concurrent pull would.
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO attachment (
				workspace_id, entity_type, entity_id, filename, storage_key,
				source, captured_by, external_source_id, external_part_id)
			VALUES (current_setting('app.workspace_id')::uuid, 'activity', $1, 'contract.pdf',
			        'some/other/key', 'imap', 'connector:imap', $2, $3)`,
			activityOf(ctx, t, pool, "imap:msg-racing-pulls-"+tag),
			"imap:msg-racing-pulls-"+tag, *stored[0].partID)
		return err
	})
	if err == nil {
		t.Fatal("a second row for the same provider part was accepted — the part identity is not unique")
	}

	if files := filesFor(ctx, t, pool, "msg-racing-pulls-"+tag); len(files) != 1 {
		t.Errorf("stored %d files, want the one the refused duplicate did not add", len(files))
	}
}

// A deployment with no object store keeps the correspondence and no files.
// Refusing the message would lose a real exchange over an operator's omission.
func TestWithNoObjectStoreTheMessageLandsAndItsFilesDoNot(t *testing.T) {
	ctx, pool, tag := captureWorkspace(t)
	sink := capture.NewSink(pool)

	rec := withFiles(mailRecord("msg-no-store-"+tag), onePDF())
	ref, err := sink.Upsert(ctx, rec)
	if err != nil {
		t.Fatalf("capture with no blob seam: %v", err)
	}
	if ref.ID == (ids.UUID{}) {
		t.Fatal("the message itself was not captured")
	}
	if files := filesFor(ctx, t, pool, "msg-no-store-"+tag); len(files) != 0 {
		t.Errorf("stored %d files with no object store configured, want 0", len(files))
	}
}

// DOC-AC-12: what the bounds refused is observable, so a message whose files
// were dropped is not silently identical to one that carried none.
func TestARefusedFileLeavesAnObservableReason(t *testing.T) {
	ctx, pool, tag := captureWorkspace(t)
	sink := capture.NewSink(pool).WithFileKeeper(fileKeeper(pool, blobstore.NewMemory()))

	rec := mailRecord("msg-dropped-file-" + tag)
	rec.PartDrops = []connector.PartDrop{{Reason: "part_too_large", Count: 3}}
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}

	var logged int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM system_log
			 WHERE action = 'capture_parts_dropped'
			   AND detail->>'source_id' = $1
			   AND detail->>'reason' = 'part_too_large'`, "msg-dropped-file-"+tag).Scan(&logged)
	}); err != nil {
		t.Fatalf("read the drop breadcrumb: %v", err)
	}
	// ONE breadcrumb for three refused files. The tally is what makes an
	// inbound message unable to size our own log.
	if logged != 1 {
		t.Errorf("found %d drop breadcrumbs, want 1 — one per reason, whatever the count", logged)
	}
}

// fileKeeper is the same join compose makes: capture drives the transaction,
// the timeline store owns the attachment table. Spelled here rather than
// reaching for the compose adapter, which this module may not import.
type fileKeeperAdapter struct{ store *activities.Store }

func fileKeeper(pool *pgxpool.Pool, blob blobstore.Store) capture.FileKeeper {
	return fileKeeperAdapter{store: activities.NewStore(pool).WithBlobstore(blob)}
}

func (k fileKeeperAdapter) Stage(
	ctx context.Context, files []capture.CapturedFile,
) ([]capture.StagedFile, error) {
	owned := make([]activities.CapturedFile, 0, len(files))
	for _, file := range files {
		owned = append(owned, activities.CapturedFile{
			PartID: file.PartID, Filename: file.Filename,
			ContentType: file.ContentType, DeclaredType: file.DeclaredType, Body: file.Body,
		})
	}
	staged, err := k.store.StageCapturedFiles(ctx, owned)
	if err != nil {
		return nil, err
	}
	out := make([]capture.StagedFile, 0, len(staged))
	for _, file := range staged {
		out = append(out, file)
	}
	return out, nil
}

func (k fileKeeperAdapter) Record(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
	from capture.FileSource, staged []capture.StagedFile,
) error {
	owned := make([]activities.StagedFile, 0, len(staged))
	for _, file := range staged {
		typed, ok := file.(activities.StagedFile)
		if !ok {
			return errNotStagedHere
		}
		owned = append(owned, typed)
	}
	return k.store.RecordCapturedFiles(ctx, tx, activityID, activities.CapturedFileSource{
		System: from.System, MessageID: from.MessageID, CapturedBy: from.CapturedBy,
	}, owned)
}

var errNotStagedHere = errors.New("a captured file was staged by something else")

// activityOf reads the activity a captured file hangs off, so a duplicate can
// be written against the same parent the real one has.
func activityOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sourceKey string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT entity_id FROM attachment WHERE external_source_id = $1 LIMIT 1`,
			sourceKey).Scan(&id)
	}); err != nil {
		t.Fatalf("read the captured file's activity: %v", err)
	}
	return id
}
