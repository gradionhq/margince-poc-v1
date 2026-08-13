// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"testing"
)

// The cases are the real headers a Gmail import produced, so the table doubles
// as the record of what was actually wrong: every `want` here is a row that
// shipped broken before the parser existed.
func TestParsePersonNameReadsTheNameAHeaderCarries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		display     string
		email       string
		wantFull    string
		wantFirst   string
		wantLast    string
		wantHonor   string
		wantConfide bool
	}{
		{
			name:  "dotted local part splits into a first and last name",
			email: "lars.ferner@louis.de", wantFull: "Lars Ferner",
			wantFirst: "Lars", wantLast: "Ferner", wantConfide: true,
		},
		{
			name:  "trailing digits are a handle's tail, not part of the surname",
			email: "charlotte.nguyen2016@gmail.com", wantFull: "Charlotte Nguyen",
			wantFirst: "Charlotte", wantLast: "Nguyen", wantConfide: true,
		},
		{
			name:    "a surname-first display name is reversed and its header quotes dropped",
			display: `"Lienesch, André"`, email: "andre.lienesch@louis.de",
			wantFull: "André Lienesch", wantFirst: "André", wantLast: "Lienesch", wantConfide: true,
		},
		{
			name:  "a lone surname names the person but is not split",
			email: "schluepmann@k5-gmbh.com", wantFull: "Schluepmann",
		},
		{
			name:  "a role mailbox names nobody",
			email: "mail@petereich.com", wantFull: "mail",
		},
		{
			name:    "an employer suffixed onto the display name is not a surname",
			display: "Sven Rittau | K5", email: "sven@k5.de",
			wantFull: "Sven Rittau", wantFirst: "Sven", wantLast: "Rittau", wantConfide: true,
		},
		{
			name:    "a parenthesised affiliation is dropped the same way",
			display: "Andreas Stegmann (NFQ)", email: "andreas@nfq.com",
			wantFull: "Andreas Stegmann", wantFirst: "Andreas", wantLast: "Stegmann", wantConfide: true,
		},
		{
			name:    "an honorific is lifted off and never becomes part of the name",
			display: "Dr. Anna Weber", email: "anna.weber@example.com",
			wantFull: "Anna Weber", wantFirst: "Anna", wantLast: "Weber",
			wantHonor: "Dr.", wantConfide: true,
		},
		{
			name:    "a particle binds to the surname it belongs to",
			display: "Ludwig van Beethoven", email: "lvb@example.com",
			wantFull: "Ludwig van Beethoven", wantFirst: "Ludwig", wantLast: "van Beethoven",
			wantConfide: true,
		},
		{
			name:    "a surname that is only a particle phrase has no first name to report",
			display: "van Dijk", email: "vandijk@example.com", wantFull: "van Dijk",
		},
		{
			name:  "plus-addressing is a routing tag, not a name",
			email: "anna.weber+crm@example.com", wantFull: "Anna Weber",
			wantFirst: "Anna", wantLast: "Weber", wantConfide: true,
		},
		{
			name:    "a chosen inner-capital spelling is preserved",
			display: "Ronan McDonald", email: "r@example.com",
			wantFull: "Ronan McDonald", wantFirst: "Ronan", wantLast: "McDonald", wantConfide: true,
		},
		{
			name:  "hyphenated and apostrophed names capitalize every part",
			email: "anne-marie.o'brien@example.com", wantFull: "Anne-Marie O'Brien",
			wantFirst: "Anne-Marie", wantLast: "O'Brien", wantConfide: true,
		},
		{
			name:    "a department prefix makes the string too long to read as a name",
			display: "MKT-Quynh.Vo Ngoc Nhu Thi Anh", email: "mkt@example.com",
			wantFull: "MKT-Quynh.Vo Ngoc Nhu Thi Anh",
		},
		{
			name:  "a digits-only local part is not a name",
			email: "2016@example.com", wantFull: "2016",
		},
		{
			name:  "an address with no local part falls back to the raw string",
			email: "nobody", wantFull: "nobody",
		},
		{
			name:    "a bare honorific names nobody",
			display: "Dr.", email: "d@example.com", wantFull: "Dr.",
		},
		{
			name:    "three plain tokens are ambiguous and are not split",
			display: "Anna Maria Weber", email: "amw@example.com",
			wantFull: "Anna Maria Weber",
		},
		{
			name:    "a second comma means the tail is a credential, not a given name",
			display: "Weber, Anna, PhD", email: "aw@example.com",
			wantFull: "Weber, Anna, PhD",
		},
		{
			name:    "a post-nominal after one comma is dropped, not read as a given name",
			display: "Anna Weber, PhD", email: "aw2@example.com",
			wantFull: "Anna Weber", wantFirst: "Anna", wantLast: "Weber", wantConfide: true,
		},
		{
			name:  "a departmental mailbox is a role address, not two people",
			email: "support.eu@example.com", wantFull: "support.eu",
		},
		{
			name:    "punctuation is not a surname",
			display: "Alice -", email: "a@example.com", wantFull: "Alice -",
		},
		{
			name:    "a name mixing Latin with a look-alike script is never claimed as known",
			display: "Аlice Smith", email: "as@example.com", wantFull: "Аlice Smith",
		},
		{
			name:    "bidi overrides are stripped so the stored name cannot render as another",
			display: "Alice \u202ehtimS\u202c", email: "bidi@example.com",
			wantFull: "Alice htimS", wantFirst: "Alice", wantLast: "htimS", wantConfide: true,
		},
		{
			name:  "a handle with digits in the middle is not split",
			email: "user2name@example.com", wantFull: "user2name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParsePersonName(tc.display, tc.email)
			if got.Full != tc.wantFull {
				t.Errorf("Full = %q, want %q", got.Full, tc.wantFull)
			}
			if got.First != tc.wantFirst {
				t.Errorf("First = %q, want %q", got.First, tc.wantFirst)
			}
			if got.Last != tc.wantLast {
				t.Errorf("Last = %q, want %q", got.Last, tc.wantLast)
			}
			if got.Honorific != tc.wantHonor {
				t.Errorf("Honorific = %q, want %q", got.Honorific, tc.wantHonor)
			}
			if got.Confident != tc.wantConfide {
				t.Errorf("Confident = %v, want %v", got.Confident, tc.wantConfide)
			}
		})
	}
}

