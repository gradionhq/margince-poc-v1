// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// The wire mapping's own obligations: refuse an id the caller did not send,
// rather than letting the zero UUID reach a lookup and come back as "that
// person is not a contact on this account" — a refusal about a record the
// caller never named and cannot connect to anything they did.

import (
	"errors"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestEveryRequiredBodyIDIsNamedWhenAbsent(t *testing.T) {
	_, err := requestFrom(crmcontracts.DraftAccountEmailJSONRequestBody{})
	if err == nil {
		t.Fatal("an omitted person_id was accepted; the zero UUID would reach the contact lookup")
	}
	assertNamesField(t, err, "person_id")
}

// A null deal_id means "the account in general" and is an ordinary case. A
// present-but-zero one is a client bug, and answering "that deal is not open"
// about the nil UUID would hide it behind a plausible-sounding refusal.
func TestAPresentButZeroDealIDIsRefusedWhileAnAbsentOneIsFine(t *testing.T) {
	person := openapi_types.UUID(ids.NewV7())
	zero := openapi_types.UUID(ids.UUID{})

	_, err := requestFrom(crmcontracts.DraftAccountEmailJSONRequestBody{
		PersonId: person, DealId: &zero,
	})
	if err == nil {
		t.Fatal("a zero deal_id was accepted")
	}
	assertNamesField(t, err, "deal_id")

	req, err := requestFrom(crmcontracts.DraftAccountEmailJSONRequestBody{PersonId: person})
	if err != nil {
		t.Fatalf("an absent deal_id must be accepted as the whole account: %v", err)
	}
	if req.DealID != "" {
		t.Fatalf("an absent deal_id became %q, want the empty string", req.DealID)
	}
}

func assertNamesField(t *testing.T, err error, field string) {
	t.Helper()
	var validation *httperr.DetailedError
	if !errors.As(err, &validation) {
		t.Fatalf("refusal is %T, want a validation error naming %s", err, field)
	}
	for _, each := range validation.Fields {
		if each.Field == field {
			return
		}
	}
	t.Fatalf("refusal names %+v, want the field %s", validation.Fields, field)
}
