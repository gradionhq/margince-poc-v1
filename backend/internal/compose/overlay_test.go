// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// TestNewOverlayMeterUsesTheDefaultConfig proves NewOverlayMeter wires
// budgetmeter.go's own DefaultMeterConfig rather than an ad hoc literal
// — a Snapshot against a fresh meter must answer that config's Limit.
func TestNewOverlayMeterUsesTheDefaultConfig(t *testing.T) {
	m := NewOverlayMeter()
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	snap := m.Snapshot(ctx)
	want := overlay.DefaultMeterConfig()
	if snap.Limit != want.Limit {
		t.Fatalf("Snapshot().Limit = %d, want %d (DefaultMeterConfig)", snap.Limit, want.Limit)
	}
}
