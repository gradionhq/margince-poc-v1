// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// enrich is the one tool on this surface that reaches the open internet, so
// what it accepts before it fetches is the part worth pinning: a depth it does
// not serve, and a target that is not an absolute http(s) URL. netguard refuses
// the private address ranges underneath; this refuses the arguments that never
// should have travelled at all.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestReadEnrichArgsDefaultsTheDepthAndRefusesAnUnservedOne(t *testing.T) {
	id := ids.NewV7().String()

	args, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if args.Depth != enrichDepthPage {
		t.Fatalf("depth defaulted to %q, want %q — the cheaper read is the one an omission gets",
			args.Depth, enrichDepthPage)
	}

	if _, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `","depth":"crawl"}`)); err == nil {
		t.Fatal("depth \"crawl\" was accepted; the tool serves two depths")
	}
}

func TestReadEnrichArgsRefusesATargetThatIsNotAnAbsoluteHTTPURL(t *testing.T) {
	id := ids.NewV7().String()
	for _, target := range []string{
		"example.com",         // scheme-less: not a URL the fetcher can resolve
		"file:///etc/passwd",  // a scheme that is not the web
		"https://",            // no host
		"javascript:alert(1)", // not a fetch at all
	} {
		t.Run(target, func(t *testing.T) {
			_, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `","url":"` + target + `"}`))
			if err == nil {
				t.Fatalf("%q was accepted as a fetch target", target)
			}
			if !strings.Contains(err.Error(), "absolute http(s) URL") {
				t.Fatalf("err = %v, want the requirement named so a caller can fix it", err)
			}
		})
	}

	if _, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `","url":"https://example.com/about"}`)); err != nil {
		t.Fatalf("an absolute https URL must be accepted: %v", err)
	}
}

// The tool is 🟡, so the only way to complete one is to re-present it with the
// approval — a surface that does not advertise the argument cannot be driven
// by a client that validates against it.
func TestEnrichAdvertisesTheArgumentItsOwnRedemptionNeeds(t *testing.T) {
	spec := enrichCompany{}.Spec()
	if !strings.Contains(string(spec.InputSchema), `"approval_id"`) {
		t.Errorf("enrich is confirm-first but its input schema omits approval_id, and it forbids "+
			"additional properties:\n%s", spec.InputSchema)
	}
}
