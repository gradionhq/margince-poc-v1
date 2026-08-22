// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A room shares what is in the deal's Files area, not only what was uploaded
// on the deal: a file that arrived with an email linked to the deal can be put
// in the room, published and downloaded by the buyer — and hiding it from the
// deal takes it out of the buyer's download without touching the file.

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// captureFileOnEmail logs an email on the deal over HTTP and files a part on
// it through the attachment table's own captured-file writer — the path a
// mailbox pull takes, so the row carries exactly what capture stamps.
func captureFileOnEmail(t *testing.T, e *apptest.AppEnv, blob blobstore.Store, dealID string) (activityID, attachmentID string) {
	t.Helper()
	var email apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "email", "subject": "Re: MSA", "direction": "inbound",
	}, nil, &email); status != http.StatusCreated {
		t.Fatalf("log email = %d %v", status, email)
	}
	activityID, _ = email["id"].(string)
	if status := e.Call(t, "POST", "/v1/activities/"+activityID+"/relink", apptest.AnyMap{
		"entity_type": "deal", "entity_id": dealID,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("link the email to the deal = %d", status)
	}

	ctx := captureContext(t, e)
	store := activities.NewStore(e.DB()).WithBlobstore(blob)
	staged, err := store.StageCapturedFiles(ctx, []activities.CapturedFile{{
		PartID: "1", Filename: "MSA-redline.docx", ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Body: []byte("redline"),
	}})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return store.RecordCapturedFiles(ctx, tx, ids.From[ids.ActivityKind](ids.MustParse(activityID)),
			activities.CapturedFileSource{System: "imap", MessageID: "msa-" + activityID, CapturedBy: "connector:imap", Category: "email_attachment"}, staged)
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT id FROM attachment WHERE entity_type = 'activity' AND entity_id = $1`, activityID).Scan(&attachmentID); err != nil {
		t.Fatalf("read the captured file: %v", err)
	}
	return activityID, attachmentID
}

// captureContext is the capture principal's authority: create on activity,
// nothing else, on the installation's workspace.
func captureContext(t *testing.T, e *apptest.AppEnv) context.Context {
	t.Helper()
	var wsID ids.UUID
	if err := e.Pool.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	ctx := principal.WithCorrelationID(principal.WithWorkspaceID(context.Background(), wsID), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:imap",
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"activity": {Create: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestAnEmailedFileReachesTheBuyerThroughTheRoomUntilItIsHidden(t *testing.T) {
	blob := blobstore.NewMemory()
	e := apptest.SetupAppWithOptions(t, compose.WithBlobstore(blob))
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)
	var roomRow apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID, nil, nil, &roomRow); status != http.StatusOK {
		t.Fatalf("room = %d", status)
	}
	dealID, _ := roomRow["deal_id"].(string)
	_, attachmentID := captureFileOnEmail(t, e, blob, dealID)

	// The emailed file is in the deal's Files area, and so admissible to the room.
	var area apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deals/"+dealID+"/documents", nil, nil, &area); status != http.StatusOK {
		t.Fatalf("deal files = %d %v", status, area)
	}
	if list, _ := area["data"].([]any); len(list) != 1 {
		t.Fatalf("deal files = %v, want the emailed file", list)
	}
	var doc apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/documents", apptest.AnyMap{
		"attachment_id": attachmentID, "group_key": "legal", "source": "ui",
	}, nil, &doc); status != http.StatusCreated {
		t.Fatalf("add emailed file to the room = %d %v", status, doc)
	}
	docID, _ := doc["id"].(string)
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/publish", apptest.AnyMap{}, nil, nil); status != http.StatusCreated {
		t.Fatalf("publish = %d", status)
	}

	var session apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": room.credential}, nil, &session); status != http.StatusOK {
		t.Fatalf("exchange = %d", status)
	}
	token, _ := session["session_token"].(string)
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents/"+docID+"/file", nil, bearer(token), nil); status != http.StatusOK {
		t.Fatalf("buyer download of the emailed file = %d, want 200", status)
	}

	// Hidden from the deal: the room's draft still lists it for the seller,
	// the buyer's download refuses, and the next release drops it.
	if status := e.Call(t, "PUT", "/v1/deals/"+dealID+"/documents/"+attachmentID+"/hide", nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("hide = %d", status)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents/"+docID+"/file", nil, bearer(token), nil); status != http.StatusNotFound {
		t.Fatalf("download after hide = %d, want 404", status)
	}
	var sellerDocs apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID+"/documents", nil, nil, &sellerDocs); status != http.StatusOK {
		t.Fatalf("seller documents = %d", status)
	}
	if list, _ := sellerDocs["data"].([]any); len(list) != 1 {
		t.Fatalf("the seller's list dropped the hidden entry (%v); only the seller can remove it", list)
	}
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/publish", apptest.AnyMap{}, nil, nil); status != http.StatusCreated {
		t.Fatalf("republish = %d", status)
	}
	var buyerDocs apptest.AnyMap
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents", nil, bearer(token), &buyerDocs); status != http.StatusOK {
		t.Fatalf("buyer documents = %d", status)
	}
	if list, _ := buyerDocs["data"].([]any); len(list) != 0 {
		t.Fatalf("a hidden file is still in the release: %v", list)
	}
}

func TestARepCannotShareATeammatesLimitedAudienceMailAttachment(t *testing.T) {
	blob := blobstore.NewMemory()
	e := apptest.SetupAppWithOptions(t, compose.WithBlobstore(blob))
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)
	var roomRow apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID, nil, nil, &roomRow); status != http.StatusOK {
		t.Fatalf("room = %d", status)
	}
	dealID, _ := roomRow["deal_id"].(string)
	activityID, attachmentID := captureFileOnEmail(t, e, blob, dealID)

	// The mail belongs to somebody else's mailbox and is limited to its
	// participants; the rep working the deal is neither.
	if _, err := e.Pool.Exec(context.Background(),
		`UPDATE activity SET audience = 'participants', captured_by = $2 WHERE id = $1`,
		activityID, "connector:imap:"+ids.NewV7().String()); err != nil {
		t.Fatalf("narrow the mail: %v", err)
	}
	if _, err := e.Pool.Exec(context.Background(),
		`DELETE FROM activity_participant WHERE activity_id = $1`, activityID); err != nil {
		t.Fatalf("take the rep off the mail: %v", err)
	}
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/documents", apptest.AnyMap{
		"attachment_id": attachmentID, "group_key": "legal", "source": "ui",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("sharing a teammate's limited-audience attachment = %d, want 404", status)
	}
}
