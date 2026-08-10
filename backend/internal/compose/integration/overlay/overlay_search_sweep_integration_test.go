// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The overlay search sweep, against a real mirror.
//
// Two defects meet here. `search_records` advertises that omitting
// `record_type` sweeps every type, and the overlay provider refused any query
// that did not name exactly one — so an agent doing exactly what the schema
// says was refused on an overlay workspace, for a rule the schema never
// stated. And the REST search shadow, which DID walk every type, reported
// `has_more` with no `next_cursor`, so whatever the first page did not carry
// was unreachable by any means the contract offers.
//
// The sweep answers both, and what makes it more than a loop is that the
// position is resumable: a walk with no ranking to interleave types by can
// still say where it stopped. This asks it to page a set it cannot fit, one
// hit at a time, and holds it to the invariant that a page claiming more
// carries somewhere to go.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	overlaymod "github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sweptPage is one /v1/search response, read down to what these assertions
// are about: which records came back, and whether the page says more exist.
type sweptPage struct {
	Data []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"data"`
	Page struct {
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	} `json:"page"`
}

func TestOverlaySearchSweepsEveryMirroredTypeAndPagesThroughThem(t *testing.T) {
	e := setupOverlayWrite(t)
	// One token in a string field of each record, so the sweep's substring
	// filter reaches all five and the assertion is about the WALK rather than
	// about matching.
	e.seed(t, "person", "9401", map[string]any{"first_name": "Sweepable", "last_name": "One"})
	e.seed(t, "person", "9402", map[string]any{"first_name": "Sweepable", "last_name": "Two"})
	e.seed(t, "organization", "9403", map[string]any{"display_name": "Sweepable Org"})
	e.seed(t, "deal", "9404", map[string]any{"name": "Sweepable Renewal", "currency": "EUR"})
	e.seed(t, "lead", "9405", map[string]any{"full_name": "Sweepable Lead"})

	// One hit per page, so every step of the walk crosses or exhausts a type
	// and the cursor is exercised in both of its shapes: resuming INSIDE a
	// type, and starting the next one.
	seen := map[string]string{}
	path := "/v1/search?q=Sweepable&limit=1"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatalf("the sweep did not terminate after %d pages, holding %d records", pages, len(seen))
		}
		var page sweptPage
		if status := e.Call(t, "GET", path, nil, nil, &page); status != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, status)
		}
		for _, hit := range page.Data {
			if _, repeated := seen[hit.ID]; repeated {
				t.Fatalf("the sweep returned %s (%s) twice — a resumed walk must not re-serve what it already handed over",
					hit.ID, hit.Type)
			}
			seen[hit.ID] = hit.Type
		}
		// The invariant #586 was filed for: more is only true when there is
		// somewhere to go, and somewhere to go only when there is more.
		if page.Page.HasMore != (page.Page.NextCursor != nil && *page.Page.NextCursor != "") {
			t.Fatalf("has_more = %v with next_cursor = %v — a page that claims more and offers no way to reach "+
				"it leaves those records unreachable", page.Page.HasMore, page.Page.NextCursor)
		}
		if !page.Page.HasMore {
			break
		}
		path = "/v1/search?q=Sweepable&limit=1&cursor=" + *page.Page.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("the sweep reached %d of the 5 seeded records: %v", len(seen), seen)
	}
	for _, want := range []string{"person", "organization", "deal", "lead"} {
		found := false
		for _, recordType := range seen {
			found = found || recordType == want
		}
		if !found {
			t.Errorf("the sweep never reached a %s — it walks every mirrored type or it is not a sweep", want)
		}
	}
}

// The tool this was filed against. `search_records` says `record_type` may be
// omitted to sweep all five, and the overlay provider used to refuse exactly
// that — an agent doing what the schema told it to was answered with an error
// about a rule the schema never stated. This drives the tool itself, through
// the registry a real call comes in on.
func TestTheSearchRecordsToolSweepsWithoutARecordTypeInOverlayMode(t *testing.T) {
	e := integration.Setup(t)
	ws, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtxWith(ws, actorID, nativeToolReaderPerms())

	mirror := overlaymod.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to the incumbent owner: %v", err)
	}
	if err := mirror.Ingest(ctx, overlaymod.Record{
		ObjectClass: "person", ExternalID: "100214862044", OwnerExternalID: "owner-1",
		Fields: map[string]any{"firstname": "Unrestricted", "lastname": "Sweep"}, ModifiedAt: seedModifiedAt,
	}); err != nil {
		t.Fatalf("seeding the mirrored person: %v", err)
	}

	out, err := compose.NewRegistry(e.Pool, compose.SendPath{}).
		Invoke(ctx, "search_records", json.RawMessage(`{"q":"Unrestricted"}`))
	if err != nil {
		t.Fatalf("search_records with no record_type in overlay mode: %v — the schema says omitting it sweeps "+
			"every type, and a surface that refuses what it advertises misleads the only caller that reads it", err)
	}
	if !strings.Contains(string(out), "Unrestricted") {
		t.Fatalf("the sweep answered without the mirrored person, so it did not reach the person arm: %s", out)
	}
}

// A type the mirror cannot hold is refused rather than answered with an empty
// page. The contract's `types` enum carries six values and the mirror carries
// five, so the alternative reads as "this workspace has no projects" when the
// truth is about the mode.
func TestOverlaySearchRefusesATypeTheMirrorDoesNotHold(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "person", "9410", map[string]any{"first_name": "Present", "last_name": "Person"})

	if status := e.Call(t, "GET", "/v1/search?q=Present&types=project", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Errorf("GET /v1/search?types=project = %d, want 422 — an unmirrored type must be refused, not "+
			"answered with a page that says the records do not exist", status)
	}
	if status := e.Call(t, "GET", "/v1/search?q=Present&types=person", nil, nil, nil); status != http.StatusOK {
		t.Errorf("GET /v1/search?types=person = %d, want 200 — the refusal above is about the type, not the parameter", status)
	}
}