// full_name is NOT NULL, so no input may parse to an empty display string.
func TestParsePersonNameAlwaysProducesADisplayableName(t *testing.T) {
	t.Parallel()
	for _, in := range []struct{ display, email string }{
		{display: "", email: "a@b.com"},
		{display: "", email: ""},
		{display: "   ", email: "  "},
		{display: `""`, email: "x@y.com"},
		{display: "|", email: "z@z.com"},
		{display: "...", email: "...@example.com"},
		{display: "", email: "@example.com"},
	} {
		if got := ParsePersonName(in.display, in.email); got.Full == "" {
			t.Errorf("ParsePersonName(%q, %q) produced an empty Full", in.display, in.email)
		}
	}
}

// A display name is untrusted header text of the sender's choosing. The parser
// must bound it before splitting, or a multi-megabyte value would be tokenized,
// rejoined and handed on to dedupe and the database.
func TestParsePersonNameRefusesAnOversizedHeader(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("A ", 200_000)
	got := ParsePersonName(huge, "real.person@example.com")
	if len([]rune(got.Full)) > maxNameInputRunes {
		t.Errorf("Full is %d runes, want at most %d", len([]rune(got.Full)), maxNameInputRunes)
	}
	// The oversized header is discarded, so the address still names the person.
	if got.First != "Real" || got.Last != "Person" {
		t.Errorf("got %q %q, want the address read instead of the huge header", got.First, got.Last)
	}
	if long := ParsePersonName("", strings.Repeat("a", 5_000)+"@example.com"); len([]rune(long.Full)) > maxNameInputRunes {
		t.Errorf("oversized address produced %d runes, want at most %d", len([]rune(long.Full)), maxNameInputRunes)
	}
}

// A name is reported only as a PAIR: half a reading is not a name, and the
// split columns must never carry one without the other.
func TestParsePersonNameNeverReportsHalfAName(t *testing.T) {
	t.Parallel()
	for _, in := range []struct{ display, email string }{
		{display: "", email: "schluepmann@k5-gmbh.com"},
		{display: "", email: "info@acme.com"},
		{display: "van Dijk", email: "v@d.com"},
		{display: "", email: "lars.ferner@louis.de"},
		{display: "Dr. Anna Weber", email: "a@w.com"},
		{display: "", email: "2016@example.com"},
	} {
		got := ParsePersonName(in.display, in.email)
		if (got.First == "") != (got.Last == "") {
			t.Errorf("ParsePersonName(%q, %q) = first %q last %q: one set without the other",
				in.display, in.email, got.First, got.Last)
		}
		if got.Confident != (got.First != "" && got.Last != "") {
			t.Errorf("ParsePersonName(%q, %q): Confident=%v disagrees with first %q last %q",
				in.display, in.email, got.Confident, got.First, got.Last)
		}
	}
}
