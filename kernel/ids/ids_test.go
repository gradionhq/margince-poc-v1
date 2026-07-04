package ids

import (
	"sort"
	"testing"
)

func TestNewV7_versionAndVariant(t *testing.T) {
	u := NewV7()
	if got := u[6] >> 4; got != 7 {
		t.Errorf("version = %d, want 7", got)
	}
	if got := u[8] >> 6; got != 0b10 {
		t.Errorf("variant bits = %02b, want 10", got)
	}
}

func TestNewV7_sortsByCreationOrder(t *testing.T) {
	const n = 1000
	generated := make([]string, n)
	for i := range generated {
		generated[i] = NewV7().String()
	}
	if !sort.StringsAreSorted(generated) {
		t.Error("sequentially generated v7 UUIDs are not lexicographically sorted")
	}
}

func TestParse_roundTrip(t *testing.T) {
	u := NewV7()
	parsed, err := Parse(u.String())
	if err != nil {
		t.Fatalf("Parse(%q): %v", u.String(), err)
	}
	if parsed != u {
		t.Errorf("round-trip mismatch: %v != %v", parsed, u)
	}
}

func TestParse_rejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-uuid",
		"0198c5f4-6c1a-7000-8000-0123456789",    // too short
		"0198c5f4-6c1a-7000-8000-0123456789abc", // too long
		"0198c5f46c1a70008000-0123456789ab",     // dashes misplaced
		"zz98c5f4-6c1a-7000-8000-0123456789ab",  // non-hex
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", s)
		}
	}
}
