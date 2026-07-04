//go:build integration

package store

// Regression lane for the store's cross-cutting invariants: the deal
// lifecycle (amount/currency pairing, terminal-field clearing on reopen,
// owner-change events), activity link-scoping, and the scope-safe dedupe
// 409 — each pins a bug class, not a happy path.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/crmctx"
	"github.com/gradionhq/margince/backend/kernel/errs"
	"github.com/gradionhq/margince/backend/kernel/ids"
)

// dealFixture provisions a workspace with the seeded default pipeline
// and returns the open + won stage ids.
func dealFixture(t *testing.T, e *authzEnv) (pipeline, open, won ids.UUID) {
	t.Helper()
	admin := e.admin()
	if err := e.store.SeedDefaults(admin); err != nil {
		t.Fatal(err)
	}
	p, err := e.store.DefaultPipeline(admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range *p.Stages {
		switch st.Semantic {
		case "open":
			if open.IsZero() {
				open = ids.UUID(st.Id)
			}
		case "won":
			won = ids.UUID(st.Id)
		}
	}
	return ids.UUID(p.Id), open, won
}

func TestUpdateDealRejectsAStrandedAmount(t *testing.T) {
	e := setupAuthz(t)
	pipeline, open, _ := dealFixture(t, e)
	admin := e.admin()

	d, err := e.store.CreateDeal(admin, CreateDealInput{
		Name: "No money yet", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	amount := int64(5000)
	_, err = e.store.UpdateDeal(admin, ids.UUID(d.Id), UpdateDealInput{AmountMinor: &amount})
	var pairErr *AmountCurrencyPairError
	if !errors.As(err, &pairErr) {
		t.Fatalf("amount without currency → %v, want AmountCurrencyPairError", err)
	}

	// The paired update is accepted, and clearing neither alone either.
	currency := "EUR"
	if _, err := e.store.UpdateDeal(admin, ids.UUID(d.Id), UpdateDealInput{AmountMinor: &amount, Currency: &currency}); err != nil {
		t.Fatalf("paired amount+currency: %v", err)
	}
}

func TestReopeningAWonDealClearsTerminalFields(t *testing.T) {
	e := setupAuthz(t)
	pipeline, open, won := dealFixture(t, e)
	admin := e.admin()

	amount, currency := int64(100000), "EUR"
	d, err := e.store.CreateDeal(admin, CreateDealInput{
		Name: "Round trip", PipelineID: pipeline, StageID: open, Source: "manual",
		AmountMinor: &amount, Currency: &currency,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AdvanceDeal(admin, ids.UUID(d.Id), AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("closing as won: %v", err)
	}
	if _, err := e.store.AdvanceDeal(admin, ids.UUID(d.Id), AdvanceDealInput{ToStageID: open}); err != nil {
		t.Fatalf("reopening: %v", err)
	}

	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
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
	e := setupAuthz(t)
	pipeline, open, _ := dealFixture(t, e)
	admin := e.admin()

	d, err := e.store.CreateDeal(admin, CreateDealInput{
		Name: "Handover", PipelineID: pipeline, StageID: open, Source: "manual", OwnerID: &e.rep1,
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "Handover (renamed)"
	if _, err := e.store.UpdateDeal(admin, ids.UUID(d.Id), UpdateDealInput{OwnerID: &e.rep2, Name: &name}); err != nil {
		t.Fatal(err)
	}

	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
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
	if oc.From == nil || *oc.From != e.rep1 || oc.To != e.rep2 {
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
	e := setupAuthz(t)
	foreignPerson := e.seedPerson(t, "Foreign owner", &e.rep3)
	myPerson := e.seedPerson(t, "Mine", &e.rep1)
	admin := e.admin()

	secret, _, err := e.store.LogActivity(admin, LogActivityInput{
		Kind: "note", Subject: strPtr("Confidential pricing call"), Source: "manual",
		Links: []ActivityLinkInput{{EntityType: "person", EntityID: foreignPerson}},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible, _, err := e.store.LogActivity(admin, LogActivityInput{
		Kind: "note", Subject: strPtr("Team call"), Source: "manual",
		Links: []ActivityLinkInput{{EntityType: "person", EntityID: myPerson}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unlinked, _, err := e.store.LogActivity(admin, LogActivityInput{
		Kind: "note", Subject: strPtr("Workspace-wide note"), Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPermsWithActivity())

	// Get: the activity attached to another team's person answers 404.
	if _, err := e.store.GetActivity(rep, ids.UUID(secret.Id), false); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("foreign-linked activity → %v, want ErrNotFound", err)
	}
	if _, err := e.store.GetActivity(rep, ids.UUID(visible.Id), false); err != nil {
		t.Errorf("team-linked activity → %v, want success", err)
	}

	// List: the timeline never surfaces it, including via the entity filter.
	list, _, err := e.store.ListActivities(rep, ListActivitiesInput{})
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
	probed, _, err := e.store.ListActivities(rep, ListActivitiesInput{EntityType: &entityType, EntityID: &entityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(probed) != 0 {
		t.Errorf("entity-filter probe on a foreign person returned %d activities, want 0", len(probed))
	}
}

func TestDuplicate409DoesNotDiscloseOutOfScopeIDs(t *testing.T) {
	e := setupAuthz(t)
	admin := e.admin()
	if _, err := e.store.CreatePerson(admin, CreatePersonInput{
		FullName: "Owned elsewhere", OwnerID: &e.rep3, Source: "manual",
		Emails: []PersonEmailInput{{Email: "taken@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)
	_, err := e.store.CreatePerson(rep, CreatePersonInput{
		FullName: "Duplicate attempt", Source: "manual",
		Emails: []PersonEmailInput{{Email: "taken@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	})
	var dup *DuplicateEmailError
	if !errors.As(err, &dup) {
		t.Fatalf("duplicate create → %v, want DuplicateEmailError", err)
	}
	if !dup.ExistingID.IsZero() {
		t.Errorf("409 disclosed out-of-scope id %s", dup.ExistingID)
	}

	// The same conflict against a row the rep CAN see keeps the id — the
	// dedupe UX ("open the existing record") survives for legit cases.
	if _, err := e.store.CreatePerson(admin, CreatePersonInput{
		FullName: "Teammate's", OwnerID: &e.rep2, Source: "manual",
		Emails: []PersonEmailInput{{Email: "team@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = e.store.CreatePerson(rep, CreatePersonInput{
		FullName: "Duplicate attempt 2", Source: "manual",
		Emails: []PersonEmailInput{{Email: "team@example.com", EmailType: "work", IsPrimary: true, Position: 1}},
	})
	if !errors.As(err, &dup) {
		t.Fatalf("visible duplicate → %v, want DuplicateEmailError", err)
	}
	if dup.ExistingID.IsZero() {
		t.Error("409 for a visible duplicate should carry the existing id")
	}
}

// repPermsWithActivity extends the rep fixture with activity grants for
// the timeline tests.
func repPermsWithActivity() crmctx.Permissions {
	p := repPerms
	objects := make(map[string]crmctx.ObjectGrant, len(p.Objects)+1)
	for k, v := range p.Objects {
		objects[k] = v
	}
	objects["activity"] = crmctx.ObjectGrant{Create: true, Read: true, Update: true}
	p.Objects = objects
	return p
}

// lostStage resolves the seeded pipeline's lost stage.
func lostStage(t *testing.T, e *authzEnv) ids.UUID {
	t.Helper()
	p, err := e.store.DefaultPipeline(e.admin())
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
	e := setupAuthz(t)
	pipeline, open, won := dealFixture(t, e)
	admin := e.admin()

	d, err := e.store.CreateDeal(admin, CreateDealInput{
		Name: "Closed amountless", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AdvanceDeal(admin, ids.UUID(d.Id), AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("closing amountless: %v", err)
	}

	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(context.Background()) }()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		 VALUES ($1, 'USD', 'EUR', 0.9200000000, current_date)`, e.ws); err != nil {
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
	if _, err := e.store.UpdateDeal(admin, ids.UUID(d.Id), UpdateDealInput{AmountMinor: &amount, Currency: &eur}); err != nil {
		t.Fatalf("adding amount to a closed deal: %v", err)
	}
	rate, _ := readFx()
	if rate == nil {
		t.Fatal("closed deal gained an amount but no frozen FX — deal_closed_fx would have 500ed before the fix")
	}

	// Switching the closed deal's currency re-freezes for the NEW pair.
	usd := "USD"
	if _, err := e.store.UpdateDeal(admin, ids.UUID(d.Id), UpdateDealInput{Currency: &usd}); err != nil {
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
	e := setupAuthz(t)
	pipeline, _, won := dealFixture(t, e)

	_, err := e.store.CreateDeal(e.admin(), CreateDealInput{
		Name: "Born won", PipelineID: pipeline, StageID: won, Source: "manual",
	})
	var terminal *TerminalStageOnCreateError
	if !errors.As(err, &terminal) {
		t.Fatalf("create on won stage → %v, want TerminalStageOnCreateError", err)
	}
}

// A client that reopens a lost deal while (redundantly) re-sending the
// lost_reason must not produce a duplicate SET clause — the reopen wins
// and clears the field.
func TestReopeningWithARedundantLostReasonStillCleans(t *testing.T) {
	e := setupAuthz(t)
	pipeline, open, _ := dealFixture(t, e)
	lost := lostStage(t, e)
	admin := e.admin()

	d, err := e.store.CreateDeal(admin, CreateDealInput{
		Name: "Lost and found", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AdvanceDeal(admin, ids.UUID(d.Id), AdvanceDealInput{ToStageID: lost, LostReason: strPtr("price")}); err != nil {
		t.Fatalf("closing as lost: %v", err)
	}
	reopened, err := e.store.AdvanceDeal(admin, ids.UUID(d.Id), AdvanceDealInput{ToStageID: open, LostReason: strPtr("price")})
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
	e := setupAuthz(t)
	foreignPerson := e.seedPerson(t, "Foreign", &e.rep3)
	admin := e.admin()

	src, key := "gmail", "msg-123"
	if _, _, err := e.store.LogActivity(admin, LogActivityInput{
		Kind: "email", Subject: strPtr("Confidential thread"), Source: "connector",
		SourceSystem: &src, SourceID: &key,
		Links: []ActivityLinkInput{{EntityType: "person", EntityID: foreignPerson}},
	}); err != nil {
		t.Fatal(err)
	}
	leadSrc, leadKey := "apollo", "lead-9"
	if _, _, err := e.store.CreateLead(admin, CreateLeadInput{
		FullName: strPtr("Foreign lead"), OwnerID: &e.rep3, Source: "import",
		SourceSystem: &leadSrc, SourceID: &leadKey,
	}); err != nil {
		t.Fatal(err)
	}

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPermsWithCapture())

	if _, _, err := e.store.LogActivity(rep, LogActivityInput{
		Kind: "email", Source: "connector", SourceSystem: &src, SourceID: &key,
	}); !errors.Is(err, errs.ErrConflict) {
		t.Errorf("activity replay of a foreign source key → %v, want bare ErrConflict", err)
	}
	if _, _, err := e.store.CreateLead(rep, CreateLeadInput{
		FullName: strPtr("Replay attempt"), Source: "import",
		SourceSystem: &leadSrc, SourceID: &leadKey,
	}); !errors.Is(err, errs.ErrConflict) {
		t.Errorf("lead replay of a foreign source key → %v, want bare ErrConflict", err)
	}
}

// Link targets are validated under RLS + row scope before the insert: the
// FK alone runs as the table owner and would persist a guessed foreign or
// out-of-scope UUID as a link.
func TestActivityLinkTargetsMustBeVisible(t *testing.T) {
	e := setupAuthz(t)
	foreignPerson := e.seedPerson(t, "Foreign", &e.rep3)
	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPermsWithCapture())

	if _, _, err := e.store.LogActivity(rep, LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []ActivityLinkInput{{EntityType: "person", EntityID: foreignPerson}},
	}); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("link to an out-of-scope person → %v, want ErrNotFound", err)
	}
	if _, _, err := e.store.LogActivity(rep, LogActivityInput{
		Kind: "note", Source: "manual",
		Links: []ActivityLinkInput{{EntityType: "person", EntityID: ids.NewV7()}},
	}); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("link to a nonexistent person → %v, want ErrNotFound", err)
	}
}

// repPermsWithCapture extends the rep fixture with the capture-side
// grants (activity + lead) the replay tests need.
func repPermsWithCapture() crmctx.Permissions {
	p := repPermsWithActivity()
	objects := make(map[string]crmctx.ObjectGrant, len(p.Objects)+1)
	for k, v := range p.Objects {
		objects[k] = v
	}
	objects["lead"] = crmctx.ObjectGrant{Create: true, Read: true, Update: true}
	p.Objects = objects
	return p
}
