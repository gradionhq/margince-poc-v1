// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

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
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// captureWorkspace seeds a workspace and returns a context bound to it under
// the per-user mail connector principal the sync loop mints.
func captureWorkspace(t *testing.T) (context.Context, *database.DB, string) {
	t.Helper()
	pool := integration.SchemaPool(t)
	ws := ids.NewV7()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		ws, "captured-files-"+ws.String()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	ctx = asConnector(principal.WithWorkspaceID(ctx, ws), "connector:imap")
	// The tag makes every source id unique to this run. The test database is
	// shared and long-lived, so a fixed id would meet rows an earlier run of
	// this same suite left behind and count them as this run's.
	return principal.WithCorrelationID(ctx, ids.NewV7()), database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), ws.String()
}

// asConnector binds the capture principal one adapter's sync loop mints.
//
// It takes the id rather than fixing it because the sink REFUSES a record whose
// captured_by is not the acting connector — so a test that captures a mail
// message and a Telegram message has to act as each in turn, and a single
// hardcoded principal would only be able to prove one of them.
func asConnector(ctx context.Context, id string) context.Context {
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   id,
		Permissions: principal.Permissions{
			RoleKeys: []string{"capture"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true},
				"person":   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
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

func filesFor(ctx context.Context, t *testing.T, db *database.DB, sourceID string) []capturedFile {
	t.Helper()
	return filesFrom(ctx, t, db, "imap", sourceID)
}

// filesFrom reads the rows a given adapter's message produced. The stored
// identity names the ADAPTER as well as the message, so a reader has to say
// which one it means — and a channel capture and a mail capture landing under
// the same source id is exactly the collision that naming prevents.
func filesFrom(ctx context.Context, t *testing.T, db *database.DB, system, sourceID string) []capturedFile {
	t.Helper()
	var out []capturedFile
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT filename, content_type, declared_type, category, storage_key,
			       byte_size, external_part_id, external_source_id, organization_id::text
			  FROM attachment
			 WHERE external_source_id = $1
			 ORDER BY external_part_id`, system+":"+sourceID)
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
	ctx, db, tag := captureWorkspace(t)
	blob := blobstore.NewMemory()
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blob))

	rec := withFiles(mailRecord("msg-with-file-"+tag), onePDF())
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}

	files := filesFor(ctx, t, db, "msg-with-file-"+tag)
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
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))

	rec := withFiles(mailRecord("msg-pulled-twice-"+tag), onePDF())
	for pull := 1; pull <= 2; pull++ {
		if _, err := sink.Upsert(ctx, rec); err != nil {
			t.Fatalf("pull %d: %v", pull, err)
		}
	}

	if files := filesFor(ctx, t, db, "msg-pulled-twice-"+tag); len(files) != 1 {
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
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))

	rec := withFiles(mailRecord("msg-racing-pulls-"+tag), onePDF())
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}
	stored := filesFor(ctx, t, db, "msg-racing-pulls-"+tag)
	if len(stored) != 1 {
		t.Fatalf("stored %d files, want 1 before the duplicate is attempted", len(stored))
	}

	// The same (message, part) identity, written as a concurrent pull would.
	err := db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO attachment (
				entity_type, entity_id, filename, storage_key,
				source, captured_by, external_source_id, external_part_id)
			VALUES ('activity', $1, 'contract.pdf',
			        'some/other/key', 'imap', 'connector:imap', $2, $3)`,
			activityOf(ctx, t, db, "imap:msg-racing-pulls-"+tag),
			"imap:msg-racing-pulls-"+tag, *stored[0].partID)
		return err
	})
	if err == nil {
		t.Fatal("a second row for the same provider part was accepted — the part identity is not unique")
	}

	if files := filesFor(ctx, t, db, "msg-racing-pulls-"+tag); len(files) != 1 {
		t.Errorf("stored %d files, want the one the refused duplicate did not add", len(files))
	}
}

