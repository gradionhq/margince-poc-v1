// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personresearch

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/persondata"
)

// A provider's URL is untrusted input. A javascript: or data: source becomes a
// link the reader clicks, so it is refused HERE — at the boundary — rather than
// left for each consumer to remember.
func TestAClaimWhoseSourceIsNotAWebURLIsDropped(t *testing.T) {
	claims := wireClaims([]persondata.Claim{
		{Body: "clickable payload", Sources: []persondata.Source{{Label: "Bio", URL: "javascript:alert(1)"}}},
		{Body: "smuggled document", Sources: []persondata.Source{{Label: "Doc", URL: "data:text/html,<script>"}}},
		{Body: "a real citation", Sources: []persondata.Source{{Label: "Company site", URL: "https://example.com/team"}}},
	})
	if len(claims) != 1 {
		t.Fatalf("kept %d claims, want only the https one — a scheme that executes is not a citation", len(claims))
	}
	if claims[0].Body != "a real citation" {
		t.Errorf("kept %q, want the https-sourced claim", claims[0].Body)
	}
}

// A claim that loses every source loses its evidence, and a claim a reader
// cannot check is exactly what the citation rule exists to keep off the page.
func TestAClaimLeftWithNoOpenableSourceIsDropped(t *testing.T) {
	claims := wireClaims([]persondata.Claim{
		{Body: "unsourced", Sources: []persondata.Source{{Label: "nowhere", URL: "ftp://example.com/x"}}},
	})
	if len(claims) != 0 {
		t.Fatalf("kept %d claims, want none", len(claims))
	}
}
