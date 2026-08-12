// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// A conditional write must never be silently promoted to an unconditional one.
// Zero is this transport's sentinel for "no precondition" and no row can carry
// it — the version column starts at 1 — so every header value that lands on
// zero has to be refused rather than parsed and passed on. The first version
// of this fix caught unparseable text but let `If-Match: 0` through, which is
// the same hole one step further in.
func TestIfMatchRefusesEveryValueThatCannotNameARow(t *testing.T) {
	for _, raw := range []string{"0", `"0"`, "-1", "abc", ""} {
		v := crmcontracts.IfMatch(raw)
		if _, err := ifMatchVersion(&v); err == nil {
			t.Errorf("If-Match %q was accepted; a value that cannot name a row must be refused", raw)
		}
	}
	for _, raw := range []string{"1", `"7"`} {
		v := crmcontracts.IfMatch(raw)
		n, err := ifMatchVersion(&v)
		if err != nil || n < 1 {
			t.Errorf("If-Match %q -> (%d, %v); a real version must pass", raw, n, err)
		}
	}
	// The one legal zero: no header at all is the contract's unconditional write.
	if n, err := ifMatchVersion(nil); err != nil || n != 0 {
		t.Errorf("absent header -> (%d, %v); absent is the one legal zero", n, err)
	}
}