// A deployment with no object store keeps the correspondence and no files.
// Refusing the message would lose a real exchange over an operator's omission.
func TestWithNoObjectStoreTheMessageLandsAndItsFilesDoNot(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db)

	rec := withFiles(mailRecord("msg-no-store-"+tag), onePDF())
	ref, err := sink.Upsert(ctx, rec)
	if err != nil {
		t.Fatalf("capture with no blob seam: %v", err)
	}
	if ref.ID == (ids.UUID{}) {
		t.Fatal("the message itself was not captured")
	}
	if files := filesFor(ctx, t, db, "msg-no-store-"+tag); len(files) != 0 {
		t.Errorf("stored %d files with no object store configured, want 0", len(files))
	}
}

// DOC-AC-12: what the bounds refused is observable, so a message whose files
// were dropped is not silently identical to one that carried none.
func TestARefusedFileLeavesAnObservableReason(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))

	rec := mailRecord("msg-dropped-file-" + tag)
	rec.PartDrops = []connector.PartDrop{{Reason: "part_too_large", Count: 3}}
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}

	var logged int
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
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

// fileKeeper is the join PRODUCTION makes — the compose adapter itself, not a
// copy of it. Copying it was the whole defect: every case below passed while
// the real wiring was free to rot, because nothing under test was the thing
// that ships.
func fileKeeper(pool *pgxpool.Pool, blob blobstore.Store) capture.FileKeeper {
	return capturedFileKeeper{store: activities.NewStore(InstallationDB(pool)).WithBlobstore(blob)}
}

// A file on a message filed against a company rolls up to that company, which
// is what makes the account's document library one indexed read instead of a
// union over every kind of parent. It is NOT what authorizes the file — that
// stays the activity — so a message filed against nobody rolls up to nothing
// and its file is reachable from the timeline alone.
func TestACapturedFileRollsUpToTheCompanyItsMessageIsFiledUnder(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))

	orgID := ids.NewV7()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO organization (id, display_name, source, captured_by)
			VALUES ($1, 'Voltaq', 'manual', 'human:test')`,
			orgID)
		return err
	}); err != nil {
		t.Fatalf("seed the company: %v", err)
	}

	rec := withFiles(mailRecord("msg-filed-"+tag), onePDF())
	rec.Links = []datasource.EntityRef{{Type: datasource.EntityOrganization, ID: orgID}}
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}

	files := filesFor(ctx, t, db, "msg-filed-"+tag)
	if len(files) != 1 {
		t.Fatalf("stored %d files, want 1", len(files))
	}
	if files[0].organization == nil || *files[0].organization != orgID.String() {
		t.Errorf("organization_id = %v, want the company the message is filed under (%s)",
			files[0].organization, orgID)
	}
}

// And one filed against nobody rolls up to nothing rather than to the wrong
// company. An empty roll-up keeps the file out of every account library; a
// guessed one puts it in somebody else's.
func TestACapturedFileFiledAgainstNobodyRollsUpToNothing(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))

	if _, err := sink.Upsert(ctx, withFiles(mailRecord("msg-unfiled-"+tag), onePDF())); err != nil {
		t.Fatalf("capture: %v", err)
	}

	files := filesFor(ctx, t, db, "msg-unfiled-"+tag)
	if len(files) != 1 {
		t.Fatalf("stored %d files, want 1", len(files))
	}
	if files[0].organization != nil {
		t.Errorf("organization_id = %v, want none — this message names no company",
			*files[0].organization)
	}
}

// activityOf reads the activity a captured file hangs off, so a duplicate can
// be written against the same parent the real one has.
func activityOf(ctx context.Context, t *testing.T, db *database.DB, sourceKey string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT entity_id FROM attachment WHERE external_source_id = $1 LIMIT 1`,
			sourceKey).Scan(&id)
	}); err != nil {
		t.Fatalf("read the captured file's activity: %v", err)
	}
	return id
}

