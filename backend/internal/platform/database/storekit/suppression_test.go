package storekit

import "testing"

// The suppression hash is a matching contract between the eraser and
// every ingest path: normalization must be identical on both sides,
// and a pattern-metacharacter identifier must match itself, never act
// as a wildcard.

func TestSuppressionHashNormalizes(t *testing.T) {
	base := SuppressionHash("selma@example.test")
	for _, variant := range []string{"SELMA@example.test", "  selma@example.test  ", "Selma@Example.Test"} {
		if SuppressionHash(variant) != base {
			t.Errorf("variant %q hashes differently — a trivial respelling would resurrect the subject", variant)
		}
	}
	if SuppressionHash("other@example.test") == base {
		t.Error("distinct identifiers collide")
	}
}

func TestEscapeLikeNeutralizesWildcards(t *testing.T) {
	cases := map[string]string{
		`a%b@example.test`: `a\%b@example.test`,
		`a_b@example.test`: `a\_b@example.test`,
		`a\b@example.test`: `a\\b@example.test`,
		`plain@example.te`: `plain@example.te`,
	}
	for in, want := range cases {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
