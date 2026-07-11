// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The record-history read (GET /records/{entity_type}/{id}/history): one
// plain-language line per audit row — the whole-mutation view next to
// field-history's per-field diffs. Gated exactly like every other record
// read (human-only, object-read, row-scope), keyset-paginated ASC, and cut
// at the erasure boundary — but tombstone-INCLUSIVE: the erase line is the
// honest disclosure that the record was scrubbed.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedRecordAuditRow inserts a raw person audit row with full control
// over the actor columns — the record-history read renders actor_id and
// on_behalf_of, which seedAuditActionRow (fieldhistory suite) pins to a
// fixed literal. This suite exercises entity-type dispatch through the
// shared gate stack (proven per-type by the fieldhistory suite), so its
// seeds stay on person. INSERT is the one verb the append-only trigger
// admits, and the workspace tx satisfies RLS.
func seedRecordAuditRow(t *testing.T, e *Env, action string, personID ids.UUID,
	actorType, actorID string, onBehalfOf *ids.UUID, before, after map[string]any, occurredAt time.Time,
) ids.UUID {
	t.Helper()
	rowID := ids.NewV7()
	ctx := principal.WithWorkspaceID(t.Context(), e.WS)
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, on_behalf_of,
			                        action, entity_type, entity_id, before, after, occurred_at)
			 VALUES ($1, $2, $3, $4, $5, $6, 'person', $7, $8, $9, $10)`,
			rowID, e.WS, actorType, actorID, onBehalfOf, action, personID,
			storekit.JSONArg(before), storekit.JSONArg(after), occurredAt)
		return err
	})
	if err != nil {
		t.Fatalf("seed record audit row: %v", err)
	}
	return rowID
}

// seedWorkspaceUser inserts an app_user with a distinct display name so
// the read's name resolution has something real to resolve (the harness
// seeds every rep as "Rep"). Owner connection like the harness itself:
// app_user rows are identity fixtures, not app-role writes.
func seedWorkspaceUser(t *testing.T, e *Env, displayName string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := OwnerConn(t).Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, $4)`,
		id, e.WS, id.String()+"@recordhistory.test", displayName); err != nil {
		t.Fatalf("seed app_user %q: %v", displayName, err)
	}
	return id
}

func TestRecordHistoryGatesOnPrincipalPermissionAndVisibility(t *testing.T) {
	e := Setup(t)
	// Owned by Rep1 (Team1): an ownerless record is workspace-shared at
	// every tier, so the out-of-scope assertion needs a real owner to
	// exclude the Team2-only caller.
	personID := e.SeedPerson(t, "Gated Subject", &e.Rep1)

	// Rep3 sits only in Team2: 404, not an empty page — existence-hiding
	// on the row-scope gate like every record read.
	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, RepPerms)
	if _, err := privacy.ListRecordHistory(outsider, e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("out-of-scope read: err = %v, want not found", err)
	}

	// A principal without person:read at all: 403 before any row is touched.
	noRead := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{RowScope: principal.RowScopeTeam})
	if _, err := privacy.ListRecordHistory(noRead, e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("no-permission read: err = %v, want permission denied", err)
	}

	// The history surface is human-only: an agent principal is refused
	// outright, before the entity gate.
	if _, err := privacy.ListRecordHistory(e.AgentCtx(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent-principal read: err = %v, want permission denied", err)
	}
}

