// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Deal Room buyer edge, end to end through the real stack: a rep opens a
// room, publishes it, invites a buyer; the buyer exchanges the link, reads the
// release, ticks a to-do; the rep pauses and then revokes; the buyer's session
// stops answering. Alongside, the two security properties the edge exists for:
// every dead credential reads alike, and a room session holds no CRM authority.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

// buyerRoom is the seller-side setup every scenario starts from: a live,
// published room with one invited buyer, and the buyer's credential.
type buyerRoom struct {
	roomID     string
	credential string
	email      string
}

func openPublishedRoom(t *testing.T, e *apptest.AppEnv) buyerRoom {
	t.Helper()
	stages := apptest.DiscoverSeededPipeline(t, e)
	dealID := apptest.CreateOpenDeal(t, e, stages)

	var room apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms", apptest.AnyMap{
		"deal_id": dealID, "title": "Acme rollout", "welcome_message": "Welcome, Laura.", "source": "ui",
	}, nil, &room); status != http.StatusCreated {
		t.Fatalf("create room = %d %v", status, room)
	}
	roomID, _ := room["id"].(string)

	var release apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+roomID+"/publish", apptest.AnyMap{}, nil, &release); status != http.StatusCreated {
		t.Fatalf("publish = %d %v", status, release)
	}

	var issued apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+roomID+"/participants", apptest.AnyMap{
		"full_name": "Laura Buyer", "email": "laura@buyer.example", "capability": "comment", "source": "ui",
	}, nil, &issued); status != http.StatusCreated {
		t.Fatalf("invite = %d %v", status, issued)
	}
	credential, _ := issued["credential"].(string)
	if credential == "" {
		t.Fatalf("invite returned no credential: %v", issued)
	}
	return buyerRoom{roomID: roomID, credential: credential, email: "laura@buyer.example"}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestABuyerEntersTheRoomReadsTheReleaseAndSpeaks(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)

	var peek apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/peek", apptest.AnyMap{"credential": room.credential}, nil, &peek); status != http.StatusOK || peek["exchangeable"] != true {
		t.Fatalf("peek = %d %v, want 200 exchangeable", status, peek)
	}

	var session apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": room.credential}, nil, &session); status != http.StatusOK {
		t.Fatalf("exchange = %d %v", status, session)
	}
	token, _ := session["session_token"].(string)

	// One-time: the same credential a second time admits nobody.
	if status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": room.credential}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("second exchange = %d, want 404", status)
	}

	var me apptest.AnyMap
	if status := publicCall(t, e, "GET", "/v1/public/rooms/me", nil, bearer(token), &me); status != http.StatusOK {
		t.Fatalf("me = %d %v", status, me)
	}
	if me["access"] != "live" {
		t.Fatalf("access = %v, want live", me["access"])
	}
	content, _ := me["room"].(map[string]any)
	if content["title"] != "Acme rollout" || content["welcome_message"] != "Welcome, Laura." {
		t.Fatalf("room content = %v", content)
	}
	participant, _ := me["participant"].(map[string]any)
	if participant["email"] != room.email {
		t.Fatalf("participant = %v", participant)
	}

	// The release is what the buyer reads: a rename after publish changes nothing.
	var patched apptest.AnyMap
	if status := e.Call(t, "PATCH", "/v1/deal-rooms/"+room.roomID, apptest.AnyMap{"title": "Renamed after publish"}, nil, &patched); status != http.StatusOK {
		t.Fatalf("patch = %d %v", status, patched)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/me", nil, bearer(token), &me); status != http.StatusOK {
		t.Fatalf("me after rename = %d", status)
	}
	if content, _ := me["room"].(map[string]any); content["title"] != "Acme rollout" {
		t.Fatalf("buyer saw an unpublished rename: %v", content["title"])
	}

	// The buyer speaks: a room-level thread, live, no publish needed.
	var opened apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads", apptest.AnyMap{"body": "When does the pilot start?"}, bearer(token), &opened); status != http.StatusCreated {
		t.Fatalf("open thread = %d %v", status, opened)
	}

	// The seller reads the same thread, attributed to the buyer's side.
	var sellerThreads apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID+"/threads", nil, nil, &sellerThreads); status != http.StatusOK {
		t.Fatalf("seller threads = %d", status)
	}
	sellerList, _ := sellerThreads["data"].([]any)
	if len(sellerList) != 1 {
		t.Fatalf("seller threads = %v, want the one the buyer opened", sellerList)
	}
	first, _ := sellerList[0].(map[string]any)
	comments, _ := first["comments"].([]any)
	firstComment, _ := comments[0].(map[string]any)
	author, _ := firstComment["author"].(map[string]any)
	if author["side"] != "buyer" {
		t.Fatalf("seller view of the buyer's comment = %v", first)
	}

	// Pause: the session still resolves, content is withheld, the tick refuses.
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/pause", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("pause = %d", status)
	}
	// A fresh map: decoding into the one above would keep its old "room" key.
	var paused apptest.AnyMap
	if status := publicCall(t, e, "GET", "/v1/public/rooms/me", nil, bearer(token), &paused); status != http.StatusOK || paused["access"] != "paused" || paused["room"] != nil {
		t.Fatalf("paused me = %d %v, want access paused and no room", status, paused)
	}
	var refused apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads", apptest.AnyMap{"body": "still there?"}, bearer(token), &refused); status != http.StatusUnprocessableEntity || refused["code"] != "deal_room_paused" {
		t.Fatalf("comment while paused = %d %v, want 422 deal_room_paused", status, refused)
	}

	// Revoke: the next request is refused.
	var roster apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID+"/participants", nil, nil, &roster); status != http.StatusOK {
		t.Fatalf("roster = %d", status)
	}
	seats, _ := roster["data"].([]any)
	seat, _ := seats[0].(map[string]any)
	participantID, _ := seat["id"].(string)
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/participants/"+participantID+"/revoke", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("revoke = %d", status)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/me", nil, bearer(token), nil); status != http.StatusUnauthorized {
		t.Fatalf("me after revoke = %d, want 401", status)
	}
}

