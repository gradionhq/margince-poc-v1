// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The bound on how many files one message may name, and what happens to a
// caller who names one twice.

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A send naming more files than a message may carry is refused BEFORE any of
// them is resolved.
//
// The contract's maxItems is documentation: nothing in this stack validates a
// request body against its schema, so this bound is the only thing between one
// request and a transaction per named id — thousands of them fit in a body well
// inside the chassis ceiling. Refused before the loop, so the cost of refusing
// does not scale with what was asked for.
func TestASendNamingMoreFilesThanAMessageMayCarryIsRefused(t *testing.T) {
	named := make([]ids.UUID, 0, maxAttachmentsPerSend+1)
	for range maxAttachmentsPerSend + 1 {
		named = append(named, ids.NewV7())
	}

	_, err := boundAttachmentIDs(named)

	var tooMany *TooManyAttachmentsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("resolveAttachments → %v, want TooManyAttachmentsError", err)
	}
	if tooMany.Named != maxAttachmentsPerSend+1 || tooMany.Limit != maxAttachmentsPerSend {
		t.Errorf("the refusal reports %+v, want %d named against a limit of %d",
			tooMany, maxAttachmentsPerSend+1, maxAttachmentsPerSend)
	}
	// The caller can only fix this by sending fewer, so the reason has to say so
	// and the field has to name what to shorten.
	field, code, message := tooMany.FieldFault()
	if field != "attachment_ids" || code != "too_many_attachments" {
		t.Errorf("field fault = %q/%q, want attachment_ids/too_many_attachments", field, code)
	}
	if !strings.Contains(message, "second message") {
		t.Errorf("the refusal %q does not tell the caller what to do instead", message)
	}
}

// A file named twice counts once.
//
// Not politeness: counting the repeats would refuse a legitimate send while
// admitting the same total work under a shorter list, so the cap has to apply to
// what the message actually carries. And a message carrying one file twice is
// not something a message can mean — a recipient would see the same document in
// two parts.
func TestNamingOneFileTwiceCountsItOnce(t *testing.T) {
	one := ids.NewV7()
	named := make([]ids.UUID, 0, maxAttachmentsPerSend*3)
	for range maxAttachmentsPerSend * 3 {
		named = append(named, one)
	}

	got, err := boundAttachmentIDs(named)
	if err != nil {
		t.Fatalf("one file named %d times was refused: %v", maxAttachmentsPerSend*3, err)
	}
	if len(got) != 1 || got[0] != one {
		t.Errorf("the bounded set is %v, want the one file named once — a repeat reaching the resolve "+
			"is a second transaction and a second part in the message", got)
	}
}

// The ordinary case, asserted so the guard cannot pass by refusing everything:
// a set within the cap comes back whole and in the order it was given, because
// that order is what the recipient sees.
func TestASetWithinTheCapComesBackInOrder(t *testing.T) {
	named := []ids.UUID{ids.NewV7(), ids.NewV7(), ids.NewV7()}
	got, err := boundAttachmentIDs(named)
	if err != nil {
		t.Fatalf("a set of %d within the cap of %d was refused: %v", len(named), maxAttachmentsPerSend, err)
	}
	if len(got) != len(named) {
		t.Fatalf("the bounded set holds %d of %d files", len(got), len(named))
	}
	for i := range named {
		if got[i] != named[i] {
			t.Errorf("file %d is %v, want %v — the order the caller attached them in is the order they arrive", i, got[i], named[i])
		}
	}
}
