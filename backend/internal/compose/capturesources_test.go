// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The transport directory's non-transport half: the provenance ids a unit's
// landed records carry, published with a name.
//
// The defect these hold shut is one transport under two spellings. A unit's
// channel messages resolve to "Dispact" from the registry, while the same
// unit's mail — landed on no channel — carries the natural key's source system
// and reached a member as the raw `ext:dispact-connector:dispact`. The fix is
// only a fix if the id the directory publishes is BYTE-IDENTICAL to the one the
// ingress runtime stamps, which is what the first test asserts by running both.

import (
	"regexp"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The contract's own `CaptureSourceEntry.source` pattern. Restated here rather
// than read from the YAML because a published id that the contract's schema
// would reject is the failure, and a test that derived the pattern from the same
// document it is checking against would agree with itself.
var capturedSourceGrammar = regexp.MustCompile(`^[a-z][a-z0-9_:.-]*$`)

func unitWithIngress(name string, systems ...string) extension.Extension {
	sources := make([]extension.IngressSource, 0, len(systems))
	for _, system := range systems {
		sources = append(sources, extension.IngressSource{
			System: system,
			Lands:  []extension.RecordKind{extension.KindActivity},
		})
	}
	return extension.Extension{Name: extension.Name(name), Ingress: sources}
}

func TestThePublishedSourceIsTheIdEveryLandedRecordCarries(t *testing.T) {
	// The runtime side is the WRITER: this is the value that goes onto the
	// natural key and onto `capture_trace.connector` for every record the unit
	// lands. Constructed here rather than asserted against a literal, so a
	// change to the grammar moves both halves or fails.
	runtime := &callRuntime{unit: "dispact-connector"}
	published := publishedCaptureSources([]extension.Extension{
		unitWithIngress("dispact-connector", "dispact"),
	})
	if published == nil {
		t.Fatal("a unit declaring an ingress source published nothing; its records would keep reaching members raw")
	}
	entries := *published
	if len(entries) != 1 {
		t.Fatalf("one declared source published %d entries", len(entries))
	}
	if want := runtime.sourceSystem("dispact"); entries[0].Source != want {
		t.Errorf("the directory publishes %q and the ingress runtime writes %q; a label keyed on one never resolves the other",
			entries[0].Source, want)
	}
	if entries[0].Label == "" {
		t.Error("published with no label, which leaves the raw id as the only thing a member can be shown")
	}
}

func TestEveryDeclaredSourceIsPublished(t *testing.T) {
	// Two units, one of them with two sources, so the test can only pass by
	// walking both levels. A capture-only unit — no Channels — is deliberately
	// among them: it is the case with no transport entry standing beside it,
	// which makes it the one that most needs a name of its own.
	units := []extension.Extension{
		unitWithIngress("zalo-oa", "zalo-oa"),
		unitWithIngress("dispact-connector", "dispact", "dispact-mail"),
	}
	published := publishedCaptureSources(units)
	if published == nil {
		t.Fatal("three declared sources published nothing")
	}
	entries := *published

	byID := map[string]string{}
	for _, entry := range entries {
		byID[entry.Source] = entry.Label
		if !capturedSourceGrammar.MatchString(entry.Source) {
			t.Errorf("%q does not match the contract's own source pattern, so a conforming client may refuse the whole directory", entry.Source)
		}
	}
	for _, unit := range units {
		for _, source := range unit.Ingress {
			id := (&callRuntime{unit: string(unit.Name)}).sourceSystem(source.System)
			label, ok := byID[id]
			if !ok {
				t.Errorf("%s declares %q and the directory does not publish it", unit.Name, source.System)
				continue
			}
			if label == "" {
				t.Errorf("%q is published with no label", id)
			}
		}
	}

	// Sorted, so a diff of two deployments' directories is readable. Asserted on
	// the answer rather than on the sort call, because the reason is the output.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Source >= entries[i].Source {
			t.Errorf("published out of order: %q before %q", entries[i-1].Source, entries[i].Source)
		}
	}
}

// Absent, not empty. The field is optional on the wire and the contract says an
// empty answer and no answer mean the same thing; publishing `[]` would state
// that in one more shape for every client to handle, and a vanilla installation
// composing no ingress unit is the common case rather than the edge one.
func TestAnInstallationWithNoIngressPublishesNothing(t *testing.T) {
	if got := publishedCaptureSources(nil); got != nil {
		t.Errorf("no composed unit published %v", *got)
	}
	unitWithNoIngress := extension.Extension{Name: "notes"}
	if got := publishedCaptureSources([]extension.Extension{unitWithNoIngress}); got != nil {
		t.Errorf("a unit declaring no ingress published %v", *got)
	}
}

// A name a human reads, from an id a machine keys on. Both separators are
// exercised because the two ids that reach this function use one each: a
// provider id is snake and an ingress system key is kebab, and a label that
// title-cased only one of them would print `Zalo-oa` beside `Zalo Oa` for what
// is one transport seen from two sides.
func TestAnIdBecomesWordsWhicheverSeparatorItUses(t *testing.T) {
	for id, want := range map[string]string{
		"dispact":      "Dispact",
		"zalo_oa":      "Zalo Oa",
		"zalo-oa":      "Zalo Oa",
		"dispact-mail": "Dispact Mail",
		"deal_room":    "Deal Room",
		"a":            "A",
	} {
		if got := titleCasedID(id); got != want {
			t.Errorf("titleCasedID(%q) = %q, want %q", id, got, want)
		}
	}
}
