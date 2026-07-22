// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

var wireSyncedAt = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// wireRecord builds a mirror-shaped datasource.Record the way
// overlay.Provider serves one: canonical fields as jsonb, T2-labelled.
func wireRecord(t *testing.T, et datasource.EntityType, fields map[string]any) datasource.Record {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshaling fixture fields: %v", err)
	}
	return datasource.Record{
		Ref:       datasource.EntityRef{Type: et, ID: ids.NewV7()},
		Fields:    raw,
		Freshness: datasource.FreshnessInfo{LastSyncedAt: wireSyncedAt, Authoritative: false},
	}
}

func wireCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), ids.NewV7())
}

func TestOverlayWirePersonAssemblesNameAndStampsProvenance(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"first_name": "Ada", "last_name": "Overlay", "title": "CTO",
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.FullName != "Ada Overlay" {
		t.Errorf("FullName = %q, want the joined first+last", person.FullName)
	}
	if person.Source != "overlay" {
		t.Errorf("Source = %q, want overlay", person.Source)
	}
	if !person.CreatedAt.Equal(wireSyncedAt) || !person.UpdatedAt.Equal(wireSyncedAt) {
		t.Error("timestamps must carry the mirror's own last-synced instant — the only time the mirror can honestly claim")
	}
	if person.Raw == nil || (*person.Raw)["title"] != "CTO" {
		t.Error("the full canonical payload must ride raw")
	}
}

func TestOverlayWirePersonNamelessFallsBackToEmailThenUnnamed(t *testing.T) {
	withEmail := wireRecord(t, datasource.EntityPerson, map[string]any{
		"person_email": map[string]any{"email": "ada@example.test"},
	})
	person, err := overlayWirePerson(wireCtx(), withEmail)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if person.FullName != "ada@example.test" {
		t.Errorf("nameless person FullName = %q, want the mapped email", person.FullName)
	}
	bare, err := overlayWirePerson(wireCtx(), wireRecord(t, datasource.EntityPerson, map[string]any{}))
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	if bare.FullName != "Unnamed" {
		t.Errorf("bare person FullName = %q, want Unnamed", bare.FullName)
	}
}

func TestOverlayWireDealDerivesStatusFromClosedStageKeys(t *testing.T) {
	for stage, want := range map[string]crmcontracts.DealStatus{
		"closedwon":      crmcontracts.DealStatusWon,
		"closedlost":     crmcontracts.DealStatusLost,
		"qualifiedtobuy": crmcontracts.DealStatusOpen,
		"":               crmcontracts.DealStatusOpen,
	} {
		rec := wireRecord(t, datasource.EntityDeal, map[string]any{"name": "Acme", "stage_id": stage})
		deal, err := overlayWireDeal(wireCtx(), rec)
		if err != nil {
			t.Fatalf("overlayWireDeal(%q): %v", stage, err)
		}
		if deal.Status != want {
			t.Errorf("stage %q → status %q, want %q", stage, deal.Status, want)
		}
	}
}

func TestOverlayWireDealParsesAmountAndCloseDate(t *testing.T) {
	rec := wireRecord(t, datasource.EntityDeal, map[string]any{
		"name": "Acme", "amount_minor": "125000", "expected_close_date": "2026-09-30",
	})
	deal, err := overlayWireDeal(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireDeal: %v", err)
	}
	if deal.AmountMinor == nil || *deal.AmountMinor != 125000 {
		t.Errorf("AmountMinor = %v, want 125000 (HubSpot amounts arrive as strings)", deal.AmountMinor)
	}
	if deal.ExpectedCloseDate == nil || deal.ExpectedCloseDate.Format("2006-01-02") != "2026-09-30" {
		t.Errorf("ExpectedCloseDate = %v, want 2026-09-30", deal.ExpectedCloseDate)
	}
}

// TestOverlayWireDealNullsPipelineAndStage is the OVA-MAP-6 contract proof:
// an overlay-mirror deal reads with NULL pipeline_id/stage_id (never a
// fabricated/zero UUID — a forbidden dangling FK), while the incumbent's own
// pipeline/dealstage identifiers ride raw.
func TestOverlayWireDealNullsPipelineAndStage(t *testing.T) {
	rec := wireRecord(t, datasource.EntityDeal, map[string]any{
		"name": "Acme", "pipeline_id": "default", "stage_id": "appointmentscheduled",
	})
	deal, err := overlayWireDeal(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireDeal: %v", err)
	}
	if deal.PipelineId != nil {
		t.Errorf("PipelineId = %v, want nil (overlay has no native pipeline row — OVA-MAP-6)", *deal.PipelineId)
	}
	if deal.StageId != nil {
		t.Errorf("StageId = %v, want nil (overlay has no native stage row — OVA-MAP-6)", *deal.StageId)
	}
	// The incumbent identifiers ride raw, never lost.
	if deal.Raw == nil || (*deal.Raw)["pipeline_id"] != "default" || (*deal.Raw)["stage_id"] != "appointmentscheduled" {
		t.Errorf("raw = %v, want the incumbent pipeline/dealstage identifiers preserved", deal.Raw)
	}
}

