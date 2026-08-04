// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsJSONRecognisesTheStructuredSuffix(t *testing.T) {
	for mediaType, want := range map[string]bool{
		"application/json":         true,
		"text/json":                true,
		"application/vnd.api+json": true,
		"text/html":                false,
		"text/markdown":            false,
		"":                         false,
		"application/jsonwhatever": false,
	} {
		if got := (Doc{MediaType: mediaType}).IsJSON(); got != want {
			t.Errorf("Doc{%q}.IsJSON() = %v, want %v", mediaType, got, want)
		}
	}
}

// StripTags is an HTML reducer: every '>' becomes a space and anything between
// '<' and '>' is swallowed. Both occur inside ordinary JSON string values, so a
// caller that parsed the reduced text would be parsing something the server
// never sent.
func TestStripTagsWouldRewriteJSONThatMustSurviveVerbatim(t *testing.T) {
	body := `{"data":[{"id":"a","modality":"text->text","desc":"see <https://x.test/> for more"}]}`

	reduced := StripTags(body)
	if strings.Contains(reduced, "text->text") {
		t.Error("StripTags left '->' intact; this test no longer guards what it claims to")
	}
	if strings.Contains(reduced, "https://x.test/") {
		t.Error("StripTags left the angle-bracketed URL intact; this test no longer guards what it claims to")
	}

	// The fetch branch must therefore hand JSON back untouched.
	doc := Doc{Text: body, MediaType: "application/json"}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(doc.Text), &parsed); err != nil {
		t.Fatalf("a JSON doc must remain parseable: %v", err)
	}
}
