// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

var testBands = aicert.Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40}

func runResults(hardPasses []bool, scores []int) []aicert.RunResult {
	rs := make([]aicert.RunResult, len(hardPasses))
	for i := range rs {
		rs[i] = aicert.RunResult{HardPass: hardPasses[i], Score: scores[i]}
	}
	return rs
}

// TestVerdictReliabilityAtEveryN3PassCount pins the four reliability values
// an N=3 run set can land on: 0, 1/3, 2/3, 1 HardPasses out of 3.
func TestVerdictReliabilityAtEveryN3PassCount(t *testing.T) {
	cases := []struct {
		name       string
		hardPasses []bool
		wantReliab float64
	}{
		{"zero of three", []bool{false, false, false}, 0},
		{"one of three", []bool{true, false, false}, 1.0 / 3.0},
		{"two of three", []bool{true, true, false}, 2.0 / 3.0},
		{"three of three", []bool{true, true, true}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := runResults(c.hardPasses, []int{80, 80, 80})
			_, reliability := aicert.Verdict(rs, testBands)
			if diff := reliability - c.wantReliab; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("reliability = %v, want %v", reliability, c.wantReliab)
			}
		})
	}
}

func TestVerdictCertifiedRequiresAllHardPassAndMedianAndFloor(t *testing.T) {
	rs := runResults([]bool{true, true, true}, []int{75, 80, 90})
	verdict, _ := aicert.Verdict(rs, testBands)
	if verdict != aicert.VerdictCertified {
		t.Fatalf("verdict = %q, want %q", verdict, aicert.VerdictCertified)
	}
}

// TestVerdictMinBelowFloorFailsCertification: every run HardPasses and the
// median clears CertifiedMin, but the worst run's score dips under Floor —
// spec §5 makes the floor a hard gate on Certified independent of the
// median, so this must NOT certify.
func TestVerdictMinBelowFloorFailsCertification(t *testing.T) {
	rs := runResults([]bool{true, true, true}, []int{90, 90, 30})
	verdict, _ := aicert.Verdict(rs, testBands)
	if verdict == aicert.VerdictCertified {
		t.Fatalf("verdict = %q, want anything but certified (min score 30 < floor 40)", verdict)
	}
	// The median (90) still clears DegradedMin (50) and 3/3 HardPass clears
	// the ceil(2*3/3)=2 threshold, so this specific case still qualifies as
	// supported-degraded — the floor only gates the top verdict.
	if verdict != aicert.VerdictSupportedDegraded {
		t.Fatalf("verdict = %q, want %q", verdict, aicert.VerdictSupportedDegraded)
	}
}

func TestVerdictSupportedDegradedAtTheTwoOfThreeThreshold(t *testing.T) {
	rs := runResults([]bool{true, true, false}, []int{60, 60, 10})
	verdict, _ := aicert.Verdict(rs, testBands)
	if verdict != aicert.VerdictSupportedDegraded {
		t.Fatalf("verdict = %q, want %q (2/3 HardPass meets ceil(2*3/3)=2, median 60 >= DegradedMin 50)", verdict, aicert.VerdictSupportedDegraded)
	}
}

func TestVerdictNotSupportedBelowTheDegradedThreshold(t *testing.T) {
	rs := runResults([]bool{true, false, false}, []int{90, 10, 10})
	verdict, _ := aicert.Verdict(rs, testBands)
	if verdict != aicert.VerdictNotSupported {
		t.Fatalf("verdict = %q, want %q (only 1/3 HardPass, below the ceil(2*3/3)=2 threshold)", verdict, aicert.VerdictNotSupported)
	}
}

func TestVerdictNotSupportedWhenMedianMissesDegradedMinDespiteHardPasses(t *testing.T) {
	rs := runResults([]bool{true, true, true}, []int{20, 20, 20})
	verdict, _ := aicert.Verdict(rs, testBands)
	if verdict != aicert.VerdictNotSupported {
		t.Fatalf("verdict = %q, want %q (median 20 misses DegradedMin 50 despite 3/3 HardPass)", verdict, aicert.VerdictNotSupported)
	}
}

func TestVerdictMedianOfAnOddRunCountIsTheMiddleElementNoInterpolation(t *testing.T) {
	// Five runs, scores unsorted on input: median must be the middle of the
	// SORTED sequence (60), not an interpolated or input-order value.
	rs := runResults([]bool{true, true, true, true, true}, []int{90, 10, 60, 55, 100})
	verdict, _ := aicert.Verdict(rs, testBands)
	// median(sorted [10,55,60,90,100]) = 60 >= CertifiedMin 70? No: 60 < 70,
	// so this must NOT certify; it must still clear supported-degraded
	// (5/5 HardPass >= ceil(10/3)=4, median 60 >= DegradedMin 50).
	if verdict != aicert.VerdictSupportedDegraded {
		t.Fatalf("verdict = %q, want %q", verdict, aicert.VerdictSupportedDegraded)
	}
}

func TestVerdictPanicsOnAnEvenRunCount(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an even (non-odd) run count")
		}
	}()
	aicert.Verdict(runResults([]bool{true, true}, []int{80, 80}), testBands)
}

func TestVerdictPanicsOnAnEmptyRunSet(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an empty run set")
		}
	}()
	aicert.Verdict(nil, testBands)
}