func TestEveryDeadCredentialReadsAlikeAndARoomSessionHoldsNoCRMAuthority(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)

	// Pause BEFORE the exchange: a valid credential for a paused room still
	// exchanges, so the anonymous edge cannot say whether a room is paused.
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/pause", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("pause = %d", status)
	}
	var session apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": room.credential}, nil, &session); status != http.StatusOK {
		t.Fatalf("exchange into a paused room = %d, want 200", status)
	}
	token, _ := session["session_token"].(string)

	// Unknown, consumed and a session token presented as a credential: one answer.
	var shapes []apptest.AnyMap
	for _, dead := range []string{"mdr_unknown", room.credential, token} {
		var body apptest.AnyMap
		status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": dead}, nil, &body)
		if status != http.StatusNotFound {
			t.Fatalf("exchange %q = %d, want 404", dead, status)
		}
		delete(body, "instance")
		shapes = append(shapes, body)
		var peek apptest.AnyMap
		if status := publicCall(t, e, "POST", "/v1/public/rooms/peek", apptest.AnyMap{"credential": dead}, nil, &peek); status != http.StatusOK || peek["exchangeable"] != false {
			t.Fatalf("peek %q = %d %v, want 200 not exchangeable", dead, status, peek)
		}
	}
	for i := 1; i < len(shapes); i++ {
		if len(shapes[i]) != len(shapes[0]) || shapes[i]["code"] != shapes[0]["code"] || shapes[i]["detail"] != shapes[0]["detail"] {
			t.Fatalf("dead credentials read differently: %v vs %v", shapes[0], shapes[i])
		}
	}

	// The room session is not a passport: every seat route refuses it.
	for _, path := range []string{"/v1/deals", "/v1/people", "/v1/organizations", "/v1/deal-rooms", "/v1/me"} {
		if status := publicCall(t, e, "GET", path, nil, bearer(token), nil); status != http.StatusUnauthorized {
			t.Fatalf("GET %s with a room session = %d, want 401", path, status)
		}
	}

	// A read-only participant reads the list but cannot work it.
	var viewerIssued apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/participants", apptest.AnyMap{
		"full_name": "Victor Viewer", "email": "victor@buyer.example", "capability": "view", "source": "ui",
	}, nil, &viewerIssued); status != http.StatusCreated {
		t.Fatalf("invite viewer = %d %v", status, viewerIssued)
	}
	var viewerSession apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": viewerIssued["credential"]}, nil, &viewerSession); status != http.StatusOK {
		t.Fatalf("viewer exchange = %d", status)
	}
	viewerToken, _ := viewerSession["session_token"].(string)
	var viewerRefused apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads", apptest.AnyMap{"body": "hello"}, bearer(viewerToken), &viewerRefused); status != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(viewerRefused["detail"]), "read-only") {
		t.Fatalf("viewer comment = %d %v, want 422 view_only", status, viewerRefused)
	}

	// Sign-out works while paused (an access act), and ends the session.
	if status := publicCall(t, e, "POST", "/v1/public/rooms/sign-out", nil, bearer(token), nil); status != http.StatusNoContent {
		t.Fatalf("sign-out = %d, want 204", status)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/me", nil, bearer(token), nil); status != http.StatusUnauthorized {
		t.Fatalf("me after sign-out = %d, want 401", status)
	}
}

