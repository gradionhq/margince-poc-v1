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

// TestPeriodValidateRefusesNegativeComponents: a negative component
// renders as a Postgres-parseable interval whose cutoff lies in the
// future — it would silently shrink a statutory floor.
func TestPeriodValidateRefusesNegativeComponents(t *testing.T) {
	for _, valid := range []Period{{}, {Years: 6}, {Years: 1, Months: 6, Days: 15}} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Period%+v.Validate() = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []Period{{Years: -6}, {Months: -1}, {Days: -30}, {Years: 6, Days: -1}} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Period%+v.Validate() = nil, want the negative-component rejection", invalid)
		}
	}
}

// TestPeriodCutoffMatchesPostgresClamping: Cutoff must speak POSTGRES
// month arithmetic — day-of-month clamped to the target month's last
// day — because Go selects the strictest floor and Postgres enforces
// it; time.AddDate's forward normalization would disagree by a day at
// leap days and month ends, and a one-day disagreement is destruction a
// day early.
func TestPeriodCutoffMatchesPostgresClamping(t *testing.T) {
	leap := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	if got, want := (Period{Years: 1}).Cutoff(leap), time.Date(2023, 2, 28, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("2024-02-29 − P1Y = %s, want %s (Postgres clamps; AddDate would normalize to 2023-03-01)", got, want)
	}
	monthEnd := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if got, want := (Period{Months: 1}).Cutoff(monthEnd), time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("2026-07-31 − P1M = %s, want %s (Postgres clamps; AddDate would normalize to 2026-07-01)", got, want)
	}
	janCross := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	if got, want := (Period{Months: 1}).Cutoff(janCross), time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("2026-03-31 − P1M = %s, want %s", got, want)
	}
}

func TestAnchorValidate(t *testing.T) {
	for _, valid := range []Anchor{"", AnchorOccurrence, AnchorCalendarYearEnd} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Anchor(%q).Validate() = %v, want nil", valid, err)
		}
	}
	if err := Anchor("fiscal_year_end").Validate(); err == nil {
		t.Error("Anchor(fiscal_year_end).Validate() = nil, want the closed-set rejection")
	}
}

// TestProtectedSinceHonorsTheCalendarYearEndAnchor: §147(4) AO starts
// the clock at the END of the record's calendar year — a January 2020
// Handelsbrief under P6Y keeps until 2026-12-31, so in July 2026 every
// record back to 2020-01-01 is still protected, where an
// occurrence-anchored floor would already expose the first half of 2020.
func TestProtectedSinceHonorsTheCalendarYearEndAnchor(t *testing.T) {
	ref := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	yearEnd := RetentionClass{Name: CommercialCorrespondence, Keep: Period{Years: 6}, Anchor: AnchorCalendarYearEnd}
	if got, want := yearEnd.ProtectedSince(ref), time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("year-end ProtectedSince = %s, want %s", got, want)
	}
	occurrence := RetentionClass{Name: CommercialCorrespondence, Keep: Period{Years: 6}}
	if got, want := occurrence.ProtectedSince(ref), time.Date(2020, 7, 22, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("occurrence ProtectedSince = %s, want %s", got, want)
	}
	if !yearEnd.ProtectedSince(ref).Before(occurrence.ProtectedSince(ref)) {
		t.Error("the year-end anchor must be the stricter floor (earlier ProtectedSince) for an equal period")
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
