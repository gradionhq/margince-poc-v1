// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The clock-free shape gates only — currency and rate. The effective-day guard
// moved into writeFxRate (sampled at write time), so its past/future behaviour
// is proven in the integration lane (TestFxRateAppendForward /
// TestFxRateRejectsPastBaseAndNonPositive) where a fixed clock is injected.
func TestNormalizeFxCurrencyRate(t *testing.T) {
	t.Run("uppercases and accepts a valid currency + rate", func(t *testing.T) {
		from, err := normalizeFxCurrencyRate(SetFxRateInput{FromCurrency: "usd", Rate: "0.92"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if from != "USD" {
			t.Errorf("from = %q, want USD", from)
		}
	})

	cases := map[string]SetFxRateInput{
		"non-3-letter currency": {FromCurrency: "US", Rate: "0.9"},
		"non-letter currency":   {FromCurrency: "U5D", Rate: "0.9"},
		"empty currency":        {FromCurrency: "", Rate: "0.9"},
		"zero rate":             {FromCurrency: "USD", Rate: "0"},
		"negative rate":         {FromCurrency: "USD", Rate: "-0.5"},
		"non-numeric rate":      {FromCurrency: "USD", Rate: "abc"},
	}
	for name, in := range cases {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := normalizeFxCurrencyRate(in)
			var v *FxRateValidationError
			if !errors.As(err, &v) {
				t.Fatalf("expected FxRateValidationError, got %v", err)
			}
		})
	}
}

// fxRateCtx binds a human holding exactly one fx_rate grant row.
func fxRateCtx(g principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
		Permissions: principal.Permissions{
			RoleKeys: []string{"fixture"},
			Objects:  map[string]principal.ObjectGrant{"fx_rate": g},
		},
	})
}

// The upfront half of the sheet's admission pair: it cannot know yet whether
// the write inserts or replaces, so EITHER write grant gets past it and a
// principal holding neither is refused here — before a pool connection is
// taken. The grant-specific half runs inside the transaction and is proven end
// to end in the integration lane (TestFxRateCreateAndUpdateGrantsGateSeparately).
func TestPrepareFxRateAdmitsEitherWriteGrant(t *testing.T) {
	in := SetFxRateInput{FromCurrency: "USD", Rate: "0.92"}
	admitted := map[string]principal.ObjectGrant{
		"create only":       {Create: true},
		"update only":       {Update: true},
		"create and update": {Create: true, Update: true},
	}
	for name, g := range admitted {
		t.Run("admits "+name, func(t *testing.T) {
			if _, err := NewStore(nil, Installation{BaseCurrency: unreachableBaseCurrency}).prepareFxRate(fxRateCtx(g), in); err != nil {
				t.Fatalf("prepareFxRate = %v, want admitted", err)
			}
		})
	}

	refused := map[string]principal.ObjectGrant{
		"read only": {Read: true},
		"no grant":  {},
		// delete is the one verb the sheet never grants (a past-dated row
		// prices historical rollups), so it must not open the write either.
		"delete only": {Delete: true},
	}
	for name, g := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := NewStore(nil, Installation{BaseCurrency: unreachableBaseCurrency}).prepareFxRate(fxRateCtx(g), in)
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Fatalf("prepareFxRate = %v, want ErrPermissionDenied", err)
			}
		})
	}
}

// unreachableBaseCurrency stands in for the installation-settings seam in the
// tests above. prepareFxRate is the connection-free half of the fx write — it
// admits or refuses before any value is resolved — so reaching this is a
// signal that the split moved, not a fixture that needs a currency.
func unreachableBaseCurrency(context.Context, pgx.Tx) (string, error) {
	return "", errors.New("prepareFxRate resolved the base currency; it is meant to run before any connection")
}

// A store built without the base-currency seam fails CLOSED at the first money
// operation, rather than dereferencing a nil function inside an open
// transaction. The field's doc calls the seam required; this is what makes
// that a check rather than a claim.
func TestAStoreWithNoBaseCurrencySeamRefusesRatherThanPanicking(t *testing.T) {
	t.Parallel()
	_, err := NewStore(nil, Installation{}).installation.BaseCurrency(context.Background(), nil)
	if err == nil {
		t.Fatal("an un-injected seam resolved a base currency; it must refuse")
	}
	if !strings.Contains(err.Error(), "BaseCurrency") {
		t.Errorf("the refusal should name which seam is missing, got %q", err)
	}
}
