// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Field-level provenance (B-E02.12): one shared, RLS-scoped
// field_provenance table covers every core captured object; the
// row-level source/captured_by stays the creation default and display
// falls back to it (both layers coexist); human-entered and captured
// fields are independently queryable; and no per-object JSONB
// provenance backing exists (gates Q1–Q3 → a).

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestFieldProvenanceCoversCaptureAcrossObjectTypes(t *testing.T) {
	e := setupSearch(t)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Inbox Sender', 'manual', 'human:x')`)

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	fake := &mailFake{linkTo: personID}
	registry.Register(fake)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}

	assertCaptureStampedBothObjectTypes(t, e)

	// Captured vs human-entered fields are independently queryable: the
	// captured set is non-empty, and none of it claims a human author.
	var humanStamped int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM field_provenance WHERE captured_by LIKE 'human:%'`).Scan(&humanStamped)
	})
	if err != nil {
		t.Fatal(err)
	}
	if humanStamped != 0 {
		t.Fatalf("capture stamped %d fields as human-entered", humanStamped)
	}

	// Mixed-origin display read: the person was human-created and has no
	// field rows — every requested field falls back to the row-level
	// provenance (gate Q3 coexistence).
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		origins, err := storekit.FieldOrigins(context.Background(), tx, "person", personID,
			[]string{"full_name", "title"}, "manual", "human:x", time.Now().UTC())
		if err != nil {
			return err
		}
		for field, origin := range origins {
			if origin.FieldLevel || origin.Source != "manual" || origin.CapturedBy != "human:x" {
				t.Fatalf("field %s must fall back to row-level provenance: %+v", field, origin)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// assertCaptureStampedBothObjectTypes checks the capture stamped
// field-level provenance for BOTH object types it created (activity +
// lead), under the connector's identity.
func assertCaptureStampedBothObjectTypes(t *testing.T, e *searchEnv) {
	t.Helper()
	type stampRow struct {
		objectType, field, source, capturedBy string
	}
	var stamps []stampRow
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT object_type, field_name, source, captured_by FROM field_provenance ORDER BY object_type, field_name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s stampRow
			if err := rows.Scan(&s.objectType, &s.field, &s.source, &s.capturedBy); err != nil {
				return err
			}
			stamps = append(stamps, s)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, s := range stamps {
		types[s.objectType] = true
		if s.capturedBy != "connector:graph" {
			t.Fatalf("field stamp names %q, want the connector identity", s.capturedBy)
		}
	}
	if !types["activity"] || !types["lead"] {
		t.Fatalf("field provenance must cover both captured object types, got %+v", stamps)
	}
}

// The storage shape is the normalized shared table, never a per-object
// JSONB provenance column (gate Q1) — derived from the live schema so a
// future object cannot quietly reintroduce the rejected shape.
func TestNoPerObjectJSONBProvenanceBacking(t *testing.T) {
	e := setupSearch(t)
	var offenders int
	err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND data_type = 'jsonb'
		  AND column_name LIKE '%provenance%'`).Scan(&offenders)
	if err != nil {
		t.Fatal(err)
	}
	if offenders != 0 {
		t.Fatalf("%d per-object JSONB provenance columns exist — B-E02.12 gate Q1 chose the shared table", offenders)
	}
}
