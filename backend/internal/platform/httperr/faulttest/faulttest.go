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

// AssertNamesOmittedID is AssertNamesField for the specific case of a required
// body id the caller left out. It adds the one thing that case owes on top: the
// status must be 422 and not the 404 it used to be, because a not-found sends the
// caller looking for a record they never named.
func AssertNamesOmittedID(t *testing.T, err error, field string) {
	t.Helper()
	AssertNamesField(t, err, field)
	if err == nil {
		return
	}
	if fault, ok := httperr.Classify(err); ok && fault.Status == http.StatusNotFound {
		t.Errorf("an omitted %s answered 404 — that is the defect this guard closes, not the fix", field)
	}
}
