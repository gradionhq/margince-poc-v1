// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What a signature line contributes before anything is written.

import "testing"

// person_phone.phone is E.164 by contract and no database constraint holds it,
// so the ONLY thing making that contract true for this writer is
// values.ParsePhone. A signature states a number however its author types it.
func TestASignaturePhoneIsNormalizedOrDeclined(t *testing.T) {
	for name, tc := range map[string]struct {
		raw      string
		want     string
		readable bool
	}{
		"separators are formatting":        {"+49 (30) 1234-5678", "+493012345678", true},
		"00 is the dialled form of +":      {"0049 30 12345678", "+493012345678", true},
		"already normalized":               {"+493012345678", "+493012345678", true},
		"no country prefix is unreachable": {"030 12345678", "", false},
		"a word is not a number":           {"call me", "", false},
		"empty contributes nothing":        {"   ", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, readable := readSignatureValue(SignatureField{Name: "phone", Value: tc.raw})
			if readable != tc.readable {
				t.Fatalf("readable = %v, want %v (value %q)", readable, tc.readable, got)
			}
			if got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every other field is text the record carries as written; only trimming
// applies. Parsing them would silently rewrite a job title.
func TestANonPhoneSignatureFieldIsOnlyTrimmed(t *testing.T) {
	got, readable := readSignatureValue(SignatureField{Name: "title", Value: "  Head of Sales  "})
	if !readable || got != "Head of Sales" {
		t.Errorf("readSignatureValue(title) = %q/%v, want %q/true", got, readable, "Head of Sales")
	}
}
