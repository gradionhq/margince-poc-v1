// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"errors"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// A human may say what a document IS. They may not say where it CAME FROM.
//
// The two `*_attachment` values are the library's answer to "where did this come
// from", derived by capture from the message that carried the file. A hand
// upload came from the person uploading it, so asserting either on one mints a
// claim every later reader takes for a derived fact — and unlike a wrong title,
// nothing downstream can tell it from the real thing.
//
// The correction case is the reason this is gated on the ROW and not by narrowing
// the vocabulary: a captured file that landed under the wrong provenance is
// exactly what a patch is for.
func TestOnlyACapturedFileCanCarryAProvenanceCategory(t *testing.T) {
	for _, c := range []struct {
		name     string
		source   string
		category string
		refused  bool
	}{
		{"an upload claiming it arrived on a channel", "upload", "message_attachment", true},
		{"an upload claiming it arrived by mail", "upload", "email_attachment", true},
		{"an upload filed by what it is", "upload", "contract", false},
		{"an upload filed as other", "upload", "other", false},
		// A captured row may be corrected between the two — including onto the
		// value this change introduces, which is how a file captured before the
		// migration gets relabelled.
		{"a captured file corrected onto the channel value", "telegram", "message_attachment", false},
		{"a captured file corrected onto the mail value", "imap", "email_attachment", false},
		{"a captured file read as a contract", "imap", "contract", false},
	} {
		err := refuseAssertedProvenance(
			crmcontracts.Attachment{Source: c.source}, DocumentMetadata{Category: &c.category})
		if c.refused && err == nil {
			t.Errorf("%s: allowed, want refused", c.name)
		}
		if !c.refused && err != nil {
			t.Errorf("%s: refused (%v), want allowed", c.name, err)
		}
	}
	// A patch that never mentions the category cannot be refused by a rule about
	// categories — the guard must read "absent", not "empty".
	if err := refuseAssertedProvenance(
		crmcontracts.Attachment{Source: "upload"}, DocumentMetadata{}); err != nil {
		t.Errorf("a patch with no category was refused: %v", err)
	}
}

// The refusal says what to do instead, and carries the machine code the contract
// publishes — a client that shows the message to a person and a client that
// branches on the code both need it to be this one.
func TestTheProvenanceRefusalNamesTheFieldAndWhatToDo(t *testing.T) {
	category := "message_attachment"
	err := refuseAssertedProvenance(
		crmcontracts.Attachment{Source: "upload"}, DocumentMetadata{Category: &category})
	var refusal *values.ParseError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal = %#v, want a values.ParseError so it maps to a 422", err)
	}
	if refusal.Field != "category" || refusal.Code != "category_not_assertable" {
		t.Errorf("refusal field/code = %q/%q, want category/category_not_assertable",
			refusal.Field, refusal.Code)
	}
	if !strings.Contains(refusal.Message, "file it by what it is") {
		t.Errorf("refusal message = %q, want it to end with what the caller should do instead",
			refusal.Message)
	}
}