func TestFieldInt64RejectsNonIntegralNumbers(t *testing.T) {
	for name, v := range map[string]any{
		"fractional": 1.5, "huge": 1e19, "nan": math.NaN(), "inf": math.Inf(1), "text": "12.5",
	} {
		if got, ok := fieldInt64(map[string]any{"amount_minor": v}, "amount_minor"); ok {
			t.Errorf("%s: fieldInt64 = %d, want absent — a narrowed cast invents a different amount", name, got)
		}
	}
	if got, ok := fieldInt64(map[string]any{"amount_minor": float64(42)}, "amount_minor"); !ok || got != 42 {
		t.Errorf("integral float = (%d,%v), want (42,true)", got, ok)
	}
}

func TestOverlayWireActivityKindFallsBackToNoteAndParsesEpochMillis(t *testing.T) {
	rec := wireRecord(t, datasource.EntityActivity, map[string]any{
		"kind": "linkedin_message", "subject": "Ping", "occurred_at": "1767225600000",
	})
	act, err := overlayWireActivity(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	if act.Kind != crmcontracts.ActivityKindNote {
		t.Errorf("unknown engagement kind → %q, want note (the true kind stays in raw)", act.Kind)
	}
	if (*act.Raw)["kind"] != "linkedin_message" {
		t.Error("the true engagement kind must survive in raw")
	}
	want := time.UnixMilli(1767225600000).UTC()
	if !act.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want the parsed epoch-millis %v", act.OccurredAt, want)
	}
}

func TestOverlayWireActivityWithoutTimestampFallsBackToSyncInstant(t *testing.T) {
	rec := wireRecord(t, datasource.EntityActivity, map[string]any{"kind": "call"})
	act, err := overlayWireActivity(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	if act.Kind != crmcontracts.ActivityKindCall {
		t.Errorf("Kind = %q, want call", act.Kind)
	}
	if !act.OccurredAt.Equal(wireSyncedAt) {
		t.Errorf("OccurredAt = %v, want the sync-instant fallback %v", act.OccurredAt, wireSyncedAt)
	}
}

// TestOverlayWireActivitySurfacesDurationAndDueAt proves the wire assembler
// now consumes the canonical fields the mapping lands: duration_seconds
// (already seconds, OVA-MAP-2 — never re-divided) and a task's due_at
// (OVA-MAP-8), rather than dropping them into raw only.
func TestOverlayWireActivitySurfacesDurationAndDueAt(t *testing.T) {
	rec := wireRecord(t, datasource.EntityActivity, map[string]any{
		"kind": "call", "occurred_at": "2026-06-02T09:00:00.000Z", "duration_seconds": int64(90),
	})
	act, err := overlayWireActivity(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWireActivity: %v", err)
	}
	if act.DurationSeconds == nil || *act.DurationSeconds != 90 {
		t.Errorf("DurationSeconds = %v, want 90 (surfaced in seconds, not re-divided)", act.DurationSeconds)
	}

	task := wireRecord(t, datasource.EntityActivity, map[string]any{
		"kind": "task", "occurred_at": "2026-07-01T08:30:00.000Z", "due_at": "2026-07-10T17:00:00.000Z",
	})
	tact, err := overlayWireActivity(wireCtx(), task)
	if err != nil {
		t.Fatalf("overlayWireActivity(task): %v", err)
	}
	if tact.DueAt == nil || !tact.DueAt.Equal(time.Date(2026, 7, 10, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("DueAt = %v, want the task deadline surfaced", tact.DueAt)
	}
}

// TestOverlayWireTitlePrefersCanonicalFullName locks in the search-title
// precedence: when a person carries a canonical full_name that differs from
// first+last (the email-local/placeholder fallback, or an incumbent that set
// full_name independently), the search hit's title is the canonical value —
// matching the person detail — not a separately re-derived name.
func TestOverlayWireTitlePrefersCanonicalFullName(t *testing.T) {
	rec := wireRecord(t, datasource.EntityPerson, map[string]any{
		"full_name": "grace.hopper", "first_name": "", "last_name": "",
		"person_email": map[string]any{"email": "grace.hopper@navy.mil"},
	})
	person, err := overlayWirePerson(wireCtx(), rec)
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	title := overlayWireTitle(datasource.EntityPerson, *person.Raw)
	if title != "grace.hopper" {
		t.Errorf("search title = %q, want the canonical full_name %q (must match the person detail)", title, "grace.hopper")
	}
	if person.FullName != title {
		t.Errorf("person detail full_name %q and search title %q diverge", person.FullName, title)
	}
}

func TestOverlayWireTitlePicksThePerTypeDisplayField(t *testing.T) {
	for _, tc := range []struct {
		et     datasource.EntityType
		fields map[string]any
		want   string
	}{
		{datasource.EntityPerson, map[string]any{"first_name": "Ada", "last_name": "O"}, "Ada O"},
		{datasource.EntityOrganization, map[string]any{"display_name": "Acme GmbH"}, "Acme GmbH"},
		{datasource.EntityDeal, map[string]any{"name": "Renewal"}, "Renewal"},
		{datasource.EntityLead, map[string]any{"full_name": "Lea D"}, "Lea D"},
		{datasource.EntityActivity, map[string]any{"subject": "Kickoff"}, "Kickoff"},
	} {
		if got := overlayWireTitle(tc.et, tc.fields); got != tc.want {
			t.Errorf("title(%s) = %q, want %q", tc.et, got, tc.want)
		}
	}
}
