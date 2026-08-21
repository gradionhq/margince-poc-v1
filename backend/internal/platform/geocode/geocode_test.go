// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package geocode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An address that resolves to nothing is not an error.
//
// The distinction is load-bearing: the worker records "no_match" against the
// company and stops asking, where an error would be retried. Collapsing the
// two means either retrying an address that will never resolve, or recording a
// network blip as a permanent fact about a company's location.
func TestAnAddressThatResolvesToNothingIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	point, ok, err := unpacedClient(srv.URL).Resolve(context.Background(), "Nowhere at all")
	if err != nil {
		t.Fatalf("an empty answer came back as an error: %v", err)
	}
	if ok {
		t.Errorf("an empty answer reported a point: %+v", point)
	}
}

func TestAResolvedAddressCarriesItsCoordinates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != UserAgent {
			t.Errorf("User-Agent = %q, want %q — the policy requires an identifiable agent", got, UserAgent)
		}
		if got := r.URL.Query().Get("limit"); got != "1" {
			t.Errorf("limit = %q, want 1 — asking for more is bandwidth nobody reads", got)
		}
		_, _ = w.Write([]byte(`[{"lat":"48.7758","lon":"9.1829"}]`))
	}))
	defer srv.Close()

	point, ok, err := unpacedClient(srv.URL).Resolve(context.Background(), "Stuttgart")
	if err != nil || !ok {
		t.Fatalf("resolving Stuttgart: ok=%v err=%v", ok, err)
	}
	if point.Lat != 48.7758 || point.Lon != 9.1829 {
		t.Errorf("point = %+v, want Stuttgart's coordinates", point)
	}
}

// A coordinate off the earth is refused rather than stored.
//
// This is where a transposed lat/lon pair gets caught: a longitude in the
// latitude field produces confidently wrong distances, which nobody notices,
// where an error is noticed immediately.
func TestACoordinateOffTheEarthIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"lat":"181.0","lon":"9.1829"}]`))
	}))
	defer srv.Close()

	if _, ok, err := unpacedClient(srv.URL).Resolve(context.Background(), "Somewhere"); err == nil || ok {
		t.Fatalf("a latitude of 181 was accepted: ok=%v err=%v", ok, err)
	}
}

// A provider that answers 429 or 403 is a condition to surface: this
// installation is being rate-limited or blocked, and absorbing that as "no
// match" would record a permanent fact about every company it touched.
func TestARefusedRequestIsAnErrorRatherThanNoMatch(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, ok, err := unpacedClient(srv.URL).Resolve(context.Background(), "Stuttgart")
		srv.Close()
		if err == nil {
			t.Errorf("status %d came back as ok=%v with no error", status, ok)
		}
	}
}

// The pacer holds the installation to one request per interval — proven on a
// fake clock, because a test that waited 15 seconds to prove a 15-second floor
// would be skipped, and a skipped test proves nothing.
func TestThePacerHoldsTheInstallationToOneRequestPerInterval(t *testing.T) {
	clock := time.Unix(0, 0)
	var slept []time.Duration
	p := &Pacer{
		interval: RecurringInterval,
		now:      func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			clock = clock.Add(d)
			return nil
		},
	}
	for range 3 {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatalf("waiting: %v", err)
		}
	}
	if len(slept) != 2 {
		t.Fatalf("slept %d times for 3 requests, want 2 — the first request waits for nothing", len(slept))
	}
	for i, d := range slept {
		if d != RecurringInterval {
			t.Errorf("wait %d = %v, want the full %v the usage policy asks of a recurring client",
				i, d, RecurringInterval)
		}
	}
}

// A caller who has given up must not hold the queue behind them.
func TestAnAbandonedRequestReleasesTheQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewPacer(RecurringInterval)
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("the first wait sleeps for nothing and should not check the context: %v", err)
	}
	if err := p.Wait(ctx); err == nil {
		t.Error("a cancelled caller waited out the full interval instead of giving up")
	}
}

// unpacedClient is the real client with its pacer set to zero, so the tests
// above exercise the HTTP path without waiting out the policy interval.
func unpacedClient(baseURL string) *Nominatim {
	n := NewNominatim(baseURL, nil)
	n.pacer = NewPacer(0)
	return n
}
