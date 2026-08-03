// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runtimeenv

import "testing"

func TestParseFailsClosed(t *testing.T) {
	nonProd := map[string]Environment{"dev": Development, "staging": Staging, "test": Test}
	for in, want := range nonProd {
		if got := Parse(in); got != want || !got.IsNonProduction() {
			t.Fatalf("Parse(%q) = %q (nonProd=%v); want %q nonProd=true", in, got, got.IsNonProduction(), want)
		}
	}
	// Anything not on the explicit non-prod allowlist collapses to Production.
	for _, in := range []string{"", "production", "prod", "PROD", "Dev", "staging ", "qa", "🤷"} {
		if got := Parse(in); got != Production || got.IsNonProduction() {
			t.Fatalf("Parse(%q) = %q (nonProd=%v); want production, nonProd=false", in, got, got.IsNonProduction())
		}
	}
}
