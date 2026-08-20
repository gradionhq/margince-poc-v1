// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The transport directory's non-transport half, from the shaping up to the
// response the handler writes.
//
// The invariant everything here serves: the id the directory PUBLISHES is
// byte-identical to the one the ingress runtime STAMPS on every record it lands.
// They are different code paths reaching for the same string, and a mismatch
// fails nothing at runtime — a directory miss is a fallback, not an error — so
// the ids would drift silently and every one of a unit's rows would go back to
// showing raw provenance. The first test asserts it by running both.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The contract's own `CaptureSourceEntry.source` pattern. Restated rather than
// read from the YAML because a published id the contract's schema would reject is
// the failure, and a test deriving the pattern from the document it checks
// against would only ever agree with itself.
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
	// lands. Run rather than asserted against a literal, so a change to the
	// grammar moves both halves or fails here.
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

// Every id the boot can ADMIT satisfies the pattern the contract publishes, and
// the bound is derived from the two grammars rather than sampled from a few
// hand-written units.
//
// The consequence of getting this wrong is not a bad row: a conforming client may
// refuse the whole response over one entry that fails the schema, which would
// blank every timeline in the installation over a unit's declaration. So the
// question is not "do today's units pass" but "can anything boot admits fail".
func TestNoDeclarationBootAdmitsCanBreakThePublishedPattern(t *testing.T) {
	// The extremes of `IngressSource.Validate` and `Name.Validate`: both are
	// `^[a-z0-9]+(-[a-z0-9]+)*$` capped at 32, so the widest legal id is two
	// 32-character keys, and the narrowest is two single characters.
	const longest = "a234567890123456789012345678901c" // 32, ends alphanumeric
	for _, unit := range []extension.Extension{
		unitWithIngress(longest, longest),
		unitWithIngress("a", "0"),
		unitWithIngress("a-b", "0-9-z"),
	} {
		if err := unit.Ingress[0].Validate(); err != nil {
			t.Fatalf("this test's own fixture is not a declaration boot would admit: %v", err)
		}
		if err := unit.Name.Validate(); err != nil {
			t.Fatalf("this test's own fixture carries a name boot would refuse: %v", err)
		}
		published := publishedCaptureSources([]extension.Extension{unit})
		if published == nil {
			t.Fatalf("%q published nothing", unit.Name)
		}
		for _, entry := range *published {
			if !capturedSourceGrammar.MatchString(entry.Source) {
				t.Errorf("%q fails the contract's own source pattern, so a conforming client may refuse the whole directory", entry.Source)
			}
			if len(entry.Source) > 96 {
				t.Errorf("%q is %d bytes, past the contract's maxLength of 96", entry.Source, len(entry.Source))
			}
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

// The handler reads the COMPOSED set and serves the field.
//
// Everything above shapes a slice the caller hands in, which leaves the one line
// the whole seam rides on — `publishedCaptureSources(ComposedExtensions())` —
// asserted by nothing: drop it, or narrow the set it reads to units that also
// declare a channel, and every other test here stays green while a unit's rows go
// back to showing raw provenance. So this one goes through the real wiring
// (`composeUnit`) and the real handler, and reads the response a client gets.
func TestTheDirectoryServesTheComposedUnitsCaptureSources(t *testing.T) {
	composeUnit(t, unitWithIngress("probe-unit", "probe-system"))

	recorder := httptest.NewRecorder()
	channelProvidersHandlers{}.ListChannelProviders(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v1/channel-providers", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("the directory answered %d", recorder.Code)
	}

	// Decoded off the wire rather than off the struct: the field is optional, so
	// a shaping that returned nil and a handler that dropped the key are the same
	// bug from a client's side, and only the bytes tell them apart.
	var body struct {
		CaptureSources []struct {
			Source string `json:"source"`
			Label  string `json:"label"`
		} `json:"capture_sources"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the directory: %v", err)
	}
	want := (&callRuntime{unit: "probe-unit"}).sourceSystem("probe-system")
	for _, entry := range body.CaptureSources {
		if entry.Source == want {
			if entry.Label == "" {
				t.Errorf("%q reached the wire with no label", want)
			}
			return
		}
	}
	t.Errorf("the composed unit's %q is not in the directory the handler served: %+v", want, body.CaptureSources)
}
