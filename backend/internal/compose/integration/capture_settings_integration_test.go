// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The workspace capture-settings store end to end (CAP-WIRE-7, ADR-0072/A118):
// every role reads the auto-enrich posture; only a holder of the
// capture_settings update grant may change it; the change is an audit-only
// write, and an idempotent no-op writes no audit row.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// captureSettingsCtx builds a human principal in the env workspace with a
// specific capture_settings grant.
func (e *searchEnv) captureSettingsCtx(grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestCaptureSettingsStore(t *testing.T) {
	e := setupSearch(t)
	store := capture.NewSettings(e.Pool)

	admin := e.captureSettingsCtx(principal.ObjectGrant{Read: true, Update: true})
	rep := e.captureSettingsCtx(principal.ObjectGrant{Read: true})
	none := e.captureSettingsCtx(principal.ObjectGrant{})

	auditCount := func() int {
		var n int
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT count(*) FROM audit_log WHERE entity_type = 'capture_settings'`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Default posture is ON (the testing default, migration 0121).
	got, err := store.Get(rep)
	if err != nil {
		t.Fatalf("rep read: %v", err)
	}
	if !got.AutoEnrich {
		t.Fatal("default capture_auto_enrich must be true")
	}

	// A reader with no grant is denied even the read.
	if _, err := store.Get(none); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("no-grant read err = %v, want permission denied", err)
	}

	// A rep (read-only) cannot toggle it.
	if _, err := store.Update(rep, boolPtr(false)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("rep update err = %v, want permission denied", err)
	}
	if auditCount() != 0 {
		t.Fatal("a denied update must write no audit row")
	}

	// Admin turns it off — one audit row, the new value returned and readable.
	updated, err := store.Update(admin, boolPtr(false))
	if err != nil {
		t.Fatalf("admin update: %v", err)
	}
	if updated.AutoEnrich {
		t.Fatal("update to false must return auto_enrich=false")
	}
	if auditCount() != 1 {
		t.Fatalf("admin update wrote %d audit rows, want 1", auditCount())
	}
	if reread, err := store.Get(rep); err != nil || reread.AutoEnrich {
		t.Fatalf("re-read after update: %+v err=%v — want auto_enrich=false", reread, err)
	}

	// An idempotent update (same value) is a no-op: no second audit row.
	if _, err := store.Update(admin, boolPtr(false)); err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	if auditCount() != 1 {
		t.Fatalf("idempotent update wrote a spurious audit row (%d total)", auditCount())
	}

	// A nil patch leaves it unchanged and writes nothing.
	if _, err := store.Update(admin, nil); err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	if auditCount() != 1 {
		t.Fatal("an empty patch must write no audit row")
	}
}
