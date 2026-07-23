// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// POST/GET /v1/fx-rates and /v1/ai-model-rates over the real wire: the
// happy-path append + read, the µUSD<->USD round-trip on the wire, and the
// past-date 422. Store-level suites (fxrate_/modelrate_integration_test.go)
// carry the RBAC and cross-tenant RLS matrix; these assert the transport.

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type fxRateDTO struct {
	FromCurrency  string `json:"from_currency"`
	ToCurrency    string `json:"to_currency"`
	Rate          string `json:"rate"`
	EffectiveDate string `json:"effective_date"`
}

type fxRateListDTO struct {
	Data []fxRateDTO `json:"data"`
}

func (e *env) baseCurrency(t *testing.T) string {
	t.Helper()
	var base string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT base_currency FROM workspace WHERE slug = $1`, e.slug).Scan(&base); err != nil {
		t.Fatalf("base currency lookup: %v", err)
	}
	return base
}

func TestFxRatesOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	today := time.Now().UTC().Format("2006-01-02")

	from := "USD"
	if e.baseCurrency(t) == "USD" {
		from = "GBP"
	}

	var created fxRateDTO
	if status := e.call(t, "POST", "/v1/fx-rates",
		map[string]any{"from_currency": from, "rate": "0.92", "effective_date": today}, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST /fx-rates → %d, want 201", status)
	}
	// numeric(20,10) echoes the canonical full-precision form.
	if created.FromCurrency != from || created.Rate != "0.9200000000" {
		t.Fatalf("created = %+v, want %s @ 0.9200000000", created, from)
	}

	var list fxRateListDTO
	if status := e.call(t, "GET", "/v1/fx-rates", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET /fx-rates → %d, want 200", status)
	}
	if len(list.Data) != 1 || list.Data[0].FromCurrency != from {
		t.Fatalf("list = %+v, want one %s row", list.Data, from)
	}

	// A past effective date is a clean 422, not a 500.
	past := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if status := e.call(t, "POST", "/v1/fx-rates",
		map[string]any{"from_currency": from, "rate": "0.9", "effective_date": past}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("past-date POST /fx-rates → %d, want 422", status)
	}
}

type aiModelRateDTO struct {
	Provider          string `json:"provider"`
	ModelID           string `json:"model_id"`
	InputPerMtok      string `json:"input_per_mtok"`
	OutputPerMtok     string `json:"output_per_mtok"`
	CacheReadPerMtok  string `json:"cache_read_per_mtok"`
	CacheWritePerMtok string `json:"cache_write_per_mtok"`
	EffectiveDate     string `json:"effective_date"`
}

type aiModelRateListDTO struct {
	Data []aiModelRateDTO `json:"data"`
}

func TestAiModelRatesOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	today := time.Now().UTC().Format("2006-01-02")

	var created aiModelRateDTO
	if status := e.call(t, "POST", "/v1/ai-model-rates", map[string]any{
		"provider": "anthropic", "model_id": "claude-opus-4-8",
		"input_per_mtok": "5.00", "output_per_mtok": "25",
		"cache_read_per_mtok": "0.5", "cache_write_per_mtok": "6.25",
		"effective_date": today,
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST /ai-model-rates → %d, want 201", status)
	}
	// "5.00" USD/MTok round-trips through µUSD storage to the trimmed "5".
	if created.InputPerMtok != "5" || created.CacheWritePerMtok != "6.25" {
		t.Fatalf("created = %+v, want input 5 / cache-write 6.25", created)
	}

	// The workspace bootstrap seeds default model rates; our upsert corrects
	// the opus row in place, so the list contains opus at our input of 5.
	var list aiModelRateListDTO
	if status := e.call(t, "GET", "/v1/ai-model-rates", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET /ai-model-rates → %d, want 200", status)
	}
	found := false
	for _, row := range list.Data {
		if row.Provider == "anthropic" && row.ModelID == "claude-opus-4-8" {
			found = true
			if row.InputPerMtok != "5" {
				t.Fatalf("opus input = %q, want 5", row.InputPerMtok)
			}
		}
	}
	if !found {
		t.Fatalf("list %+v has no anthropic/claude-opus-4-8 row", list.Data)
	}

	past := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if status := e.call(t, "POST", "/v1/ai-model-rates", map[string]any{
		"provider": "anthropic", "model_id": "m",
		"input_per_mtok": "1", "output_per_mtok": "1",
		"cache_read_per_mtok": "0", "cache_write_per_mtok": "0",
		"effective_date": past,
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("past-date POST /ai-model-rates → %d, want 422", status)
	}
}
