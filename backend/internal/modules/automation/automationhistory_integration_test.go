// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package automation

// The /automations/{id}/runs read (A72/ADR-0035 Am.1) over a real
// migrated Postgres: keyset paging newest-first, the wire-vocabulary
// outcome filter, instance-exact linkage, and existence-hiding for
// absent and foreign automations.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedRunHistory records one run per terminal status on the instance,
// one minute apart, plus a sibling instance of the same type whose run
// must never ride into the first one's history.
func seedRunHistory(t *testing.T, fx *autoFixture, autoID ids.AutomationID) int {
	t.Helper()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reason := func(s string) []byte {
		payload, err := reasonDetail(s)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	staged, err := stagedApprovalDetail(ids.New[ids.ApprovalKind]())
	if err != nil {
		t.Fatal(err)
	}
	seeded := []struct {
		status string
		detail []byte
	}{
		{"applied", nil},
		{"failed", reason("provider error")},
		{"blocked", reason("approval rejected")},
		{"skipped", reason("conditions declined")},
		{"requires_approval", staged},
	}
	for i, s := range seeded {
		fx.seedRun(t, autoID, "stage_change_create_task", s.status, s.detail, base.Add(time.Duration(i)*time.Minute))
	}
	otherID := fx.seedAutomation(t, "stage_change_create_task")
	fx.seedRun(t, otherID, "stage_change_create_task", "applied", nil, base.Add(time.Hour))
	return len(seeded)
}

func TestListRunsPagesNewestFirstScopedToTheInstance(t *testing.T) {
	fx := setupAutomationDB(t)
	store := NewAutomationStore(fx.pool)
	ctx := fx.humanCtx(fx.rep1, principal.RowScopeAll)
	autoID := fx.seedAutomation(t, "stage_change_create_task")
	seeded := seedRunHistory(t, fx, autoID)

	limit := 2
	page1, err := store.ListRuns(ctx, autoID, nil, &limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page1 = %d items hasMore=%v — want 2 with a cursor", len(page1.Items), page1.HasMore)
	}
	// Newest first: the last-seeded status leads, stamped with the tier.
	if page1.Items[0].Status != "requires_approval" || page1.Items[0].Tier != "auto_execute" {
		t.Fatalf("page1[0] = %s/%s, want the newest run stamped with the automation's tier", page1.Items[0].Status, page1.Items[0].Tier)
	}
	all := append([]AutomationRunRecord(nil), page1.Items...)
	cursor := page1.NextCursor
	for cursor != "" {
		page, err := store.ListRuns(ctx, autoID, &cursor, &limit, nil)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Items...)
		cursor = page.NextCursor
	}
	if len(all) != seeded {
		t.Fatalf("paging returned %d runs, want exactly this instance's %d (the sibling instance's run must not ride in)", len(all), seeded)
	}
}

func TestListRunsOutcomeFilterSpeaksTheWireVocabulary(t *testing.T) {
	fx := setupAutomationDB(t)
	store := NewAutomationStore(fx.pool)
	ctx := fx.humanCtx(fx.rep1, principal.RowScopeAll)
	autoID := fx.seedAutomation(t, "stage_change_create_task")
	seedRunHistory(t, fx, autoID)

	outcome := "fired"
	fired, err := store.ListRuns(ctx, autoID, nil, nil, &outcome)
	if err != nil {
		t.Fatal(err)
	}
	if len(fired.Items) != 1 || fired.Items[0].Status != "applied" {
		t.Fatalf("outcome=fired returned %d items, want the one applied run", len(fired.Items))
	}
	bad := "exploded"
	_, err = store.ListRuns(ctx, autoID, nil, nil, &bad)
	var param *ParamError
	if !errors.As(err, &param) {
		t.Fatalf("unknown outcome → %v, want a ParamError (not an empty page)", err)
	}
}

func TestListRunsHidesAbsentAndForeignAutomations(t *testing.T) {
	fx := setupAutomationDB(t)
	store := NewAutomationStore(fx.pool)
	ctx := fx.humanCtx(fx.rep1, principal.RowScopeAll)

	if _, err := store.ListRuns(ctx, ids.New[ids.AutomationKind](), nil, nil, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("absent automation → %v, want ErrNotFound", err)
	}
	foreignWS := ids.NewV7()
	fx.exec(t, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Other', 'other', 'EUR')`, foreignWS)
	foreignAuto := ids.New[ids.AutomationKind]()
	fx.exec(t, `
		INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, enabled)
		VALUES ($1, $2, 'route_lead', 'foreign', '{"event_type":"test"}', '{"kind":"test"}', '{}'::jsonb, true)`,
		foreignAuto, foreignWS)
	if _, err := store.ListRuns(ctx, foreignAuto, nil, nil, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("foreign automation → %v, want ErrNotFound (existence-hiding, indistinguishable from absent)", err)
	}
}
