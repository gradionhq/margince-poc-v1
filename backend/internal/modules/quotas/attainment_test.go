// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package quotas

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The pace table (RD-PARAM-4): 0 before the period opens, 100 from the
// period's end onward, linear elapsed/total between — evaluated at an
// explicit instant, never a wall clock.
func TestPacePct(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	midpoint := start.Add(end.Sub(start) / 2)
	oneDayBeforeEnd := end.Add(-24 * time.Hour)
	wantOneDayBefore := oneDayBeforeEnd.Sub(start).Seconds() / end.Sub(start).Seconds() * 100

	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		{"before period_start", start.Add(-24 * time.Hour), 0},
		{"at period_end", end, 100},
		{"after period_end", end.Add(24 * time.Hour), 100},
		{"midpoint", midpoint, 50},
		{"one day before period_end", oneDayBeforeEnd, wantOneDayBefore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pacePct(start, end, tt.now); math.Abs(got-tt.want) > 0.001 {
				t.Errorf("pacePct(%s) = %v, want %v", tt.now.Format(time.DateOnly), got, tt.want)
			}
		})
	}
}

// The band boundaries (RD-PARAM-4): met from exactly 100 up, accent from
// exactly 60 to just under 100, behind below 60 — the server computes the
// band once, the client never re-derives it.
func TestAttainmentBand(t *testing.T) {
	tests := []struct {
		pct  float64
		want string
	}{
		{0, "behind"},
		{59.9, "behind"},
		{60.0, "accent"},
		{99.9, "accent"},
		{100.0, "met"},
		{150, "met"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := attainmentBand(tt.pct); got != tt.want {
				t.Errorf("attainmentBand(%v) = %q, want %q", tt.pct, got, tt.want)
			}
		})
	}
}

// A store built without the installation-settings seam must REFUSE to compute
// attainment rather than fall back to the retiring workspace column. Two
// answers to "what is the base currency" is how the copies drift apart
// unnoticed, and here the drift would surface as silently mispriced attainment.
func TestAttainmentRefusesWithoutTheInstallationSettingsSeam(t *testing.T) {
	t.Parallel()
	_, _, err := NewStore(nil).targetInBase(context.Background(), nil, crmcontracts.Quota{
		Currency: "EUR", TargetMinor: 100_000,
	}, time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("targetInBase without the settings seam returned no error; " +
			"an unwired store must refuse, never read the retiring column instead")
	}
	if !strings.Contains(err.Error(), "WithSettings") {
		t.Errorf("the refusal must name the fix; got %q", err)
	}
}
