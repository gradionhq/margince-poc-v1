// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A pointer gate in the same spirit as the other compose surface gates: it
// names the surface it protects rather than proving behaviour through the DB,
// because the invariant it guards is textual (which package resolves the
// vocabulary), not something a workspace fixture would exercise differently.

import (
	"os"
	"strings"
	"testing"
)

// Export resolves its vocabulary through the same store the list and the
// validator use. A second, static lookup here is how an export comes to
// return more rows than the list it was built from — the one-engine
// guarantee broken by omission.
func TestFilteredExportResolvesItsVocabularyThroughCollections(t *testing.T) {
	body, err := os.ReadFile("filteredexport.go")
	if err != nil {
		t.Fatalf("read filteredexport.go: %v", err)
	}
	src := string(body)
	if strings.Contains(src, "collections.SegmentEngine(resource)") {
		t.Error("filtered export looks the engine up statically; it must resolve through the store")
	}
	if !strings.Contains(src, "h.collections.SegmentEngine(") {
		t.Error("the export handler does not resolve its vocabulary through the collections store instance")
	}
}
