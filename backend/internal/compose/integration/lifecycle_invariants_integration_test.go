// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Regression lane for the store's cross-cutting invariants: the deal
// lifecycle (amount/currency pairing, terminal-field clearing on reopen,
// owner-change events), activity link-scoping, and the scope-safe dedupe
// 409 — each pins a bug class, not a happy path.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestUpdateDealRejectsAStrandedAmount(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	admin := e.Admin()

	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "No money yet", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	amount := int64(5000)
	_, err = e.Deals.UpdateDeal(admin, ids.UUID(d.Id), deals.UpdateDealInput{AmountMinor: &amount})
	var pairErr *deals.AmountCurrencyPairError
	if !errors.As(err, &pairErr) {
		t.Fatalf("amount without currency → %v, want deals.AmountCurrencyPairError", err)
	}

	// The paired update is accepted, and clearing neither alone either.
	currency := "EUR"
	if _, err := e.Deals.UpdateDeal(admin, ids.UUID(d.Id), deals.UpdateDealInput{AmountMinor: &amount, Currency: &currency}); err != nil {
		t.Fatalf("paired amount+currency: %v", err)
	}
}

func TestReopeningAWonDealClearsTerminalFields(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	admin := e.Admin()

	amount, currency := int64(100000), "EUR"
	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Round trip", PipelineID: pipeline, StageID: open, Source: "manual",
		AmountMinor: &amount, Currency: &currency,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deals.AdvanceDeal(admin, ids.UUID(d.Id), deals.AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("closing as won: %v", err)
	}
	if _, err := e.Deals.AdvanceDeal(admin, ids.UUID(d.Id), deals.AdvanceDealInput{ToStageID: open}); err != nil {
		t.Fatalf("reopening: %v", err)
	}

	owner := OwnerConn(t)
	var status string
	var closedAt, lostReason, fxRate, fxDate *string
	err = owner.QueryRow(context.Background(),
		`SELECT status, closed_at::text, lost_reason, fx_rate_to_base::text, fx_rate_date::text FROM deal WHERE id = $1`,
		ids.UUID(d.Id)).Scan(&status, &closedAt, &lostReason, &fxRate, &fxDate)
	if err != nil {
		t.Fatal(err)
	}
	if status != "open" {
		t.Fatalf("status = %s after reopen, want open", status)
	}
	for name, v := range map[string]*string{"closed_at": closedAt, "lost_reason": lostReason, "fx_rate_to_base": fxRate, "fx_rate_date": fxDate} {
		if v != nil {
			t.Errorf("reopened deal still carries %s = %q — corrupts won/lost reporting", name, *v)
		}
	}
}

