// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Scheduling (features/07 §5c): availability proposes business-hour
// slots minus the host's existing meetings; booking commits one slot,
// lands a meeting on the linked timeline, and a taken slot answers
// slot_taken instead of double-booking.

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestAvailabilityAndBooking(t *testing.T) {
	e := setupRelationships(t)

	// A Tuesday-to-Wednesday window keeps the fixture off weekends.
	from := time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	window := fmt.Sprintf("from=%s&to=%s&duration_minutes=60",
		from.Format(time.RFC3339), to.Format(time.RFC3339))

	var avail struct {
		Slots []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"slots"`
	}
	if status := e.call(t, "GET", "/v1/availability?"+window, nil, nil, &avail); status != http.StatusOK {
		t.Fatalf("availability → %d", status)
	}
	if len(avail.Slots) == 0 {
		t.Fatal("no slots in a free business day")
	}
	for _, s := range avail.Slots {
		if s.Start.Hour() < 9 || s.End.Hour() > 17 {
			t.Fatalf("slot outside business hours: %+v", s)
		}
	}
	first := avail.Slots[0]

	// Book it, linked to the person.
	var booked struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if status := e.call(t, "POST", "/v1/bookings", anyMap{
		"start": first.Start, "end": first.End, "subject": "Discovery call",
		"links": []anyMap{{"entity_type": "person", "entity_id": e.personID}},
	}, nil, &booked); status != http.StatusCreated || booked.Kind != "meeting" {
		t.Fatalf("book → %d %+v", status, booked)
	}

	// The same slot is now taken…
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "POST", "/v1/bookings", anyMap{
		"start": first.Start, "end": first.End,
		"links": []anyMap{{"entity_type": "person", "entity_id": e.personID}},
	}, nil, &problem); status != http.StatusConflict || problem.Code != "slot_taken" {
		t.Fatalf("double-book → %d %q, want 409 slot_taken", status, problem.Code)
	}
	// …and availability no longer proposes it.
	if status := e.call(t, "GET", "/v1/availability?"+window, nil, nil, &avail); status != http.StatusOK {
		t.Fatalf("availability after booking → %d", status)
	}
	for _, s := range avail.Slots {
		if s.Start.Equal(first.Start) {
			t.Fatalf("booked slot still proposed: %+v", s)
		}
	}

	// A link target outside visibility refuses the booking (H1).
	if status := e.call(t, "POST", "/v1/bookings", anyMap{
		"start": first.Start.Add(3 * time.Hour), "end": first.End.Add(3 * time.Hour),
		"links": []anyMap{{"entity_type": "person", "entity_id": "00000000-0000-7000-8000-00000000dead"}},
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("booking with invisible link → %d, want 404", status)
	}
}
