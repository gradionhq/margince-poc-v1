// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTheDiffNamesEveryWayTheNextReleaseWouldDiffer(t *testing.T) {
	kept, retitled, added, dropped, gone := newDocID(), newDocID(), newDocID(), newDocID(), newDocID()
	oldWelcome := "Welcome"
	published := &releaseSnapshot{
		Title: "Acme", WelcomeMessage: &oldWelcome,
		Documents: []snapshotDocument{
			{ID: kept, GroupKey: "legal", Title: "MSA"},
			{ID: retitled, GroupKey: "legal", Title: "DPA v1"},
			{ID: gone, GroupKey: "commercial", Title: "Old pricing"},
			{ID: dropped, GroupKey: "legal", Title: "Emailed redline"},
		},
	}
	newWelcome := "Welcome, Laura"
	room := crmcontracts.DealRoom{Title: "Acme rollout", WelcomeMessage: &newWelcome}
	eligible := []crmcontracts.DealRoomDocument{
		{Id: kept, GroupKey: "legal", Title: "MSA"},
		{Id: retitled, GroupKey: "commercial", Title: "DPA v2", Position: 2},
		{Id: added, GroupKey: "legal", Title: "SOW"},
	}
	all := append(append([]crmcontracts.DealRoomDocument{}, eligible...),
		crmcontracts.DealRoomDocument{Id: dropped, GroupKey: "legal", Title: "Emailed redline"})

	got := map[string][]string{}
	for _, c := range diffRelease(room, published, eligible, all) {
		title := ""
		if c.Title != nil {
			title = *c.Title
		}
		got[c.Kind] = append(got[c.Kind], title)
	}
	want := map[string][]string{
		changeTitle:              {""},
		changeWelcome:            {""},
		changeDocumentRetitled:   {"DPA v2"},
		changeDocumentRegrouped:  {"DPA v2"},
		changeDocumentReordered:  {"DPA v2"},
		changeDocumentAdded:      {"SOW"},
		changeDocumentIneligible: {"Emailed redline"},
		changeDocumentRemoved:    {"Old pricing"},
	}
	for kind, titles := range want {
		if len(got[kind]) != len(titles) || (len(titles) > 0 && got[kind][0] != titles[0]) {
			t.Errorf("%s = %v, want %v", kind, got[kind], titles)
		}
	}
	if len(got) != len(want) {
		t.Errorf("kinds = %v, want exactly %v", got, want)
	}

	// Never published: the title (and a welcome, when set) plus every
	// eligible document as an addition — so the first publish is possible
	// even for a room with no documents yet.
	first := diffRelease(room, nil, eligible, all)
	adds := 0
	for _, c := range first {
		switch c.Kind {
		case changeDocumentAdded:
			adds++
		case changeTitle, changeWelcome, changeDocumentIneligible:
		default:
			t.Errorf("unpublished room reports %s", c.Kind)
		}
	}
	if adds != len(eligible) {
		t.Errorf("unpublished room reports %d additions, want %d", adds, len(eligible))
	}
}

func newDocID() openapi_types.UUID { return openapi_types.UUID(ids.NewV7()) }