func TestOwnerReassignmentEmitsOwnerChanged(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	admin := e.Admin()

	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Handover", PipelineID: pipeline, StageID: open, Source: "manual", OwnerID: &e.Rep1,
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "Handover (renamed)"
	if _, err := e.Deals.UpdateDeal(admin, ids.UUID(d.Id), deals.UpdateDealInput{OwnerID: &e.Rep2, Name: &name}); err != nil {
		t.Fatal(err)
	}

	owner := OwnerConn(t)
	rows, err := owner.Query(context.Background(),
		`SELECT envelope->>'type', envelope->'payload' FROM event_outbox ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]json.RawMessage{}
	var order []string
	for rows.Next() {
		var typ string
		var payload json.RawMessage
		if err := rows.Scan(&typ, &payload); err != nil {
			t.Fatal(err)
		}
		types[typ] = payload
		order = append(order, typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	ownerPayload, ok := types["deal.owner_changed"]
	if !ok {
		t.Fatalf("no deal.owner_changed staged; events seen: %v", order)
	}
	var oc struct {
		From *ids.UUID `json:"from_owner_id"`
		To   ids.UUID  `json:"to_owner_id"`
	}
	if err := json.Unmarshal(ownerPayload, &oc); err != nil {
		t.Fatal(err)
	}
	if oc.From == nil || *oc.From != e.Rep1 || oc.To != e.Rep2 {
		t.Errorf("owner_changed payload %s, want rep1→rep2", ownerPayload)
	}
	// The co-occurring rename still emits deal.updated — WITHOUT the owner
	// field (owner transitions are never folded into the generic event).
	updated, ok := types["deal.updated"]
	if !ok {
		t.Fatal("the co-occurring field change lost its deal.updated")
	}
	var rest map[string]any
	if err := json.Unmarshal(updated, &rest); err != nil {
		t.Fatal(err)
	}
	if _, leaked := rest["owner_id"]; leaked {
		t.Error("deal.updated payload carries owner_id; the transition belongs to deal.owner_changed alone")
	}
}

func TestActivityReadsAreScopedThroughLinks(t *testing.T) {
	e := Setup(t)
	foreignPerson := e.SeedPerson(t, "Foreign owner", &e.Rep3)
	myPerson := e.SeedPerson(t, "Mine", &e.Rep1)
	admin := e.Admin()

	secret, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
		Kind: "note", Subject: strPtr("Confidential pricing call"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: foreignPerson}},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
		Kind: "note", Subject: strPtr("Team call"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: myPerson}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unlinked, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
		Kind: "note", Subject: strPtr("Workspace-wide note"), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, repPermsWithActivity())

	// Get: the activity attached to another team's person answers 404.
	if _, err := e.Activities.GetActivity(rep, ids.UUID(secret.Id), storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("foreign-linked activity → %v, want ErrNotFound", err)
	}
	if _, err := e.Activities.GetActivity(rep, ids.UUID(visible.Id), storekit.LiveOnly); err != nil {
		t.Errorf("team-linked activity → %v, want success", err)
	}

	// List: the timeline never surfaces it, including via the entity filter.
	list, _, err := e.Activities.ListActivities(rep, activities.ListActivitiesInput{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[ids.UUID]bool{}
	for _, a := range list {
		seen[ids.UUID(a.Id)] = true
	}
	if seen[ids.UUID(secret.Id)] {
		t.Error("timeline surfaced an activity linked only to another team's record")
	}
	if !seen[ids.UUID(visible.Id)] || !seen[ids.UUID(unlinked.Id)] {
		t.Error("timeline lost a visible or workspace-shared activity")
	}

	entityType, entityID := "person", foreignPerson
	probed, _, err := e.Activities.ListActivities(rep, activities.ListActivitiesInput{EntityType: &entityType, EntityID: &entityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(probed) != 0 {
		t.Errorf("entity-filter probe on a foreign person returned %d activities, want 0", len(probed))
	}
}

func TestDuplicate409DoesNotDiscloseOutOfScopeIDs(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	if _, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Owned elsewhere", OwnerID: &e.Rep3, Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "taken@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	_, err := e.People.CreatePerson(rep, people.CreatePersonInput{
		FullName: "Duplicate attempt", Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "taken@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	})
	var dup *people.DuplicateEmailError
	if !errors.As(err, &dup) {
		t.Fatalf("duplicate create → %v, want people.DuplicateEmailError", err)
	}
	if !dup.ExistingID.IsZero() {
		t.Errorf("409 disclosed out-of-scope id %s", dup.ExistingID)
	}

	// The same conflict against a row the rep CAN see keeps the id — the
	// dedupe UX ("open the existing record") survives for legit cases.
	if _, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Teammate's", OwnerID: &e.Rep2, Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "team@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = e.People.CreatePerson(rep, people.CreatePersonInput{
		FullName: "Duplicate attempt 2", Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "team@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	})
	if !errors.As(err, &dup) {
		t.Fatalf("visible duplicate → %v, want people.DuplicateEmailError", err)
	}
	if dup.ExistingID.IsZero() {
		t.Error("409 for a visible duplicate should carry the existing id")
	}
}

// repPermsWithActivity extends the rep fixture with activity grants for
// the timeline tests.
func repPermsWithActivity() principal.Permissions {
	p := RepPerms
	objects := make(map[string]principal.ObjectGrant, len(p.Objects)+1)
	for k, v := range p.Objects {
		objects[k] = v
	}
	objects["activity"] = principal.ObjectGrant{Create: true, Read: true, Update: true}
	p.Objects = objects
	return p
}

// lostStage resolves the seeded pipeline's lost stage.
func lostStage(t *testing.T, e *Env) ids.UUID {
	t.Helper()
	p, err := e.Deals.DefaultPipeline(e.Admin())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range *p.Stages {
		if st.Semantic == "lost" {
			return ids.UUID(st.Id)
		}
	}
	t.Fatal("seeded pipeline has no lost stage")
	return ids.Nil
}

// A closed deal's frozen FX must track its amount/currency: adding an
// amount to a deal that was closed amountless must freeze a rate (not
// trip deal_closed_fx into a 500), and changing the currency must
// re-freeze as of the CLOSE date (not leave the old currency's rate
// silently corrupting base-currency roll-ups).
func TestRepricingAClosedDealRefreezesFx(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	admin := e.Admin()

	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Closed amountless", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deals.AdvanceDeal(admin, ids.UUID(d.Id), deals.AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("closing amountless: %v", err)
	}

	owner := OwnerConn(t)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		 VALUES ($1, 'USD', 'EUR', 0.9200000000, current_date)`, e.WS); err != nil {
		t.Fatal(err)
	}

	readFx := func() (rate, date *string) {
		t.Helper()
		if err := owner.QueryRow(context.Background(),
			`SELECT fx_rate_to_base::text, fx_rate_date::text FROM deal WHERE id = $1`,
			ids.UUID(d.Id)).Scan(&rate, &date); err != nil {
			t.Fatal(err)
		}
		return rate, date
	}

	// Adding an amount to the closed deal freezes a rate (base currency → 1).
	amount, eur := int64(48000), "EUR"
	if _, err := e.Deals.UpdateDeal(admin, ids.UUID(d.Id), deals.UpdateDealInput{AmountMinor: &amount, Currency: &eur}); err != nil {
		t.Fatalf("adding amount to a closed deal: %v", err)
	}
	rate, _ := readFx()
	if rate == nil {
		t.Fatal("closed deal gained an amount but no frozen FX — deal_closed_fx would have 500ed before the fix")
	}

	// Switching the closed deal's currency re-freezes for the NEW pair.
	usd := "USD"
	if _, err := e.Deals.UpdateDeal(admin, ids.UUID(d.Id), deals.UpdateDealInput{Currency: &usd}); err != nil {
		t.Fatalf("re-currencying a closed deal: %v", err)
	}
	rate, _ = readFx()
	if rate == nil || *rate != "0.9200000000" {
		t.Errorf("frozen rate after currency change = %v, want the USD→EUR 0.92 (a stale rate silently corrupts roll-ups)", rate)
	}
}

