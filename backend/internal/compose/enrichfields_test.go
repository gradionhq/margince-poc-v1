// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The extraction vocabulary feeds the model prompt and the schema `field`
// enum; the gate accepts a field only if the contract's ColdStartField enum
// does (coldStartFieldValid). Pin that every name we advertise is contract-
// valid and unique, so a hand-listed entry can never drift ahead of the gate
// (the model would be told to emit a value the gate then silently drops).
func TestExtractionFieldNamesAreContractValidAndUnique(t *testing.T) {
	if len(extractionFieldNames) == 0 {
		t.Fatal("extractionFieldNames is empty")
	}
	seen := make(map[string]bool, len(extractionFieldNames))
	for _, name := range extractionFieldNames {
		if !crmcontracts.ColdStartFieldField(name).Valid() {
			t.Errorf("%q is not a valid ColdStartField — the enum would outrun the gate", name)
		}
		if seen[name] {
			t.Errorf("%q listed twice in extractionFieldNames", name)
		}
		seen[name] = true
	}
}
