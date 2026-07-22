// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// TestUnresolvedOwnerEmailsIsHonestlyUnwired proves the compose-level
// placeholder OwnerEmailResolver's own doc contract: it always answers
// an error naming the wiring gap, never a fabricated email — neither
// production consumer of this placeholder (NewOverlayHandlers,
// NewOverlayProvider) reaches it today, but a future one that did must
// see an honest failure, not a silent empty string.
func TestUnresolvedOwnerEmailsIsHonestlyUnwired(t *testing.T) {
	var r unresolvedOwnerEmails
	email, err := r.OwnerEmail(context.Background(), "owner-1")
	if err == nil {
		t.Fatal("OwnerEmail: want an error, got nil")
	}
	if email != "" {
		t.Fatalf("OwnerEmail email = %q, want empty on the error path", email)
	}
}

// TestOverlayMetricsSectionOmittedWithoutAVault proves the "declared or
// absent" posture overlayMetricsSection follows for /metrics — a role
// that never wired a vault (WithKeyvault absent) reports no overlay
// metrics section at all, the same posture readyz's own optional probes
// take.
func TestOverlayMetricsSectionOmittedWithoutAVault(t *testing.T) {
	srv := Server{}
	if got := overlayMetricsSection(srv, nil); got != nil {
		t.Fatalf("overlayMetricsSection with no vault = %#v, want nil", got)
	}
}

// TestOverlayMetricsSectionPresentWithAVault proves the section is
// assembled (SourceLag/SyncedTotal/ConflictTotal all wired) once a vault
// is configured — the fields themselves are exercised end to end by the
// overlay integration/e2e suites, which need a real Postgres for
// SourceLag's fleet walk.
func TestOverlayMetricsSectionPresentWithAVault(t *testing.T) {
	srv := Server{vault: keyvault.NewMemory()}
	got := overlayMetricsSection(srv, nil)
	if got == nil {
		t.Fatal("overlayMetricsSection with a vault configured = nil, want a populated section")
	}
	if got.SourceLag == nil || got.SyncedTotal == nil || got.ConflictTotal == nil {
		t.Fatalf("overlayMetricsSection fields = %#v, want all three wired", got)
	}
}

// TestOverlayBudgetConfigMapsEveryField proves compose's deployconfig->
// platform OVB config translation carries every window and fraction
// through faithfully — a dropped field would silently mismeter the shared
// incumbent quota. The meter's own behavior over this config is proven in
// the platform overlaybudget integration tests.
func TestOverlayBudgetConfigMapsEveryField(t *testing.T) {
	in := deployconfig.OverlayBudget{
		"hubspot": {
			Search:       deployconfig.WindowBudget{Ceiling: 5, Cap: 4},
			REST:         deployconfig.WindowBudget{Ceiling: 100000, Cap: 90000},
			WarnFraction: 0.7,
			ShedFraction: 0.9,
		},
	}
	got := OverlayBudgetConfig(in)
	hs, ok := got["hubspot"]
	if !ok {
		t.Fatal("OverlayBudgetConfig dropped the hubspot incumbent")
	}
	if hs.Search != (overlaybudget.WindowConfig{Ceiling: 5, Cap: 4}) {
		t.Fatalf("search window = %+v, want {5 4}", hs.Search)
	}
	if hs.REST != (overlaybudget.WindowConfig{Ceiling: 100000, Cap: 90000}) {
		t.Fatalf("rest window = %+v, want {100000 90000}", hs.REST)
	}
	if hs.WarnFraction != 0.7 || hs.ShedFraction != 0.9 {
		t.Fatalf("fractions = %g/%g, want 0.7/0.9", hs.WarnFraction, hs.ShedFraction)
	}
}