// Deals are born open: creation directly onto a won/lost stage would put
// an "open" deal on a terminal column with no closed_at/FX — the
// invariant AdvanceDeal exists to maintain, bypassed at birth.
func TestCreateDealRejectsATerminalStage(t *testing.T) {
	e := Setup(t)
	pipeline, _, won := DealFixture(t, e)

	_, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "Born won", PipelineID: pipeline, StageID: won, Source: "manual",
	})
	var terminal *deals.TerminalStageOnCreateError
	if !errors.As(err, &terminal) {
		t.Fatalf("create on won stage → %v, want deals.TerminalStageOnCreateError", err)
	}
}

// A client that reopens a lost deal while (redundantly) re-sending the
// lost_reason must not produce a duplicate SET clause — the reopen wins
// and clears the field.
func TestReopeningWithARedundantLostReasonStillCleans(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	lost := lostStage(t, e)
	admin := e.Admin()

	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Lost and found", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deals.AdvanceDeal(admin, ids.UUID(d.Id), deals.AdvanceDealInput{ToStageID: lost, LostReason: strPtr("price")}); err != nil {
		t.Fatalf("closing as lost: %v", err)
	}
	reopened, err := e.Deals.AdvanceDeal(admin, ids.UUID(d.Id), deals.AdvanceDealInput{ToStageID: open, LostReason: strPtr("price")})
	if err != nil {
		t.Fatalf("reopen with redundant lost_reason: %v", err)
	}
	if reopened.LostReason != nil {
		t.Errorf("reopened deal kept lost_reason %q", *reopened.LostReason)
	}
}

// The idempotent-replay path returns a record, so it is a read: replaying
// someone else's external source key must answer a bare conflict, never
// the out-of-scope record's content.
func TestIdempotentReplayDoesNotDiscloseOutOfScopeRecords(t *testing.T) {
	e := Setup(t)
	foreignPerson := e.SeedPerson(t, "Foreign", &e.Rep3)
	admin := e.Admin()

	src, key := "gmail", "msg-123"
	if _, _, err := e.Activities.LogActivity(admin, activities.LogActivityInput{
		Kind: "email", Subject: strPtr("Confidential thread"), Source: "connector",
		SourceSystem: &src, SourceID: &key,
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: foreignPerson}},
	}); err != nil {
		t.Fatal(err)
	}
	leadSrc, leadKey := "apollo", "lead-9"
	if _, _, err := e.People.CreateLead(admin, people.CreateLeadInput{
		FullName: strPtr("Foreign lead"), OwnerID: &e.Rep3, Source: "import",
		SourceSystem: &leadSrc, SourceID: &leadKey,
	}); err != nil {
		t.Fatal(err)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, repPermsWithCapture())

	if _, _, err := e.Activities.LogActivity(rep, activities.LogActivityInput{
		Kind: "email", Source: "connector", SourceSystem: &src, SourceID: &key,
	}); !errors.Is(err, apperrors.ErrConflict) {
		t.Errorf("activity replay of a foreign source key → %v, want bare ErrConflict", err)
	}
	if _, _, err := e.People.CreateLead(rep, people.CreateLeadInput{
		FullName: strPtr("Replay attempt"), Source: "import",
		SourceSystem: &leadSrc, SourceID: &leadKey,
	}); !errors.Is(err, apperrors.ErrConflict) {
		t.Errorf("lead replay of a foreign source key → %v, want bare ErrConflict", err)
	}
}

// Link targets are validated under RLS + row scope before the insert: the
// FK alone runs as the table owner and would persist a guessed foreign or
// out-of-scope UUID as a link.
func TestActivityLinkTargetsMustBeVisible(t *testing.T) {
	e := Setup(t)
	foreignPerson := e.SeedPerson(t, "Foreign", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, repPermsWithCapture())

	if _, _, err := e.Activities.LogActivity(rep, activities.LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: foreignPerson}},
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("link to an out-of-scope person → %v, want ErrNotFound", err)
	}
	if _, _, err := e.Activities.LogActivity(rep, activities.LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: ids.NewV7()}},
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("link to a nonexistent person → %v, want ErrNotFound", err)
	}
}

// repPermsWithCapture extends the rep fixture with the capture-side
// grants (activity + lead) the replay tests need.
func repPermsWithCapture() principal.Permissions {
	p := repPermsWithActivity()
	objects := make(map[string]principal.ObjectGrant, len(p.Objects)+1)
	for k, v := range p.Objects {
		objects[k] = v
	}
	objects["lead"] = principal.ObjectGrant{Create: true, Read: true, Update: true}
	p.Objects = objects
	return p
}