// uploadDealFile files a document on the deal over the real upload route, the
// way a rep does, and returns the attachment id.
func uploadDealFile(t *testing.T, e *apptest.AppEnv, dealID, filename string, data []byte) string {
	t.Helper()
	body, ctype := multipartAttachment(t, "deal", dealID, filename, data)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.TS.URL+"/v1/attachments", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := e.Client.Do(req) //nolint:bodyclose // closed by apptest.CloseBody below
	if err != nil {
		t.Fatal(err)
	}
	defer apptest.CloseBody(t, resp)
	var att apptest.AnyMap
	if err := json.NewDecoder(resp.Body).Decode(&att); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d (%v) %v", resp.StatusCode, err, att)
	}
	id, _ := att["id"].(string)
	return id
}

func TestABuyerReadsAndDownloadsOnlyWhatTheReleaseNames(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithBlobstore(blobstore.NewMemory()))
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)
	var roomRow apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID, nil, nil, &roomRow); status != http.StatusOK {
		t.Fatalf("room = %d", status)
	}
	dealID, _ := roomRow["deal_id"].(string)

	// A file on the deal goes into the room under a fixed group; one on some
	// other record is refused as absent.
	attachmentID := uploadDealFile(t, e, dealID, "DPA_v7.pdf", []byte("%PDF-DPA"))
	var doc apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/documents", apptest.AnyMap{
		"attachment_id": attachmentID, "group_key": "legal", "title": "Data processing agreement", "source": "ui",
	}, nil, &doc); status != http.StatusCreated {
		t.Fatalf("add document = %d %v", status, doc)
	}
	docID, _ := doc["id"].(string)
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/documents", apptest.AnyMap{
		"attachment_id": attachmentID, "group_key": "marketing", "source": "ui",
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown group = %d, want 422", status)
	}

	var session apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": room.credential}, nil, &session); status != http.StatusOK {
		t.Fatalf("exchange = %d", status)
	}
	token, _ := session["session_token"].(string)

	// Not yet published: the buyer sees nothing, and the file is not served.
	var before apptest.AnyMap
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents", nil, bearer(token), &before); status != http.StatusOK {
		t.Fatalf("documents = %d", status)
	}
	if list, _ := before["data"].([]any); len(list) != 0 {
		t.Fatalf("an unpublished document reached the buyer: %v", list)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents/"+docID+"/file", nil, bearer(token), nil); status != http.StatusNotFound {
		t.Fatalf("unpublished download = %d, want 404", status)
	}

	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/publish", apptest.AnyMap{}, nil, nil); status != http.StatusCreated {
		t.Fatalf("publish = %d", status)
	}
	var after apptest.AnyMap
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents", nil, bearer(token), &after); status != http.StatusOK {
		t.Fatalf("documents = %d", status)
	}
	list, _ := after["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("published documents = %v, want one", list)
	}
	first, _ := list[0].(map[string]any)
	if first["title"] != "Data processing agreement" || first["group_key"] != "legal" || first["filename"] != "DPA_v7.pdf" {
		t.Fatalf("document = %v", first)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.TS.URL+"/v1/public/rooms/documents/"+docID+"/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.TS.Client().Do(req) //nolint:bodyclose // closed by apptest.CloseBody below
	if err != nil {
		t.Fatal(err)
	}
	defer apptest.CloseBody(t, resp)
	bytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(bytes) != "%PDF-DPA" || !strings.Contains(resp.Header.Get("Content-Disposition"), "DPA_v7.pdf") {
		t.Fatalf("download = %d %q %q", resp.StatusCode, bytes, resp.Header.Get("Content-Disposition"))
	}

	// The file archived on the deal: the release still names it, but the bytes
	// are no longer the deal's to hand out.
	if status := e.Call(t, "DELETE", "/v1/attachments/"+attachmentID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("archive attachment = %d", status)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/documents/"+docID+"/file", nil, bearer(token), nil); status != http.StatusNotFound {
		t.Fatalf("download of an archived file = %d, want 404", status)
	}

	// Removed after publish: the release still names it, so the buyer still
	// reads it until the next publish — and then it is gone.
	if status := e.Call(t, "DELETE", "/v1/deal-rooms/"+room.roomID+"/documents/"+docID, nil, map[string]string{"If-Match": fmt.Sprint(doc["version"])}, nil); status != http.StatusOK {
		t.Fatalf("remove = %d", status)
	}
	var still apptest.AnyMap
	publicCall(t, e, "GET", "/v1/public/rooms/documents", nil, bearer(token), &still)
	if list, _ := still["data"].([]any); len(list) != 1 {
		t.Fatalf("a removal reached the buyer before a publish: %v", list)
	}
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/publish", apptest.AnyMap{}, nil, nil); status != http.StatusCreated {
		t.Fatalf("republish = %d", status)
	}
	var gone apptest.AnyMap
	publicCall(t, e, "GET", "/v1/public/rooms/documents", nil, bearer(token), &gone)
	if list, _ := gone["data"].([]any); len(list) != 0 {
		t.Fatalf("a removed document survived the republish: %v", list)
	}
}

