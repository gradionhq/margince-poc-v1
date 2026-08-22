// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// The brief reads the deal and its room through the real gates and says what
// is on the record: a live room is named, and a deal the caller cannot see
// has no brief.
func TestTheDealBriefNamesTheRoomAndHidesADealTheCallerCannotSee(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	room := openPublishedRoom(t, e)
	var roomRow apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deal-rooms/"+room.roomID, nil, nil, &roomRow); status != http.StatusOK {
		t.Fatalf("room = %d", status)
	}
	dealID, _ := roomRow["deal_id"].(string)

	var brief apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deals/"+dealID+"/brief", nil, nil, &brief); status != http.StatusOK {
		t.Fatalf("brief = %d %v", status, brief)
	}
	if brief["generated_by"] != "deterministic" {
		t.Fatalf("generated_by = %v", brief["generated_by"])
	}
	sections, _ := brief["sections"].([]any)
	kinds := map[string]bool{}
	for _, s := range sections {
		section, _ := s.(map[string]any)
		kinds[section["kind"].(string)] = true
		lines, _ := section["sentences"].([]any)
		for _, l := range lines {
			line, _ := l.(map[string]any)
			if ev, _ := line["evidence"].([]any); len(ev) == 0 {
				t.Errorf("%s: %q cites nothing", section["kind"], line["text"])
			}
		}
	}
	if !kinds["standing"] || !kinds["room"] {
		t.Fatalf("sections = %v, want standing and room", kinds)
	}

	if status := e.Call(t, "GET", "/v1/deals/01a00000-0000-7000-8000-000000000000/brief", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("brief of an unknown deal = %d, want 404", status)
	}
}
