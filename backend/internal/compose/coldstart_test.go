// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
)

func TestStripTagsSurvivesUnicodeCaseFolding(t *testing.T) {
	// U+212A (KELVIN SIGN) lowercases to a 1-byte "k": an index into a
	// lowered copy of the document would drift off the source bytes and
	// slice out of range. The stripper must work on the original bytes.
	kelvin := strings.Repeat("\u212a", 3)
	got := stripTags(kelvin + "<p>hello</p><script>evil()</script> world")
	if !strings.HasSuffix(got, "hello world") {
		t.Fatalf("stripTags = %q", got)
	}
	if stripTags("<SCRIPT>x</SCRIPT>visible<STYLE>y</STYLE>") != "visible" {
		t.Fatal("case-insensitive script/style stripping broke")
	}
}
