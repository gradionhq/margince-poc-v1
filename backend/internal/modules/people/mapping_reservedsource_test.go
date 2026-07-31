// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The importer's source-system namespace is a security boundary, and
// this mapper is where it is enforced: the lead store keys its
// idempotent replay on (source_system, source_id), so a client able to
// spell the reserved prefix could pre-plant a row under an incumbent
// record id and have a later import hand it back as already existing —
// suppressing the real record.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestLeadCreateInputRefusesTheImporterNamespace(t *testing.T) {
	reserved := "mirror:hubspot"
	_, err := leadCreateInput(crmcontracts.CreateLeadRequest{
		SourceSystem: &reserved, SourceId: ptr("501"),
	})
	var refused *ReservedSourceSystemError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want ReservedSourceSystemError — a client must not write the importer's namespace", err)
	}
	if refused.Value != reserved {
		t.Errorf("refusal names %q, want the offending value", refused.Value)
	}
}

func TestLeadCreateInputAcceptsAnOrdinarySourceSystem(t *testing.T) {
	ordinary := "hubspot"
	in, err := leadCreateInput(crmcontracts.CreateLeadRequest{
		SourceSystem: &ordinary, SourceId: ptr("501"),
	})
	if err != nil {
		t.Fatalf("an ordinary source system must stay writable: %v", err)
	}
	if in.SourceSystem == nil || *in.SourceSystem != ordinary {
		t.Errorf("SourceSystem = %v, want it carried through", in.SourceSystem)
	}
}

func ptr(s string) *string { return &s }