func TestRecordHistoryRendersEveryActorChronologically(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "History Subject", nil)
	uma := seedWorkspaceUser(t, e, "Uma Underwriter")
	ada := seedWorkspaceUser(t, e, "Ada Authority")

	// SeedPerson's create row is stamped at real "now"; the four actor
	// rows are dated forward so ordering is unambiguous.
	base := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	seedRecordAuditRow(t, e, "update", personID, "human", "human:"+uma.String(), nil,
		map[string]any{"email": "old@x.com"}, map[string]any{"email": "new@x.com"}, base)
	seedRecordAuditRow(t, e, "update", personID, "agent", "agent:enrich", &ada,
		nil, map[string]any{"title": "CTO"}, base.Add(time.Hour))
	seedRecordAuditRow(t, e, "archive", personID, "system", "system", nil,
		nil, nil, base.Add(2*time.Hour))
	seedRecordAuditRow(t, e, "update", personID, "connector", "connector:hubspot", nil,
		nil, map[string]any{"phone": "1"}, base.Add(3*time.Hour))

	page, err := privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Entries) != 5 {
		t.Fatalf("entries = %d, want 5 (create genesis + four seeded actors): %+v", len(page.Entries), page.Entries)
	}
	if page.HasMore || page.NextCursor != "" {
		t.Errorf("single page must report exhaustion: has_more=%v cursor=%q", page.HasMore, page.NextCursor)
	}
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i].OccurredAt.Before(page.Entries[i-1].OccurredAt) {
			t.Fatalf("entries not chronological ASC at index %d: %v after %v",
				i, page.Entries[i].OccurredAt, page.Entries[i-1].OccurredAt)
		}
	}

	// The genesis row's actor is the harness admin — a synthetic user with
	// no app_user row, so the summary honestly falls back to the raw
	// prefixed actor_id instead of inventing a name.
	genesis := page.Entries[0]
	if genesis.Action != "create" ||
		!strings.HasPrefix(genesis.Summary, "human:") || !strings.HasSuffix(genesis.Summary, "created the record") {
		t.Errorf("genesis line = %q (action %q), want raw-actor_id create line", genesis.Summary, genesis.Action)
	}

	human := page.Entries[1]
	if human.Summary != "Uma Underwriter updated the record" {
		t.Errorf("human line = %q, want resolved display name", human.Summary)
	}
	if human.After["email"] != "new@x.com" || human.Before["email"] != "old@x.com" {
		t.Errorf("human line payload = before %v after %v, want the seeded images served", human.Before, human.After)
	}

	agent := page.Entries[2]
	if agent.Summary != "Agent acting for Ada Authority updated the record" {
		t.Errorf("agent line = %q, want the delegating authority woven in", agent.Summary)
	}
	if agent.OnBehalfOf == nil || *agent.OnBehalfOf != ada {
		t.Errorf("agent line OnBehalfOf = %v, want %v", agent.OnBehalfOf, ada)
	}
	if agent.OnBehalfOfName == nil || *agent.OnBehalfOfName != "Ada Authority" {
		t.Errorf("agent line OnBehalfOfName = %v, want Ada Authority", agent.OnBehalfOfName)
	}

	if got := page.Entries[3].Summary; got != "System archived the record" {
		t.Errorf("system line = %q", got)
	}
	if got := page.Entries[4].Summary; got != "Connector updated the record" {
		t.Errorf("connector line = %q", got)
	}
}

// TestRecordHistoryErasureBoundaryServesOnlyTheTombstone proves D1's
// tombstone-INCLUSIVE cut: after the REAL erasure engine runs, every line
// older than the erase tombstone is withheld (their before/after images
// are exactly the PII the scrub certified gone), while the tombstone line
// itself IS served — its images are empty (the suppression tallies ride
// audit_log.evidence, which this read never selects), so the erase line is
// honest disclosure, not a leak.
func TestRecordHistoryErasureBoundaryServesOnlyTheTombstone(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Selma Subject", nil)
	past := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	seedRecordAuditRow(t, e, "update", personID, "human", "user-1", nil,
		map[string]any{"email": "selma@example.com"},
		map[string]any{"email": "selma.subject@example.com"}, past)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), personID, "dsr"); err != nil {
		t.Fatalf("erase: %v", err)
	}

	page, err := privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("post-erasure list: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d, want exactly the tombstone (pre-erasure lines withheld): %+v",
			len(page.Entries), page.Entries)
	}
	tomb := page.Entries[0]
	if tomb.Action != "erase" {
		t.Fatalf("surviving line action = %q, want erase", tomb.Action)
	}
	// The eraser stamps the calling principal — the harness admin is a
	// human — so the line renders from the human branch, honestly.
	if tomb.ActorType != "human" || !strings.HasSuffix(tomb.Summary, "erased the record") {
		t.Errorf("tombstone line = %q (actor %q), want an erased-the-record line by the caller",
			tomb.Summary, tomb.ActorType)
	}
	if len(tomb.Before) != 0 || len(tomb.After) != 0 {
		t.Errorf("tombstone images must be empty (meta rides evidence): before %v after %v",
			tomb.Before, tomb.After)
	}

	// The boundary is a cut, not a ban: a change made AFTER the scrub is
	// ordinary history again, rendered behind the erase line.
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	seedRecordAuditRow(t, e, "update", personID, "human", "user-1", nil,
		nil, map[string]any{"owner_id": "rep-2"}, future)
	page, err = privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("post-scrub list: %v", err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Action != "erase" || page.Entries[1].Action != "update" {
		t.Fatalf("post-scrub timeline = %+v, want [erase, update]", page.Entries)
	}
}

