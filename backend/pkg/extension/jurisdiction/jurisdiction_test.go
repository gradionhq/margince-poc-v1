// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jurisdiction

import (
	"testing"
	"time"
)

func TestCodeValidate(t *testing.T) {
	for _, valid := range []Code{"de", "zz", "fr"} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Code(%q).Validate() = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []Code{"", "d", "DE", "deu", "d1", "d "} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Code(%q).Validate() = nil, want the alpha-2 rejection", invalid)
		}
	}
}

func TestPeriodStringIsAnISO8601DateInterval(t *testing.T) {
	cases := map[string]Period{
		"P6Y":    {Years: 6},
		"P8Y":    {Years: 8},
		"P1Y6M":  {Years: 1, Months: 6},
		"P30D":   {Days: 30},
		"P2M10D": {Months: 2, Days: 10},
		// The zero period renders explicitly — an empty "P" is not a
		// valid ISO 8601 duration and Postgres would reject it.
		"P0D": {},
	}
	for want, p := range cases {
		if got := p.String(); got != want {
			t.Errorf("Period%+v.String() = %q, want %q", p, got, want)
		}
	}
}

// TestPeriodCutoffIsCalendarCorrect: the reason Period exists — six
// calendar years anchored after a leap day reach exactly six years back,
// not 2190 days (which would land ~1.5 days short and let destructive
// retention run early).
func TestPeriodCutoffIsCalendarCorrect(t *testing.T) {
	ref := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	got := Period{Years: 6}.Cutoff(ref)
	want := time.Date(2020, 7, 22, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("P6Y cutoff at %s = %s, want %s", ref, got, want)
	}
	dayApprox := ref.AddDate(0, 0, -6*365)
	if got.Equal(dayApprox) {
		t.Fatal("P6Y cutoff equals the 365-day approximation — the span 2020–2026 contains leap days, so calendar arithmetic must differ")
	}
}
