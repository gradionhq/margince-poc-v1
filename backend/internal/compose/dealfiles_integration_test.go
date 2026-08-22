// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deal's Files area, end to end through the real capture writer: a file
// that arrived with a message linked to the deal is listed on the deal, an
// inline image is not, hiding takes it off the deal without touching the
// file, and a colleague outside the message's audience never sees it.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

func seedDealForFiles(ctx context.Context, t *testing.T, db *database.DB) ids.UUID {
	t.Helper()
	var dealID ids.UUID
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		var pipelineID, stageID ids.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO pipeline (name) VALUES ($1) RETURNING id`, "Files test "+ids.NewV7().String()).Scan(&pipelineID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO stage (pipeline_id, name, position) VALUES ($1, 'Qualified', 0) RETURNING id`, pipelineID).Scan(&stageID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO deal (name, stage_id, pipeline_id, source, captured_by)
			VALUES ('Acme rollout', $1, $2, 'manual', 'human:test') RETURNING id`, stageID, pipelineID).Scan(&dealID)
	}); err != nil {
		t.Fatalf("seed the deal: %v", err)
	}
	return dealID
}

// asRep is a human with every grant the Files area consults and unbounded
// row scope, so the only thing that can withhold a captured file is the
// message's audience.
func asRep(ctx context.Context, userID ids.UUID) context.Context {
	return principal.WithActor(ctx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:" + userID.String(),
		UserID: userID,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"deal":         {Read: true, Update: true},
				"activity":     {Read: true},
				"organization": {Read: true},
				"person":       {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

func inlineLogo() connector.Part {
	return connector.Part{
		Ordinal:     2,
		Filename:    "image001.png",
		ContentType: "image/png",
		Body:        []byte("PNG tiny logo"),
	}
}

func TestAnEmailedFileIsListedOnTheDealItIsLinkedToAndCanBeHidden(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))
	dealID := seedDealForFiles(ctx, t, db)

	rec := withFiles(mailRecord("msg-deal-"+tag), onePDF(), inlineLogo())
	rec.Links = []datasource.EntityRef{{Type: datasource.EntityDeal, ID: dealID}}
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}

	store := activities.NewStore(db)
	rep := asRep(ctx, ids.NewV7())
	docs, _, err := store.ListDealDocuments(rep, dealID, activities.DealDocumentFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(docs) != 1 || docs[0].Attachment.Filename != "contract.pdf" {
		t.Fatalf("deal files = %+v, want the one PDF and no inline logo", docs)
	}
	if docs[0].Origin == nil || docs[0].Origin.Kind != "email" || docs[0].Origin.CounterpartyEmail == nil {
		t.Fatalf("origin = %+v, want the email it arrived with", docs[0].Origin)
	}
	attachmentID := ids.UUID(docs[0].Attachment.Id)

	// Hidden: off the deal, still on the activity, back with include_hidden.
	if err := store.HideDealDocument(rep, dealID, attachmentID); err != nil {
		t.Fatalf("hide: %v", err)
	}
	docs, _, err = store.ListDealDocuments(rep, dealID, activities.DealDocumentFilters{})
	if err != nil || len(docs) != 0 {
		t.Fatalf("after hide: %d files, err %v; want none", len(docs), err)
	}
	withHidden, _, err := store.ListDealDocuments(rep, dealID, activities.DealDocumentFilters{IncludeHidden: true})
	if err != nil || len(withHidden) != 1 || !withHidden[0].Hidden {
		t.Fatalf("with hidden: %+v, err %v; want the file marked hidden", withHidden, err)
	}
	var archived *string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT archived_at::text FROM attachment WHERE id = $1`, attachmentID).Scan(&archived)
	}); err != nil {
		t.Fatalf("read the file: %v", err)
	}
	if archived != nil {
		t.Fatal("hiding archived the file; it must stay on its message")
	}
	if err := store.UnhideDealDocument(rep, dealID, attachmentID); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	docs, _, err = store.ListDealDocuments(rep, dealID, activities.DealDocumentFilters{})
	if err != nil || len(docs) != 1 {
		t.Fatalf("after unhide: %d files, err %v; want the file back", len(docs), err)
	}
}

func TestAColleagueOutsideTheMessagesAudienceSeesNoFileOnTheDeal(t *testing.T) {
	ctx, db, tag := captureWorkspace(t)
	sink := capture.NewSink(db).WithFileKeeper(fileKeeper(db.Pool(), blobstore.NewMemory()))
	dealID := seedDealForFiles(ctx, t, db)

	rec := withFiles(mailRecord("msg-private-"+tag), onePDF())
	rec.Links = []datasource.EntityRef{{Type: datasource.EntityDeal, ID: dealID}}
	if _, err := sink.Upsert(ctx, rec); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE activity SET audience = 'participants' WHERE source_system = 'imap' AND source_id = $1`, "msg-private-"+tag)
		return err
	}); err != nil {
		t.Fatalf("narrow the audience: %v", err)
	}

	store := activities.NewStore(db)
	colleague := asRep(ctx, ids.NewV7())
	docs, _, err := store.ListDealDocuments(colleague, dealID, activities.DealDocumentFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("a colleague outside the audience sees %+v; the deal must not widen the message's audience", docs)
	}
	var attachmentID ids.UUID
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM attachment WHERE external_source_id = $1`, "imap:msg-private-"+tag).Scan(&attachmentID)
	}); err != nil {
		t.Fatalf("read the file: %v", err)
	}
	// And the hide is no oracle: a file the caller cannot see is not found.
	if err := store.HideDealDocument(colleague, dealID, attachmentID); err == nil {
		t.Fatal("hide of an invisible file succeeded; it must read as not-found")
	}
}
