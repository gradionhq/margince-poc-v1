// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package password

import "testing"

// The cost parameters are selected by build tag, so an ordinary `go test` sees
// params.go — the values that compile into every shipped binary. Pinning them
// here is what stops the split from becoming a way to weaken the product:
// before the split the constants sat beside the code that used them and a
// reviewer reading Hash saw them; now they do not, and nothing else in a normal
// `make check` would notice them being lowered.
//
// This asserts the OWASP 2024 interactive-login baseline (ADR-0043). Raising
// them is fine and this test should be updated to match. LOWERING them is the
// event this exists to make loud.
func TestProductionParametersHoldTheOWASPBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		got   int
		floor int
	}{
		{"time cost", timeCost, 2},
		{"memory in KiB", memoryKiB, 19 * 1024},
		{"threads", threads, 1},
	} {
		if tc.got < tc.floor {
			t.Errorf("the shipped Argon2id %s is %d, below the OWASP 2024 baseline of %d — "+
				"a stolen password table becomes cheaper to crack by exactly this factor",
				tc.name, tc.got, tc.floor)
		}
	}
}