func TestRecordHistoryKeysetWalksAscendingWithoutOverlap(t *testing.T) {
	e := Setup(t)
	// SeedPerson's create row is the true oldest line; two forward-dated
	// updates make three rows total.
	personID := e.SeedPerson(t, "Paging Subject", nil)
	base := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	r2 := seedRecordAuditRow(t, e, "update", personID, "human", "user-1", nil,
		map[string]any{"phone": "1"}, map[string]any{"phone": "2"}, base)
	r3 := seedRecordAuditRow(t, e, "update", personID, "human", "user-1", nil,
		map[string]any{"phone": "2"}, map[string]any{"phone": "3"}, base.Add(time.Hour))

	one := 1
	var walked []ids.UUID
	var cursor *string
	for pageNo := 1; pageNo <= 3; pageNo++ {
		page, err := privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
			EntityType: "person", EntityID: personID, Limit: &one, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pageNo, err)
		}
		if len(page.Entries) != 1 {
			t.Fatalf("page %d entries = %d, want 1", pageNo, len(page.Entries))
		}
		walked = append(walked, page.Entries[0].ID)
		if pageNo < 3 {
			if !page.HasMore || page.NextCursor == "" {
				t.Fatalf("page %d must report more rows follow", pageNo)
			}
			cursor = &page.NextCursor
		} else if page.HasMore || page.NextCursor != "" {
			t.Fatalf("page 3 is genuine exhaustion — has_more must not lie")
		}
	}
	if walked[1] != r2 || walked[2] != r3 {
		t.Fatalf("walk order = %v, want [genesis, %v, %v]", walked, r2, r3)
	}
	seen := map[ids.UUID]bool{}
	for _, id := range walked {
		if seen[id] {
			t.Fatalf("row %v served on two pages — keyset overlap", id)
		}
		seen[id] = true
	}
}

// TestRecordHistoryHonestEmptyPageBeyondTheFinalRow: every store write
// audits itself, so a VISIBLE record with a truly empty spine cannot be
// seeded honestly through the stores — the honest zero-match construction
// is a cursor positioned past the record's final row: the full gate stack
// still runs, and the scan matches nothing.
func TestRecordHistoryHonestEmptyPageBeyondTheFinalRow(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Quiet Subject", nil)

	full, err := privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID,
	})
	if err != nil {
		t.Fatalf("full list: %v", err)
	}
	if len(full.Entries) == 0 {
		t.Fatal("SeedPerson must have audited its own create — harness drift")
	}
	last := full.Entries[len(full.Entries)-1]
	pastTheEnd := storekit.EncodeCursor(last.OccurredAt, last.ID)

	page, err := privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID, Cursor: &pastTheEnd,
	})
	if err != nil {
		t.Fatalf("empty page must not error: %v", err)
	}
	if page.Entries == nil || len(page.Entries) != 0 || page.HasMore || page.NextCursor != "" {
		t.Fatalf("want honest empty page (non-nil, zero entries, no more): %+v", page)
	}
}

func TestRecordHistoryMalformedCursorIsAClientFault(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Cursor Subject", nil)
	bad := "%%%not-a-cursor"
	_, err := privacy.ListRecordHistory(e.Admin(), e.Pool, privacy.RecordHistoryFilter{
		EntityType: "person", EntityID: personID, Cursor: &bad,
	})
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("err = %v, want *storekit.MalformedCursorError untouched", err)
	}
}
