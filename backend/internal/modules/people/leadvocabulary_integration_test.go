// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The two administered lead vocabularies against Postgres: the seed the
// migration writes agrees with the scorer's fixture, the CRUD's refusals are
// the ones the contract promises, and a lead's source and disqualify reason
// round-trip through the lead read.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The migration's seed and the code's fixture name the same six keys with
// the same weights: a seed edited without the fixture (or the reverse) fails
// here rather than in a scorer that quietly scores differently on a fresh
// installation than the unit tests say it does.
func TestLeadSourceSeedMatchesTheScorerFixture(t *testing.T) {
	e := setupPromoteConsent(t)
	var seeded SourceIntents
	if err := e.store.tx(e.ctx, func(tx pgx.Tx) (err error) {
		seeded, err = loadSourceIntents(e.ctx, tx)
		return err
	}); err != nil {
		t.Fatalf("load intents: %v", err)
	}
	if len(seeded) != len(defaultSourceIntents) {
		t.Fatalf("seeded sources = %v, want exactly the fixture %v", seeded, defaultSourceIntents)
	}
	for key, want := range defaultSourceIntents {
		if got := seeded[key]; got != want {
			t.Errorf("seeded %q = %q, want %q", key, got, want)
		}
	}
	reasons, err := e.store.ListLeadDisqualifyReasons(e.ctx)
	if err != nil {
		t.Fatalf("list reasons: %v", err)
	}
	if len(reasons) != 8 {
		t.Errorf("seeded reasons = %d, want the eight stock reasons", len(reasons))
	}
}

