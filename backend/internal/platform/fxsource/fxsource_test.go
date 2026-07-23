// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package fxsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestRatesInverts(t *testing.T) {
	// base EUR: 1 EUR = 1.08 USD and 0.86 GBP -> USD->EUR = 1/1.08, GBP->EUR = 1/0.86.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("base"); got != "EUR" {
			t.Errorf("base param = %q, want EUR", got)
		}
		// The real api.frankfurter.dev/v1/latest shape: amount + date ride
		// alongside base + rates; the parser ignores the first two.
		if _, err := w.Write([]byte(`{"amount":1.0,"base":"EUR","date":"2026-07-22","rates":{"USD":1.08,"GBP":0.86,"JPY":160.5}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, srv.Client())
	got, err := c.LatestRates(context.Background(), "EUR", []string{"USD", "GBP"})
	if err != nil {
		t.Fatalf("LatestRates: %v", err)
	}
	// JPY is not requested, so it must be filtered out.
	if _, ok := got["JPY"]; ok {
		t.Errorf("JPY should be filtered out, got %v", got)
	}
	if got["USD"] != "0.9259259259" { // 1/1.08 at 10dp
		t.Errorf("USD->EUR = %q, want 0.9259259259", got["USD"])
	}
	if got["GBP"] != "1.1627906977" { // 1/0.86 at 10dp
		t.Errorf("GBP->EUR = %q, want 1.1627906977", got["GBP"])
	}
}

func TestLatestRatesRejectsBaseMismatch(t *testing.T) {
	// The API answered in USD though we asked for EUR — rates would be wrong.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"base":"USD","rates":{"GBP":0.79}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()
	if _, err := New(srv.URL, srv.Client()).LatestRates(context.Background(), "EUR", []string{"GBP"}); err == nil {
		t.Fatal("expected an error on base mismatch")
	}
}

func TestLatestRatesRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, srv.Client()).LatestRates(context.Background(), "EUR", nil); err == nil {
		t.Fatal("expected an error on non-200")
	}
}
