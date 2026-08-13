// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// The attribution is added from the CONTEXT, so an ordinary core write — a
// person or an agent on the product's own surface — carries none. The absence
// is as load-bearing as the presence: it is what makes the member's presence
// mean "an extension wrote this".
func TestEvidenceCarriesNoAttributionOutsideAnExtension(t *testing.T) {
	evidence, err := withExtensionAttribution(context.Background(), map[string]any{"policy": "retention"})
	if err != nil {
		t.Fatalf("withExtensionAttribution: %v", err)
	}
	if _, stamped := evidence[provenance.ExtensionEvidenceKey]; stamped {
		t.Errorf("an ordinary write was attributed to an extension: %v", evidence)
	}
	if evidence["policy"] != "retention" {
		t.Errorf("the caller's own evidence did not survive: %v", evidence)
	}

	// Nil in, nil out: Audit's ordinary call passes no evidence at all, and a
	// map materialised here would write `{}` into a column that means "nothing
	// was recorded" when it is NULL.
	empty, err := withExtensionAttribution(context.Background(), nil)
	if err != nil {
		t.Fatalf("withExtensionAttribution: %v", err)
	}
	if empty != nil {
		t.Errorf("evidence = %v, want nil for a write that recorded none", empty)
	}
}

// Inside an extension invocation every core write carries the unit, and the
// caller's own members are untouched beside it.
func TestEvidenceCarriesTheBoundAttribution(t *testing.T) {
	ctx := provenance.WithExtension(context.Background(), provenance.Extension{
		Unit: "notes", Version: "1.0.0", Via: "tool/file_note",
	})
	evidence, err := withExtensionAttribution(ctx, map[string]any{"policy": "retention"})
	if err != nil {
		t.Fatalf("withExtensionAttribution: %v", err)
	}
	entry, stamped := evidence[provenance.ExtensionEvidenceKey].(map[string]any)
	if !stamped {
		t.Fatalf("the write is not attributed: %v", evidence)
	}
	for member, want := range map[string]string{"unit": "notes", "version": "1.0.0", "via": "tool/file_note"} {
		if entry[member] != want {
			t.Errorf("%s = %v, want %q", member, entry[member], want)
		}
	}
	if evidence["policy"] != "retention" {
		t.Errorf("the caller's own evidence did not survive beside it: %v", evidence)
	}
}

// A caller that supplies the reserved member is an ERROR, in both directions
// and for the same reason: whichever value won, the loss would be silent. One
// of them is a module's own record of what it did; the other is the answer to
// "did an extension write this row", which nothing else can reconstruct.
func TestACallerMayNotSupplyTheReservedMember(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"inside an extension invocation": provenance.WithExtension(context.Background(),
			provenance.Extension{Unit: "notes", Version: "1.0.0", Via: "tool/file_note"}),
		"outside one": context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := withExtensionAttribution(ctx, map[string]any{
				provenance.ExtensionEvidenceKey: map[string]any{"unit": "somebody-else"},
			})
			if err == nil {
				t.Fatal("the reserved member was accepted from a caller")
			}
			if !strings.Contains(err.Error(), provenance.ExtensionEvidenceKey) {
				t.Errorf("the refusal does not name the member: %v", err)
			}
		})
	}
}
