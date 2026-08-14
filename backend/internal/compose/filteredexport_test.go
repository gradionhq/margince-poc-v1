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
	// Every package-qualified call counts, not just the one literal spelling
	// the original regression had: "collections.SegmentEngine(" also matches
	// "h.collections.SegmentEngine(", so a second static lookup added
	// alongside the surviving store call — under any argument list, any
	// helper name — still leaves this at zero once the store calls are
	// subtracted out.
	if pkgQualified, viaStore := strings.Count(src, "collections.SegmentEngine("), strings.Count(src, "h.collections.SegmentEngine("); pkgQualified != viaStore {
		t.Errorf("filteredexport.go calls collections.SegmentEngine( %d times but only %d go through h.collections — "+
			"resolve the vocabulary through the store instance on every call site, never a package-level lookup",
			pkgQualified, viaStore)
	}
	if !strings.Contains(src, "h.collections.SegmentEngine(") {
		t.Error("the export handler does not resolve its vocabulary through the collections store instance")
	}
}
