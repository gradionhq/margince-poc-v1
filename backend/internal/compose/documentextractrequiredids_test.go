// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The accept mapping's own obligation: refuse an extraction_id the caller did
// not send, rather than letting the zero UUID reach the reading lookup and come
// back as "extraction reading 00000000-… does not exist" — a 404 about a record
// the request never named, which reads to a client as "the reading is gone"
// when what happened is "you left the field out".

import (
	"errors"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestEveryRequiredBodyIDIsNamedWhenAbsent(t *testing.T) {
	_, err := acceptedReadingID(crmcontracts.AcceptExtractionRequest{
		FieldKeys: []string{"amount_minor"},
	})
	if err == nil {
		t.Fatal("an omitted extraction_id was accepted; the zero UUID would reach the reading lookup")
	}
	var refused *ExtractionAcceptError
	if !errors.As(err, &refused) {
		t.Fatalf("refusal is %T, want an ExtractionAcceptError naming extraction_id", err)
	}
	if refused.Field != "extraction_id" || refused.Code != "required" {
		t.Errorf("refusal names %s/%s, want extraction_id/required", refused.Field, refused.Code)
	}
}

// A real id passes through unchanged. Without this the guard could refuse
// everything and the test above would still be green.
func TestANamedReadingIDReachesTheLookup(t *testing.T) {
	want := ids.NewV7()
	got, err := acceptedReadingID(crmcontracts.AcceptExtractionRequest{
		ExtractionId: openapi_types.UUID(want), FieldKeys: []string{"amount_minor"},
	})
	if err != nil {
		t.Fatalf("a named reading id was refused: %v", err)
	}
	if got != want {
		t.Errorf("reading id = %s, want %s", got, want)
	}
}
