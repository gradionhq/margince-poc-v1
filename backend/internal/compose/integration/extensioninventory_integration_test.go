// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TestExtensionInventoryLogsOnlyChanges: the boot observation writes one
// system_log row per composed-set CHANGE (ADR-0069 §5) — a vanilla boot
// writes nothing, a rebooted unchanged set writes nothing, and install /
// upgrade / removal each write exactly one attributable row.
func TestExtensionInventoryLogsOnlyChanges(t *testing.T) {
	env := Setup(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	observe := func(exts ...extension.Extension) {
		t.Helper()
		if err := compose.ObserveExtensionInventory(ctx, env.Pool, logger, exts); err != nil {
			t.Fatalf("ObserveExtensionInventory: %v", err)
		}
	}
	unit := func(version extension.Version) extension.Extension {
		return extension.Extension{Name: "crm-hello", Version: version}
	}
	rows := func() int {
		t.Helper()
		return env.WsCount(t, `SELECT count(*) FROM system_log WHERE action = 'extension.composition_observed'`)
	}

	observe() // vanilla boot: nothing to record
	if got := rows(); got != 0 {
		t.Fatalf("vanilla boot wrote %d observation rows, want 0", got)
	}

	observe(unit("0.1.0")) // install
	if got := rows(); got != 1 {
		t.Fatalf("install wrote %d observation rows, want 1", got)
	}
	recorded := env.WsCount(t, `SELECT count(*) FROM system_log
		WHERE action = 'extension.composition_observed'
		  AND detail->'extensions' @> '[{"name":"crm-hello","version":"0.1.0"}]'`)
	if recorded != 1 {
		t.Fatalf("the observation row does not carry the composed unit, want crm-hello@0.1.0 in detail")
	}

	observe(unit("0.1.0")) // unchanged reboot
	if got := rows(); got != 1 {
		t.Fatalf("an unchanged reboot wrote a new observation row (%d total), want still 1", got)
	}

	observe(unit("0.2.0")) // upgrade
	if got := rows(); got != 2 {
		t.Fatalf("upgrade wrote %d observation rows, want 2", got)
	}

	observe() // removal
	if got := rows(); got != 3 {
		t.Fatalf("removal wrote %d observation rows, want 3", got)
	}
}
