// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// docFetcher serves a full webread.Doc (text + media type) per URL, so a probe
// can differ from the seed by media type as well as content.
type docFetcher map[string]webread.Doc

func (d docFetcher) Fetch(_ context.Context, rawURL string) (webread.Doc, error) {
	if doc, ok := d[rawURL]; ok {
		return doc, nil
	}
	return webread.Doc{}, errNotFound
}

func TestProbeLegalPageDedupsOnlyWhenMediaTypesMatch(t *testing.T) {
	// Each page carries >= minReadableRunes (80) of text.
	home := strings.Repeat("Acme builds robots for RevOps leaders. ", 4)
	impressum := strings.Repeat("Impressum: Acme Robotics GmbH, Stuttgart. ", 4)

	t.Run("same media, identical text is a duplicate (miss)", func(t *testing.T) {
		x := evidenceExtractor{fetch: docFetcher{
			"https://acme.example":           {Text: home, MediaType: "text/markdown"},
			"https://acme.example/impressum": {Text: home, MediaType: "text/markdown"},
		}}
		seed := webread.Doc{Text: home, MediaType: "text/markdown"}
		if url, text := x.probeLegalPage(context.Background(), "https://acme.example", seed); url != "" || text != "" {
			t.Errorf("expected a miss for an SPA catch-all, got url=%q text=%q", url, text)
		}
	})

	t.Run("distinct impressum is found", func(t *testing.T) {
		x := evidenceExtractor{fetch: docFetcher{
			"https://acme.example/impressum": {Text: impressum, MediaType: "text/markdown"},
		}}
		seed := webread.Doc{Text: home, MediaType: "text/markdown"}
		url, text := x.probeLegalPage(context.Background(), "https://acme.example", seed)
		if url != "https://acme.example/impressum" || text != impressum {
			t.Errorf("expected the impressum, got url=%q text=%q", url, text)
		}
	})
}