func TestTheConversationFlowsBothWaysAndAConfirmationWaitsForOpenChanges(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithBlobstore(blobstore.NewMemory()))
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)
	var roomRow apptest.AnyMap
	e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID, nil, nil, &roomRow)
	dealID, _ := roomRow["deal_id"].(string)
	attachmentID := uploadDealFile(t, e, dealID, "MSA_v2.pdf", []byte("%PDF-MSA"))
	var doc apptest.AnyMap
	e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/documents", apptest.AnyMap{
		"attachment_id": attachmentID, "group_key": "legal", "source": "ui",
	}, nil, &doc)
	docID, _ := doc["id"].(string)
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/publish", apptest.AnyMap{}, nil, nil); status != http.StatusCreated {
		t.Fatalf("publish = %d", status)
	}

	// Laura may comment; Rita may decide.
	var session apptest.AnyMap
	publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": room.credential}, nil, &session)
	laura, _ := session["session_token"].(string)
	var reviewerIssued, reviewerSession apptest.AnyMap
	e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/participants", apptest.AnyMap{
		"full_name": "Rita Reviewer", "email": "rita@buyer.example", "capability": "reviewer", "source": "ui",
	}, nil, &reviewerIssued)
	publicCall(t, e, "POST", "/v1/public/rooms/exchange", apptest.AnyMap{"credential": reviewerIssued["credential"]}, nil, &reviewerSession)
	rita, _ := reviewerSession["session_token"].(string)

	// The seller asks on the document; the buyer answers; both names show.
	var opened apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/threads", apptest.AnyMap{
		"document_id": docID, "body": "Does clause 4 work for you?", "source": "ui",
	}, nil, &opened); status != http.StatusCreated {
		t.Fatalf("open thread = %d %v", status, opened)
	}
	threadID, _ := opened["id"].(string)
	var replied apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads/"+threadID+"/comments", apptest.AnyMap{"body": "Thirty days would."}, bearer(laura), &replied); status != http.StatusCreated {
		t.Fatalf("buyer reply = %d %v", status, replied)
	}
	comments, _ := replied["comments"].([]any)
	if len(comments) != 2 {
		t.Fatalf("comments = %v, want two", comments)
	}
	second, _ := comments[1].(map[string]any)
	author, _ := second["author"].(map[string]any)
	if author["side"] != "buyer" || author["name"] != "Laura Buyer" {
		t.Fatalf("reply author = %v", author)
	}

	// A required change from the reviewer blocks confirmation until resolved.
	var required apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads", apptest.AnyMap{
		"document_id": docID, "body": "Please shorten the cure period.", "required_change": true,
	}, bearer(rita), &required); status != http.StatusCreated {
		t.Fatalf("required-change thread = %d %v", status, required)
	}
	var refused apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/documents/"+docID+"/decision", apptest.AnyMap{"kind": "confirm_version"}, bearer(rita), &refused); status != http.StatusUnprocessableEntity || refused["code"] != "open_required_threads" {
		t.Fatalf("confirm with an open change = %d %v, want 422 open_required_threads", status, refused)
	}
	// Laura is not a reviewer: no decision at all.
	if status := publicCall(t, e, "POST", "/v1/public/rooms/documents/"+docID+"/decision", apptest.AnyMap{"kind": "request_changes"}, bearer(laura), nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("decision by a commenter = %d, want 422", status)
	}
	requiredID, _ := required["id"].(string)
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/threads/"+requiredID+"/resolve", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("resolve = %d", status)
	}
	var decision apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/documents/"+docID+"/decision", apptest.AnyMap{"kind": "confirm_version"}, bearer(rita), &decision); status != http.StatusCreated || decision["kind"] != "confirm_version" || decision["attachment_id"] != attachmentID {
		t.Fatalf("confirm after resolve = %d %v", status, decision)
	}
	// A resolved thread takes no more replies.
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads/"+requiredID+"/comments", apptest.AnyMap{"body": "one more"}, bearer(rita), nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("reply on resolved = %d, want 422", status)
	}

	// The seller reads the decision with the reviewer's name.
	var decisions apptest.AnyMap
	e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID+"/decisions", nil, nil, &decisions)
	list, _ := decisions["data"].([]any)
	first, _ := list[0].(map[string]any)
	if len(list) != 1 || first["participant_name"] != "Rita Reviewer" {
		t.Fatalf("decisions = %v", list)
	}

	// A thread on a document the seller has not published stays the seller's.
	hidden := uploadDealFile(t, e, dealID, "pricing_internal.xlsx", []byte("secret"))
	var hiddenDoc apptest.AnyMap
	e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/documents", apptest.AnyMap{"attachment_id": hidden, "group_key": "commercial", "source": "ui"}, nil, &hiddenDoc)
	e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/threads", apptest.AnyMap{"document_id": hiddenDoc["id"], "body": "internal note on pricing", "source": "ui"}, nil, nil)
	var visible apptest.AnyMap
	publicCall(t, e, "GET", "/v1/public/rooms/threads", nil, bearer(laura), &visible)
	for _, th := range visible["data"].([]any) {
		if m, _ := th.(map[string]any); m["document_id"] == hiddenDoc["id"] {
			t.Fatalf("a thread on an unpublished document reached the buyer: %v", m)
		}
	}

	// Paused: the conversation reads, but nobody on the buyer's side writes.
	e.Call(t, "POST", "/v1/deal-rooms/"+room.roomID+"/pause", apptest.AnyMap{}, nil, nil)
	if status := publicCall(t, e, "POST", "/v1/public/rooms/threads", apptest.AnyMap{"body": "hello?"}, bearer(laura), nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("thread while paused = %d, want 422", status)
	}
}
