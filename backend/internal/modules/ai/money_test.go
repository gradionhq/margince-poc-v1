// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"errors"
	"testing"
)

func TestUsdPerMTokToMicroUSD(t *testing.T) {
	cases := map[string]int64{
		"5": 5_000_000, "5.00": 5_000_000, "0.15": 150_000,
		"25.5": 25_500_000, "0": 0, "6.25": 6_250_000,
	}
	for in, want := range cases {
		got, err := UsdPerMTokToMicroUSD("input_per_mtok", in)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", in, err)
		}
		if got != want {
			t.Errorf("UsdPerMTokToMicroUSD(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestUsdPerMTokRejects(t *testing.T) {
	for _, bad := range []string{"-1", "abc", "", "1e400", "12345678901234567890"} {
		_, err := UsdPerMTokToMicroUSD("input_per_mtok", bad)
		var v *RateValidationError
		if !errors.As(err, &v) {
			t.Errorf("UsdPerMTokToMicroUSD(%q) err = %v, want RateValidationError", bad, err)
		}
	}
}

func TestMicroUSDToUsdPerMTok(t *testing.T) {
	cases := map[int64]string{
		5_000_000: "5", 150_000: "0.15", 25_500_000: "25.5",
		0: "0", 6_250_000: "6.25",
	}
	for in, want := range cases {
		if got := MicroUSDToUsdPerMTok(in); got != want {
			t.Errorf("MicroUSDToUsdPerMTok(%d) = %q, want %q", in, got, want)
		}
	}
}
