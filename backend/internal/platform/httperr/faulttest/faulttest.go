// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package faulttest is the shared assertion for a refusal that must name the
// input at fault.
//
// It lives in the platform tier beside the taxonomy it reads, because the
// question it answers belongs to that taxonomy and not to any module: does this
// error classify, does it classify as the CALLER's mistake, and does it name the
// wire field they must change. Eight modules make refusals of that shape and each
// had its own copy of the check — so the rule was spelled once in production code
// and eight times in tests, which is one place to fix a defect and eight to
// forget.
package faulttest

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// AssertNamesField asserts that err is a 4xx the caller can act on, whose
// per-field breakdown names field.
//
// The OBSERVABLE property, not a concrete error type, and deliberately: there are
// several legitimate carriers — a module's own FieldFault error, httperr's
// Validation, httperr.RequireBodyID — and a caller cannot tell them apart, which
// is the whole point of the taxonomy. A test pinned to one carrier fails when a
// refusal is correctly re-homed.
func AssertNamesField(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("a request missing %s was accepted", field)
	}
	fault, ok := httperr.Classify(err)
	if !ok {
		t.Fatalf("the refusal for %s is %v, which is outside the taxonomy — every surface would report "+
			"the caller's own mistake as an internal server fault, with advice to retry a call the "+
			"server has already settled", field, err)
	}
	if fault.Status < 400 || fault.Status >= 500 {
		t.Errorf("the refusal for %s answers status %d, want a 4xx — this is the caller's mistake",
			field, fault.Status)
	}
	for _, refusal := range fault.Fields {
		if refusal.Field == field {
			return
		}
	}
	t.Errorf("the refusal for %s names %+v, want the wire field %q — the name is what a caller branches "+
		"on and what a model reads back", field, fault.Fields, field)
}

// AssertNamesOmittedID is AssertNamesField for a required body id the caller left
// out, and it pins the status EXACTLY rather than to the 4xx band.
//
// 422 is the claim: the body was well-formed and one required value was absent. A
// 404 is the specific answer this guard class exists to stop — it sends the caller
// looking for a record they never named — but a 400 or a 409 would be wrong too,
// and the local copies this replaced all asserted the exact code. A consolidation
// that widened the assertion would have quietly weakened six suites.
func AssertNamesOmittedID(t *testing.T, err error, field string) {
	t.Helper()
	AssertNamesField(t, err, field)
	fault, ok := httperr.Classify(err)
	if !ok {
		return // AssertNamesField has already failed the test on this
	}
	if fault.Status != http.StatusUnprocessableEntity {
		t.Errorf("an omitted %s answered status %d, want exactly 422 — the body is well-formed and one "+
			"required value is missing", field, fault.Status)
	}
}
