// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The importer's provenance namespace is a security boundary, and these
// mappers are where it is enforced. Two distinct things depend on it:
// the lead store keys its idempotent replay on (source_system,
// source_id), so a client able to spell the reserved prefix could
// pre-plant a row under an incumbent record id and have a later import
// hand it back as already existing; and the flip's crash repair reads
// `source` back to recognize records it created but had not yet mapped,
// which is only safe while nothing else can write that prefix.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

func TestLeadCreateInputRefusesTheImporterNamespace(t *testing.T) {
	reserved := "mirror:hubspot"
	_, err := leadCreateInput(crmcontracts.CreateLeadRequest{
		SourceSystem: &reserved, SourceId: ptr("501"),
	})
	var refused *provenance.ReservedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want provenance.ReservedError — a client must not write the importer's namespace", err)
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

// EVERY create wire that accepts provenance refuses the namespace. The
// flip stamps `source` inside it on persons, organizations and deals —
// classes with no (source_system, source_id) replay key — so a gap in
// any one of these lets a planted row be adopted as the importer's own.
func TestEveryProvenanceWireRefusesTheImporterNamespace(t *testing.T) {
	const reserved = "mirror:hubspot:person:p-1"
	var refused *provenance.ReservedError

	if _, err := personCreateInput(crmcontracts.CreatePersonRequest{
		FullName: "Planted", Source: reserved,
	}); !errors.As(err, &refused) {
		t.Errorf("person: err = %v, want the namespace refused", err)
	}
	if _, err := organizationCreateInput(crmcontracts.CreateOrganizationRequest{
		DisplayName: "Planted", Source: reserved,
	}); !errors.As(err, &refused) {
		t.Errorf("organization: err = %v, want the namespace refused", err)
	}
	if _, err := leadCreateInput(crmcontracts.CreateLeadRequest{
		FullName: ptr("Planted"), Source: reserved,
	}); !errors.As(err, &refused) {
		t.Errorf("lead source: err = %v, want the namespace refused", err)
	}

	// The refusal names the field it arrived on, because the two are
	// different wire fields and the caller has to know which to change.
	if refused != nil && refused.Field != "source" {
		t.Errorf("refusal names field %q, want source", refused.Field)
	}

	// An ordinary provenance string stays writable — the guard is a
	// prefix rule, not a ban on the field.
	if _, err := personCreateInput(crmcontracts.CreatePersonRequest{
		FullName: "Real", Source: "hubspot:person:p-1",
	}); err != nil {
		t.Errorf("an ordinary source must stay writable: %v", err)
	}
}

func ptr(s string) *string { return &s }
