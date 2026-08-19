// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The record types a grant may name are spelled in two places a client can
// reach — this allowlist and the contract enum — and one it cannot, the
// record_grant CHECK. A type the contract admits and this map refuses is a
// share the UI offers and the service declines; the reverse is a share
// nobody can create. Either way the visibility predicate renders a grant arm
// for a row no grant can ever widen.
func TestTheGrantRecordTypesMatchTheContractEnum(t *testing.T) {
	for _, rt := range []crmcontracts.CreateRecordGrantRequestRecordType{
		crmcontracts.CreateRecordGrantRequestRecordTypeDeal,
		crmcontracts.CreateRecordGrantRequestRecordTypeLead,
		crmcontracts.CreateRecordGrantRequestRecordTypeOrganization,
		crmcontracts.CreateRecordGrantRequestRecordTypePerson,
		crmcontracts.CreateRecordGrantRequestRecordTypeProject,
	} {
		if !shareableRecordTypes[string(rt)] {
			t.Errorf("the contract admits record_type %q but CreateRecordGrant refuses it", rt)
		}
	}
	for rt := range shareableRecordTypes {
		if !crmcontracts.CreateRecordGrantRequestRecordType(rt).Valid() {
			t.Errorf("CreateRecordGrant admits record_type %q but the contract enum does not name it", rt)
		}
	}
}