func TestLeadSourceCRUDRefusesWhatTheContractPromises(t *testing.T) {
	e := setupPromoteConsent(t)
	created, err := e.store.CreateLeadSource(e.ctx, CreateLeadSourceInput{Label: "Trade show", Intent: SourceIntentHigh})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Key != "trade_show" || created.Intent != crmcontracts.LeadSourceIntentHigh || created.System == nil || *created.System {
		t.Fatalf("created = %+v, want key trade_show, intent high, not system", created)
	}
	if _, err := e.store.CreateLeadSource(e.ctx, CreateLeadSourceInput{Label: "Trade Show"}); !errors.Is(err, apperrors.ErrConflict) {
		t.Errorf("duplicate key err = %v, want ErrConflict", err)
	}
	if _, err := e.store.CreateLeadSource(e.ctx, CreateLeadSourceInput{Label: "  "}); err == nil {
		t.Error("blank label was accepted")
	}

	renamed := "Messe"
	inactive := false
	updated, err := e.store.UpdateLeadSource(e.ctx, ids.UUID(created.Id), UpdateLeadSourceInput{Label: &renamed, Active: &inactive})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Label != "Messe" || updated.Active || updated.Key != "trade_show" {
		t.Errorf("updated = %+v, want label Messe, inactive, key unchanged", updated)
	}

	list, err := e.store.ListLeadSources(e.ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var importSource *crmcontracts.LeadSource
	for i := range list.Data {
		if list.Data[i].Key == "import" {
			importSource = &list.Data[i]
		}
	}
	if importSource == nil {
		t.Fatal("the seeded import source is missing from the list")
	}
	if err := e.store.DeleteLeadSource(e.ctx, ids.UUID(importSource.Id)); !errors.Is(err, apperrors.ErrConflict) {
		t.Errorf("deleting a built-in err = %v, want ErrConflict", err)
	}
	if err := e.store.DeleteLeadSource(e.ctx, ids.UUID(created.Id)); err != nil {
		t.Errorf("deleting an unused custom source err = %v, want nil", err)
	}

	rep := e.withGrants(map[string]principal.ObjectGrant{
		"lead": {Create: true, Read: true, Update: true, Delete: true}, "custom_field": {Read: true},
	})
	if _, err := e.store.CreateLeadSource(rep, CreateLeadSourceInput{Label: "Rep source"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("rep create err = %v, want ErrPermissionDenied", err)
	}
	if _, err := e.store.ListLeadSources(rep); err != nil {
		t.Errorf("rep list err = %v, want the read to be granted", err)
	}
}

// A human's lead follows the list: an inactive administered key is refused
// on create and on correction, a connector value passes untouched, and a
// corrected source rescores the lead at once. The weight comes from the
// table, so adopting a connector family moves every lead it wrote.
func TestLeadSourceGovernsHumanWritesAndTheScore(t *testing.T) {
	e := setupPromoteConsent(t)
	list, err := e.store.ListLeadSources(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var crawl crmcontracts.LeadSource
	for _, s := range list.Data {
		if s.Key == "crawl" {
			crawl = s
		}
	}
	inactive := false
	if _, err := e.store.UpdateLeadSource(e.ctx, ids.UUID(crawl.Id), UpdateLeadSourceInput{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	email := "vocab@example.test"
	var inactiveErr *InactiveLeadSourceError
	if _, _, err := e.store.CreateLead(e.ctx, CreateLeadInput{Email: &email, Source: "crawl"}); !errors.As(err, &inactiveErr) {
		t.Fatalf("create with an inactive source err = %v, want InactiveLeadSourceError", err)
	}
	lead, _, err := e.store.CreateLead(e.ctx, CreateLeadInput{Email: &email, Source: "connector:apollo:a-1"})
	if err != nil {
		t.Fatalf("create with a connector value: %v", err)
	}
	if lead.Score != 0 || lead.SourceLabel != nil {
		t.Errorf("connector lead = score %d label %v, want 0 and no label until the family is adopted", lead.Score, lead.SourceLabel)
	}

	leadID := ids.From[ids.LeadKind](ids.UUID(lead.Id))
	crawlKey := "crawl"
	if _, err := e.store.UpdateLead(e.ctx, leadID, UpdateLeadInput{Source: &crawlKey}); !errors.As(err, &inactiveErr) {
		t.Errorf("correcting to an inactive source err = %v, want InactiveLeadSourceError", err)
	}
	referral := "referral"
	corrected, err := e.store.UpdateLead(e.ctx, leadID, UpdateLeadInput{Source: &referral})
	if err != nil {
		t.Fatalf("correct source: %v", err)
	}
	if corrected.Score != fitHighIntentSourcePoints || corrected.SourceLabel == nil || *corrected.SourceLabel != "Referral" {
		t.Errorf("corrected = score %d label %v, want %d and \"Referral\"", corrected.Score, corrected.SourceLabel, fitHighIntentSourcePoints)
	}

	// Adopting the connector family weights the leads that still carry it.
	other := "other@example.test"
	connectorLead, _, err := e.store.CreateLead(e.ctx, CreateLeadInput{Email: &other, Source: "connector:apollo:a-2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateLeadSource(e.ctx, CreateLeadSourceInput{Key: "connector:apollo", Label: "Apollo", Intent: SourceIntentHigh}); err != nil {
		t.Fatalf("adopt connector family: %v", err)
	}
	if err := e.store.RecomputeLeadScore(e.ctx, ids.From[ids.LeadKind](ids.UUID(connectorLead.Id)), connectorLead.CreatedAt); err != nil {
		t.Fatal(err)
	}
	after, err := e.store.GetLead(e.ctx, ids.From[ids.LeadKind](ids.UUID(connectorLead.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if after.Score != fitHighIntentSourcePoints || after.ScoreReason == nil || *after.ScoreReason != "high_intent_source" {
		t.Errorf("adopted connector lead = score %d reason %v, want %d from the family's intent", after.Score, after.ScoreReason, fitHighIntentSourcePoints)
	}
	list, err = e.store.ListLeadSources(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list.Data {
		if s.Key == "connector:apollo" && (s.LeadCount == nil || *s.LeadCount != 1) {
			t.Errorf("connector family lead_count = %v, want 1 counted through the prefix", s.LeadCount)
		}
	}
	for _, d := range list.Discovered {
		if d.Key == "connector:apollo" {
			t.Errorf("an adopted family still reads as discovered: %+v", d)
		}
	}

	// The counts are a lead read, and a lead is customer identity: an actor
	// scoped to their own rows still reads every lead in the workspace, so
	// owning the leads elsewhere changes nothing about what they count.
	owner := ids.From[ids.UserKind](e.user)
	for _, id := range []ids.LeadID{leadID, ids.From[ids.LeadKind](ids.UUID(connectorLead.Id))} {
		if _, err := e.store.UpdateLead(e.ctx, id, UpdateLeadInput{OwnerID: &owner}); err != nil {
			t.Fatalf("assign owner: %v", err)
		}
	}
	ownScope := principal.WithActor(principal.WithCorrelationID(principal.WithWorkspaceID(context.Background(), e.ws), ids.NewV7()),
		principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
			Permissions: principal.Permissions{
				RoleKeys: []string{"rep"},
				Objects:  map[string]principal.ObjectGrant{"lead": {Read: true}, "custom_field": {Read: true}},
				RowScope: principal.RowScopeOwn,
			},
		})
	scoped, err := e.store.ListLeadSources(ownScope)
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	counts := map[string]int{}
	for _, s := range list.Data {
		if s.LeadCount != nil {
			counts[s.Key] = *s.LeadCount
		}
	}
	for _, s := range scoped.Data {
		got := 0
		if s.LeadCount != nil {
			got = *s.LeadCount
		}
		if got != counts[s.Key] {
			t.Errorf("own-scoped actor counts %d leads under %q, want %d — a lead is workspace-readable", got, s.Key, counts[s.Key])
		}
	}
	if len(scoped.Discovered) != len(list.Discovered) {
		t.Errorf("own-scoped actor sees discovered sources %+v, want the same %d every seat sees", scoped.Discovered, len(list.Discovered))
	}
}

// Disqualifying records the reason and note on the lead, refuses a reason
// that is not active, and the reason's lead_count and RESTRICT delete follow.
func TestDisqualifyRecordsAnActiveReason(t *testing.T) {
	e := setupPromoteConsent(t)
	reasons, err := e.store.ListLeadDisqualifyReasons(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	badTiming := reasons[1]
	lead := e.seedLead(t, "dq@example.test")

	unknown := ids.NewV7()
	var unknownErr *UnknownDisqualifyReasonError
	if _, err := e.store.DisqualifyLead(e.ctx, lead, DisqualifyLeadInput{ReasonID: &unknown}); !errors.As(err, &unknownErr) {
		t.Fatalf("unknown reason err = %v, want UnknownDisqualifyReasonError", err)
	}
	note := "Call back in Q3"
	reasonID := ids.UUID(badTiming.Id)
	closed, err := e.store.DisqualifyLead(e.ctx, lead, DisqualifyLeadInput{ReasonID: &reasonID, Note: &note})
	if err != nil {
		t.Fatalf("disqualify: %v", err)
	}
	if closed.Status != crmcontracts.LeadStatusDisqualified || closed.DisqualifyReason == nil || *closed.DisqualifyReason != badTiming.Label ||
		closed.DisqualifyNote == nil || *closed.DisqualifyNote != note || closed.DisqualifyReasonId == nil {
		t.Errorf("closed = %+v, want disqualified with reason %q and the note", closed, badTiming.Label)
	}
	if err := e.store.DeleteLeadDisqualifyReason(e.ctx, reasonID); !errors.Is(err, apperrors.ErrConflict) {
		t.Errorf("deleting a built-in in-use reason err = %v, want ErrConflict", err)
	}
	custom, err := e.store.CreateLeadDisqualifyReason(e.ctx, CreateLeadDisqualifyReasonInput{Label: "Went quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteLeadDisqualifyReason(e.ctx, ids.UUID(custom.Id)); err != nil {
		t.Errorf("deleting an unused custom reason err = %v, want nil", err)
	}
}
