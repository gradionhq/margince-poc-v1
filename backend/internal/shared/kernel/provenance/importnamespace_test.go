// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package provenance_test

import (
	"testing"

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
