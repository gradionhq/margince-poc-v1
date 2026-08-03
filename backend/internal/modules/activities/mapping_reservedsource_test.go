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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

func TestActivityLogInputRefusesTheImporterNamespace(t *testing.T) {
	reserved := "mirror:hubspot"
	_, err := activityLogInput(crmcontracts.CreateActivityRequest{
		Kind: "email", SourceSystem: &reserved, SourceId: strPtr("emails:900"),
	})
	var refused *provenance.ReservedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want provenance.ReservedError — a client must not write the importer's namespace", err)
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

// The `source` guard matters as much as source_system's: activity is one
// of the classes the crash repair scans by provenance, so a client that
// could write the namespace there could have a planted row adopted.
func TestActivityLogInputRefusesAReservedSource(t *testing.T) {
	_, err := activityLogInput(crmcontracts.CreateActivityRequest{
		Kind: "note", Source: "mirror:hubspot:activity:a-1",
	})
	var refused *provenance.ReservedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want provenance.ReservedError", err)
	}
	if refused.Field != "source" {
		t.Errorf("refusal names field %q, want source", refused.Field)
	}
}

func TestActivityLogInputAcceptsAnOrdinarySource(t *testing.T) {
	// The guard is a prefix rule, not a ban on the field: an ordinary
	// provenance string has to survive, or every capture connector that
	// stamps its own source would start failing at the wire.
	in, err := activityLogInput(crmcontracts.CreateActivityRequest{Kind: "note", Source: "webform"})
	if err != nil {
		t.Fatalf("an ordinary source must stay writable: %v", err)
	}
	if in.Source != "webform" {
		t.Errorf("Source = %q, want it carried through", in.Source)
	}
}
