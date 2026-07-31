// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// See people/mapping_reservedsource_test.go: the activity store keys the
// same idempotent replay on (source_system, source_id), so the same
// boundary is enforced here and asserted the same way.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestActivityLogInputRefusesTheImporterNamespace(t *testing.T) {
	reserved := "mirror:hubspot"
	_, err := activityLogInput(crmcontracts.CreateActivityRequest{
		Kind: "email", SourceSystem: &reserved, SourceId: strPtr("emails:900"),
	})
	var refused *ReservedSourceSystemError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want ReservedSourceSystemError — a client must not write the importer's namespace", err)
	}
}

func TestActivityLogInputAcceptsAnOrdinarySourceSystem(t *testing.T) {
	ordinary := "gmail"
	in, err := activityLogInput(crmcontracts.CreateActivityRequest{
		Kind: "email", SourceSystem: &ordinary, SourceId: strPtr("msg-1"),
	})
	if err != nil {
		t.Fatalf("an ordinary source system must stay writable: %v", err)
	}
	if in.SourceSystem == nil || *in.SourceSystem != ordinary {
		t.Errorf("SourceSystem = %v, want it carried through", in.SourceSystem)
	}
}

func strPtr(s string) *string { return &s }
