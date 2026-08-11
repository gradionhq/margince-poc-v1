// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func baselineCtx(grant principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:baseline-test",
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestSearchBaselineFiltersCountsAndCapsThePage(t *testing.T) {
	ctx := baselineCtx(principal.ObjectGrant{Read: true})

	everything, err := SearchBaseline(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if everything.Total == 0 || everything.Matched != everything.Total {
		t.Errorf("no filter: matched %d of total %d, want them equal and non-zero",
			everything.Matched, everything.Total)
	}
	if len(everything.Domains) != BaselinePageSize {
		t.Errorf("no filter answers %d domains, want the %d-cap", len(everything.Domains), BaselinePageSize)
	}

	// The filter trims and case-folds, so the operator's paste matches the
	// lowercased dataset.
	gmail, err := SearchBaseline(ctx, "  GMAIL.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(gmail.Domains, "gmail.com") {
		t.Errorf("gmail.com filter answers %v, want it to contain gmail.com", gmail.Domains)
	}
	if gmail.Matched == 0 || gmail.Matched >= everything.Total {
		t.Errorf("gmail.com matched %d of %d, want a real narrowing", gmail.Matched, everything.Total)
	}

	nothing, err := SearchBaseline(ctx, "no-such-provider.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if nothing.Matched != 0 || len(nothing.Domains) != 0 {
		t.Errorf("an unmatched filter answers %d/%v, want zero", nothing.Matched, nothing.Domains)
	}
}

func TestSearchBaselineDemandsTheCaptureReadGrant(t *testing.T) {
	if _, err := SearchBaseline(baselineCtx(principal.ObjectGrant{Create: true}), "gmail"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("a seat without capture_settings read = %v, want permission denied", err)
	}
}
