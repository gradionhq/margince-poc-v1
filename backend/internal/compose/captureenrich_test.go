// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The §2.9 source-window rule as a table: the signature block is the
// TRAILING non-quoted lines — quoted history is never identity evidence,
// and padding never counts as content.

import (
	"fmt"
	"strings"
	"testing"
)

func TestSignatureBlockWindow(t *testing.T) {
	t.Run("quoted history is excluded", func(t *testing.T) {
		body := "Thanks!\n> On Tue, Alice wrote:\n> old text\nBest,\nBob Person\nCTO, Acme GmbH\n+49 30 1234567"
		got := signatureBlock(body)
		if strings.Contains(got, "old text") {
			t.Fatalf("quoted history leaked into the window: %q", got)
		}
		if !strings.Contains(got, "CTO, Acme GmbH") {
			t.Fatalf("signature line missing from the window: %q", got)
		}
	})

	t.Run("only the trailing lines survive a long body", func(t *testing.T) {
		var b strings.Builder
		for i := range 40 {
			fmt.Fprintf(&b, "prose line %d\n", i)
		}
		b.WriteString("Jane Doe\nHead of Ops\n")
		got := signatureBlock(b.String())
		if lines := strings.Count(got, "\n") + 1; lines > signatureLineCount {
			t.Fatalf("window holds %d lines, cap is %d", lines, signatureLineCount)
		}
		if !strings.HasSuffix(got, "Head of Ops") {
			t.Fatalf("window must end at the body's tail: %q", got)
		}
	})

	t.Run("an all-quoted body yields nothing", func(t *testing.T) {
		if got := signatureBlock("> a\n> b\n"); got != "" {
			t.Fatalf("all-quoted body produced %q, want empty", got)
		}
	})
}
