// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"testing"
)

// A closed vocabulary that refuses a value must say which values it accepts.
// Naming only the field leaves a caller unable to tell a casing slip from a
// wrong word — a UAT agent recovered from "Customer" only because an earlier
// read happened to show the lowercase form in its payload.
func TestEnumRefusalsCarryTheVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		valid map[string]bool
	}{
		{"lifecycle", checkLifecycle("Customer"), validLifecycles},
		{"size_band", checkSizeBand("banana"), validSizeBands},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("a value outside the vocabulary was accepted")
			}
			message := tc.err.Error()
			if !strings.Contains(message, "expected one of:") {
				t.Errorf("the refusal names no vocabulary: %s", message)
			}
			for value := range tc.valid {
				if !strings.Contains(message, value) {
					t.Errorf("the refusal omits the legal value %q: %s", value, message)
				}
			}
		})
	}
}

// Every legal value is still accepted — a refusal that also refuses the fix is
// worse than the one it replaced.
func TestEveryValueTheVocabularyNamesIsAccepted(t *testing.T) {
	for value := range validSizeBands {
		if err := checkSizeBand(value); err != nil {
			t.Errorf("size_band %q is in the vocabulary and was refused: %v", value, err)
		}
	}
	for value := range validLifecycles {
		if err := checkLifecycle(value); err != nil {
			t.Errorf("lifecycle %q is in the vocabulary and was refused: %v", value, err)
		}
	}
}
