// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rule that decides whether a file may be in a room is spelled once.
// Three readers apply it — the add, the release, the buyer's download — and
// if any of them grew its own SQL for "is this the deal's file", the next
// change to the rule would reach two of the three and the buyer would keep
// downloading a file the seller had hidden.
func TestRoomDocumentMembershipIsSpelledOnce(t *testing.T) {
	readers := map[string]bool{
		"document_write.go":         false,
		"document_read.go":          false,
		"store_public_documents.go": false,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if _, isReader := readers[name]; isReader {
			if !strings.Contains(text, "inTheDealsFilesArea") {
				t.Errorf("%s no longer applies inTheDealsFilesArea", name)
			}
			readers[name] = true
		} else if strings.Contains(text, "inTheDealsFilesArea") {
			t.Errorf("%s applies the membership rule; only the add, the release and the buyer download may", name)
		}
		// The spelling itself lives in document_read.go; nothing else may
		// re-derive it from the attachment's parent columns.
		if name != "document_read.go" && (strings.Contains(text, "entity_id = r.deal_id") || strings.Contains(text, "deal_document_hide")) {
			t.Errorf("%s spells the deal-file membership itself instead of applying inTheDealsFilesArea", name)
		}
	}
	for name, seen := range readers {
		if !seen {
			t.Errorf("%s was not found; if it moved, move the reader list with it", name)
		}
	}
}
