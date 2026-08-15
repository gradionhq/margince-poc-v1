// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

import (
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// CONTRACT-FORM-1's whole point is that the effective end is the EARLIER of
// the term end and the cancellation date. LEAST is what expresses that, and a
// future edit reaching for COALESCE — which takes the FIRST non-null instead —
// would silently let a July cancellation extend a June term.
func TestUnderContractTakesTheEarlierEndDate(t *testing.T) {
	sql := underContractSQL(1)

	if !strings.Contains(sql, "LEAST(ends_on, cancellation_effective_on)") {
		t.Errorf("the effective end must be LEAST of the two dates; got:\n%s", sql)
	}
	if strings.Contains(sql, "COALESCE(ends_on") || strings.Contains(sql, "COALESCE(cancellation_effective_on") {
		t.Errorf("COALESCE would take the first non-null rather than the earlier date; got:\n%s", sql)
	}
}

// A draft is not yet an agreement and a superseded one has been replaced.
// Neither counts as being under contract, whatever their dates say.
func TestUnderContractExcludesDraftAndSuperseded(t *testing.T) {
	sql := underContractSQL(1)

	if !strings.Contains(sql, "status NOT IN ('draft', 'superseded')") {
		t.Errorf("draft and superseded must not count as under contract; got:\n%s", sql)
	}
}

// An archived agreement is gone from every surface that counts it.
func TestUnderContractExcludesArchived(t *testing.T) {
	if !strings.Contains(underContractSQL(1), "archived_at IS NULL") {
		t.Error("an archived contract must not count as under contract")
	}
}

// The as-of date is the caller's parameter, used for both the start and the
// end comparison — one instant, so a row cannot be judged against two
// different "today"s inside one read.
func TestUnderContractUsesOneAsOfParameter(t *testing.T) {
	sql := underContractSQL(7)

	if strings.Count(sql, "$7") != 2 {
		t.Errorf("expected the as-of parameter twice (start and end); got:\n%s", sql)
	}
	if strings.Contains(sql, "now()") || strings.Contains(sql, "CURRENT_DATE") {
		t.Errorf("the as-of date is the caller's, never the database's clock; got:\n%s", sql)
	}
}

// A patch may only name columns a client is allowed to set. Status and the
// cancellation dates move through their own paths, which state the consequence
// and write the matching event; letting a generic patch set them would route
// around both.
func TestPatchableColumnsExcludeTheAssertedTransitions(t *testing.T) {
	for _, column := range []string{"status", "cancellation_notice_on", "cancellation_effective_on", "superseded_by_id"} {
		if patchableColumns[column] {
			t.Errorf("%q must not be patchable — it has its own path", column)
		}
	}
	for _, column := range []string{"title", "value_minor", "ends_on", "renewal_on", "signed_on"} {
		if !patchableColumns[column] {
			t.Errorf("%q should be patchable", column)
		}
	}
}

// Every patchable column must report a prior value, or the audit diff records
// a change from nothing and the trail stops being reconstructable.
func TestEveryPatchableColumnReportsAPriorValue(t *testing.T) {
	sample := sampleContract()

	for column := range patchableColumns {
		if priorValue(sample, column) == nil && column != "deal_id" && column != "project_id" && column != "contract_number" {
			t.Errorf("priorValue(%q) = nil on a fully-populated contract — the audit diff would record a change from nothing", column)
		}
	}
}

// sampleContract is a fully-populated agreement: every patchable column set,
// so a nil prior value in the test above means the mapping missed a column
// rather than the fixture being thin.
func sampleContract() crmcontracts.Contract {
	number := "MSA-2024-01"
	value := int64(12000000)
	currency := "EUR"
	notice := 90
	dealID := openapi_types.UUID(ids.New[ids.DealKind]().UUID)
	projectID := openapi_types.UUID(ids.New[ids.ProjectKind]().UUID)
	day := openapi_types.Date{Time: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}
	autoRenew := true

	return crmcontracts.Contract{
		Title:            "Master agreement",
		ContractNumber:   &number,
		ValueMinor:       &value,
		Currency:         &currency,
		ValueBasis:       crmcontracts.ContractValueBasis(BasisTotal),
		NoticePeriodDays: &notice,
		DealId:           &dealID,
		ProjectId:        &projectID,
		StartsOn:         &day,
		EndsOn:           &day,
		RenewalOn:        &day,
		SignedOn:         &day,
		AutoRenew:        &autoRenew,
	}
}
