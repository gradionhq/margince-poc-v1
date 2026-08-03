// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package provenance_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// The importer's namespace is a security boundary, not a convention: a
// caller who can write it can pre-plant a row under an incumbent record
// id and have a later import treat the real record as already landed.
func TestReservedSourceSystem(t *testing.T) {
	for _, reserved := range []string{"mirror:hubspot", "mirror:salesforce", "mirror:"} {
		if !provenance.ReservedSourceSystem(reserved) {
			t.Errorf("%q must be refused from a client write", reserved)
		}
	}
	for _, allowed := range []string{"hubspot", "gmail", "", "notmirror:hubspot", "MIRROR:hubspot"} {
		if provenance.ReservedSourceSystem(allowed) {
			t.Errorf("%q is an ordinary source system and must stay writable", allowed)
		}
	}
}

func TestRefuseNamesTheFieldAndLetsOrdinaryValuesThrough(t *testing.T) {
	err := provenance.Refuse("source", provenance.ReservedSourceSystemPrefix+"hubspot:person:p-1")
	var reserved *provenance.ReservedError
	if !errors.As(err, &reserved) {
		t.Fatalf("err = %v, want ReservedError — a client write into the import namespace must be refused", err)
	}
	if reserved.Field != "source" {
		t.Errorf("refusal names field %q, want the one it arrived on; the caller has to know which to change", reserved.Field)
	}
	if !strings.Contains(reserved.Error(), "source") || !strings.Contains(reserved.Error(), "reserved") {
		t.Errorf("message %q says neither the field nor why", reserved.Error())
	}
	// The guard is a prefix rule, not a ban: ordinary provenance — and an
	// empty one — stay writable, or every create wire would break.
	for _, ordinary := range []string{"", "hubspot", "hubspot:person:p-1", "mirrorless"} {
		if err := provenance.Refuse("source", ordinary); err != nil {
			t.Errorf("Refuse(%q) = %v, want nil", ordinary, err)
		}
	}
}

func TestReservedErrorStatesItselfAsCallerFixable(t *testing.T) {
	// Implementing apperrors.FieldFault is what carries this refusal to
	// the caller as a 422 naming the field — on the HTTP surface AND on
	// the MCP tool surface, neither of which knows this type. Without it
	// the refusal degrades to an opaque internal fault telling the caller
	// to retry something that will never succeed.
	var fault apperrors.FieldFault = &provenance.ReservedError{
		Field: "source", Value: "mirror:hubspot:person:p-1",
	}
	field, code, message := fault.FieldFault()
	if field != "source" {
		t.Errorf("field = %q, want the one the value arrived on", field)
	}
	if code != "reserved_source_system" {
		t.Errorf("code = %q, want the contract's machine code", code)
	}
	if !strings.Contains(message, "reserved") || !strings.Contains(message, provenance.ReservedSourceSystemPrefix) {
		t.Errorf("message %q must say what is wrong and which namespace to avoid", message)
	}
}