// channelRecord is one captured channel message, shaped the way a channel
// adapter produces one: a transport named, no counterparty address, and a
// display name a mail ladder would have quarantined.
func channelRecord(sourceID string) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "telegram", SourceID: sourceID},
		Fields: capture.ActivityFields{
			Kind:            "message",
			ChannelProvider: "telegram",
			Body:            "here's the deck",
			Direction:       connector.DirectionInbound,
		},
		Source:     "telegram:" + sourceID,
		CapturedBy: "connector:telegram",
		Counterparty: connector.Counterparty{
			Direction:       connector.DirectionInbound,
			DisplayName:     "Chatty",
			ChannelIdentity: connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "990101", Username: "chatty"},
		},
		ThreadKey: "telegram:" + sourceID,
	}
}

func onePhoto() connector.Part {
	return connector.Part{
		Ordinal:     1,
		Filename:    "deck.png",
		ContentType: "image/png",
		Body:        []byte("\x89PNG\r\n\x1a\n the deck"),
	}
}

// A file that arrived on a messaging channel is not an email attachment, and the
// row is where that has to be true: the category reaches the document library,
// every category filter and the audit image, so a wrong one is a permanently
// wrong record nobody is told about.
//
// The two arms are asserted TOGETHER rather than in two tests. The value is
// derived from one fact on the record, so the only failure worth catching is the
// derivation collapsing to a constant — and a constant passes whichever arm it
// happens to match. One test that watches both cannot be satisfied that way.
func TestACapturedFilesCategoryNamesTheTransportThatCarriedIt(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	blob := blobstore.NewMemory()
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blob))

	channelID, mailID := "chan-with-file-"+tag, "mail-with-file-"+tag
	if _, err := sink.Upsert(asConnector(ctx, "connector:telegram"),
		withFiles(channelRecord(channelID), onePhoto())); err != nil {
		t.Fatalf("capture a channel message: %v", err)
	}
	if _, err := sink.Upsert(ctx, withFiles(mailRecord(mailID), onePDF())); err != nil {
		t.Fatalf("capture a mail message: %v", err)
	}

	channelFiles := filesFrom(ctx, t, db, "telegram", channelID)
	if len(channelFiles) != 1 {
		t.Fatalf("the channel message stored %d files, want 1", len(channelFiles))
	}
	if got := channelFiles[0].category; got != "message_attachment" {
		t.Errorf("a telegram photo's category = %q, want message_attachment — "+
			"the transport that carried it decides, and it was not mail", got)
	}
	mailFiles := filesFrom(ctx, t, db, "imap", mailID)
	if len(mailFiles) != 1 {
		t.Fatalf("the mail message stored %d files, want 1", len(mailFiles))
	}
	if got := mailFiles[0].category; got != "email_attachment" {
		t.Errorf("a mail attachment's category = %q, want email_attachment — "+
			"widening the vocabulary must not move the value mail already had", got)
	}
}

// The audit image has to say what was stored. An image carrying a category the
// row does not hold is worse than no image: the trail reads as authoritative and
// is the thing a reader consults when the row is already gone.
func TestTheAuditImageOfACapturedFileNamesTheCategoryTheRowHolds(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))

	sourceID := "chan-audited-" + tag
	if _, err := sink.Upsert(asConnector(ctx, "connector:telegram"),
		withFiles(channelRecord(sourceID), onePhoto())); err != nil {
		t.Fatalf("capture a channel message: %v", err)
	}
	files := filesFrom(ctx, t, db, "telegram", sourceID)
	if len(files) != 1 {
		t.Fatalf("stored %d files, want 1", len(files))
	}

	var audited string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT after->>'category'
			  FROM audit_log
			 WHERE entity_type = 'attachment' AND action = 'create'
			   AND entity_id = (SELECT id FROM attachment
			                     WHERE external_source_id = $1 LIMIT 1)`,
			"telegram:"+sourceID).Scan(&audited)
	}); err != nil {
		t.Fatalf("read the audit image of a captured file: %v", err)
	}
	if audited != files[0].category {
		t.Errorf("the audit image says %q and the row says %q", audited, files[0].category)
	}
}
