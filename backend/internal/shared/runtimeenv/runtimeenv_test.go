// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runtimeenv

import "testing"

func TestParseFailsClosed(t *testing.T) {
	nonProd := map[string]Environment{"dev": Development, "test": Test}
	for in, want := range nonProd {
		if got := Parse(in); got != want || !got.IsNonProduction() {
			t.Fatalf("Parse(%q) = %q (nonProd=%v); want %q nonProd=true", in, got, got.IsNonProduction(), want)
		}
	}
	// Anything not on the explicit non-prod allowlist collapses to Production.
	// "staging" is in this list on purpose. It used to parse to its own
	// non-production posture, which meant an installation full of real internal
	// users honoured dev-signed licenses — and, before the reset became an
	// explicit switch, could have its data purged through the API.
	for _, in := range []string{"", "production", "prod", "PROD", "Dev", "staging", "staging ", "qa", "🤷"} {
		if got := Parse(in); got != Production || got.IsNonProduction() {
			t.Fatalf("Parse(%q) = %q (nonProd=%v); want production, nonProd=false", in, got, got.IsNonProduction())
		}
	}
}
