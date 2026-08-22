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
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// buyerRoom is the seller-side setup every scenario starts from: a live room
// with one published task and one invited buyer, and the buyer's credential.
type buyerRoom struct {
	roomID     string
	taskID     string
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

	var task apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/deal-rooms/"+roomID+"/tasks", apptest.AnyMap{
		"side": "buyer", "title": "Sign the DPA", "source": "ui",
	}, nil, &task); status != http.StatusCreated {
		t.Fatalf("create task = %d %v", status, task)
	}
	taskID, _ := task["id"].(string)

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
	return buyerRoom{roomID: roomID, taskID: taskID, credential: credential, email: "laura@buyer.example"}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestABuyerEntersTheRoomReadsTheReleaseAndTicksATask(t *testing.T) {
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

	var tasks apptest.AnyMap
	if status := publicCall(t, e, "GET", "/v1/public/rooms/tasks", nil, bearer(token), &tasks); status != http.StatusOK {
		t.Fatalf("tasks = %d %v", status, tasks)
	}
	list, _ := tasks["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("tasks = %v, want the one published item", list)
	}

	var ticked apptest.AnyMap
	if status := publicCall(t, e, "POST", "/v1/public/rooms/tasks/"+room.taskID+"/complete", apptest.AnyMap{"done": true}, bearer(token), &ticked); status != http.StatusOK {
		t.Fatalf("complete = %d %v", status, ticked)
	}
	if ticked["done"] != true || ticked["done_by"] != "buyer" {
		t.Fatalf("ticked = %v, want done by buyer", ticked)
	}

	// The seller sees the same tick, attributed to the participant.
	var sellerTasks apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID+"/tasks", nil, nil, &sellerTasks); status != http.StatusOK {
		t.Fatalf("seller tasks = %d", status)
	}
	sellerList, _ := sellerTasks["data"].([]any)
	first, _ := sellerList[0].(map[string]any)
	if first["done"] != true || first["done_by_participant_id"] == nil || first["done_by_user_id"] != nil {
		t.Fatalf("seller view of the tick = %v", first)
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
	if status := publicCall(t, e, "POST", "/v1/public/rooms/tasks/"+room.taskID+"/complete", apptest.AnyMap{"done": false}, bearer(token), &refused); status != http.StatusUnprocessableEntity || refused["code"] != "deal_room_task_not_editable" {
		t.Fatalf("tick while paused = %d %v, want 422 deal_room_task_not_editable", status, refused)
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

	// Sign-out works while paused (an access act), and ends the session.
	if status := publicCall(t, e, "POST", "/v1/public/rooms/sign-out", nil, bearer(token), nil); status != http.StatusNoContent {
		t.Fatalf("sign-out = %d, want 204", status)
	}
	if status := publicCall(t, e, "GET", "/v1/public/rooms/me", nil, bearer(token), nil); status != http.StatusUnauthorized {
		t.Fatalf("me after sign-out = %d, want 401", status)
	}
}
