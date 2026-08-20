// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Winning a deal turns into what its partner earned, through the real writer,
// the real outbox envelope, and the real consumer.
//
// The envelope is READ BACK OUT of event_outbox rather than hand-built, because
// the thing most likely to break this feature is the payload: the accrual is
// priced entirely from what deal.stage_changed carries, and a test that
// constructs its own envelope would keep passing after the emitter stopped
// putting the partner, the attribution or the frozen rate in it.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/commissions"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// commissionAdminPerms is the seat this suite acts as: the ledger and the
// records it is derived from, unbounded. The harness's own AdminPerms carries
// neither `partner` nor `commission`, and a suite that quietly ran without them
// would report a permission failure as a missing accrual.
var commissionAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"partner":      {Create: true, Read: true, Update: true, Delete: true},
		"commission":   {Create: true, Read: true, Update: true, Delete: true},
		"organization": {Create: true, Read: true, Update: true, Delete: true},
		"deal":         {Create: true, Read: true, Update: true, Delete: true},
		// Winning a deal freezes its FX rate, which reads the installation's
		// base currency.
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// accrualFixture is a partner on a known tier, a deal that names them, and the
// stages needed to win and reopen it.
type accrualFixture struct {
	deal    ids.DealID
	partner ids.OrganizationID
	won     ids.StageID
	open    ids.StageID
	ledger  *commissions.Store
	gen     *compose.CommissionGen
}

func seedAccrualFixture(t *testing.T, e *Env, tier string) accrualFixture {
	t.Helper()
	pipeline, open, won := DealFixture(t, e)
	admin := e.As(e.AdminUser, nil, commissionAdminPerms)

	partnerOrg := orgIDOf(e.SeedOrg(t, "Northgate Partners", nil))
	if _, err := e.People.UpsertPartner(admin, people.UpsertPartnerInput{
		OrganizationID: partnerOrg,
		PartnerRole:    "consulting",
		MarginTier:     &tier,
	}); err != nil {
		t.Fatalf("making the organization a partner on %s: %v", tier, err)
	}

	deal := ids.From[ids.DealKind](e.SeedDeal(t, "Northgate rollout", pipeline, open, &e.Rep1))
	amount := int64(100_000)
	currency := "EUR"
	if _, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{
		PartnerOrganizationID: &partnerOrg,
		AmountMinor:           &amount,
		Currency:              &currency,
	}); err != nil {
		t.Fatalf("pricing the deal and naming its partner: %v", err)
	}

	ledger := commissions.NewStore(e.DB())
	return accrualFixture{
		deal: deal, partner: partnerOrg, won: won, open: open, ledger: ledger,
		gen: compose.NewCommissionGen(e.Pool, ledger,
			people.NewStore(e.DB()), slog.New(slog.DiscardHandler)),
	}
}

// winAndDeliver wins the deal and hands the consumer the envelope the win
// actually emitted. It returns that envelope so a caller can deliver it twice.
func winAndDeliver(t *testing.T, e *Env, fx accrualFixture) kevents.Envelope {
	t.Helper()
	if _, err := e.Deals.AdvanceDeal(e.As(e.AdminUser, nil, commissionAdminPerms), fx.deal, deals.AdvanceDealInput{
		ToStageID: fx.won, WonWithoutContractReason: WonByImport(),
	}); err != nil {
		t.Fatalf("winning the deal: %v", err)
	}
	env := lastStageChanged(t, e, fx.deal)
	if err := fx.gen.HandleEvent(context.Background(), env); err != nil {
		t.Fatalf("the accrual consumer refused the win: %v", err)
	}
	return env
}

// lastStageChanged reads the most recent deal.stage_changed envelope this deal
// emitted, exactly as the relay would hand it to a subscriber.
func lastStageChanged(t *testing.T, e *Env, deal ids.DealID) kevents.Envelope {
	t.Helper()
	var raw []byte
	err := e.DB().Tx(context.Background(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT envelope FROM event_outbox
			  WHERE envelope->>'type' = 'deal.stage_changed'
			    AND envelope->'entity'->>'id' = $1::text
			  ORDER BY created_at DESC, id DESC LIMIT 1`, deal).Scan(&raw)
	})
	if err != nil {
		t.Fatalf("reading the stage-change envelope the win emitted: %v", err)
	}
	var env kevents.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decoding the envelope: %v", err)
	}
	return env
}

// liveEntries lists the entries a deal carries that have not been voided.
func liveEntries(t *testing.T, e *Env, fx accrualFixture) []int64 {
	t.Helper()
	page, err := fx.ledger.List(e.As(e.AdminUser, nil, commissionAdminPerms), commissions.ListInput{DealID: &fx.deal})
	if err != nil {
		t.Fatalf("listing the ledger: %v", err)
	}
	var amounts []int64
	for _, entry := range page.Data {
		if string(entry.Status) != commissions.StatusVoid {
			amounts = append(amounts, entry.AmountMinor)
		}
	}
	return amounts
}

func TestWinningAPartnerDealAccruesAtTheTierRate(t *testing.T) {
	e := Setup(t)
	fx := seedAccrualFixture(t, e, "tier2_20")

	winAndDeliver(t, e, fx)

	got := liveEntries(t, e, fx)
	if len(got) != 1 {
		t.Fatalf("live entries = %d, want exactly one accrual", len(got))
	}
	// 20% of 100_000 minor units, in integer arithmetic.
	if got[0] != 20_000 {
		t.Errorf("amount_minor = %d, want 20000 (tier2_20 of 100000)", got[0])
	}
}

// The replay guard is the stored trigger_event_id, not the Redis dedupe cache
// in front of it — so delivering the SAME envelope twice must not pay twice.
func TestARedeliveredWinDoesNotAccrueTwice(t *testing.T) {
	e := Setup(t)
	fx := seedAccrualFixture(t, e, "tier1_15")

	env := winAndDeliver(t, e, fx)
	if err := fx.gen.HandleEvent(context.Background(), env); err != nil {
		t.Fatalf("redelivering the same win: %v", err)
	}

	if got := liveEntries(t, e, fx); len(got) != 1 {
		t.Errorf("live entries = %d after the same event was delivered twice, want 1", len(got))
	}
}

// The case the one-live-entry index CANNOT catch: an old win's envelope is
// redelivered after its entry was voided. With no live entry in the way, the
// only thing standing between a partner and a second payment for the same win
// is the stored trigger_event_id.
func TestAWinRedeliveredAfterItsEntryWasVoidedDoesNotPayAgain(t *testing.T) {
	e := Setup(t)
	fx := seedAccrualFixture(t, e, "tier2_20")
	admin := e.As(e.AdminUser, nil, commissionAdminPerms)

	env := winAndDeliver(t, e, fx)
	if _, err := fx.ledger.ReverseForDeal(admin, fx.deal, "cancelled by hand"); err != nil {
		t.Fatalf("voiding the accrual: %v", err)
	}
	if got := liveEntries(t, e, fx); len(got) != 0 {
		t.Fatalf("live entries = %v after the void, want none", got)
	}

	if err := fx.gen.HandleEvent(context.Background(), env); err != nil {
		t.Fatalf("redelivering the win after the void: %v", err)
	}

	if got := liveEntries(t, e, fx); len(got) != 0 {
		t.Errorf("live entries = %v — the voided win accrued again, so the same deal is paid twice", got)
	}
}

func TestReopeningAWonDealReversesWhatItEarned(t *testing.T) {
	e := Setup(t)
	fx := seedAccrualFixture(t, e, "tier3_25")
	winAndDeliver(t, e, fx)

	if _, err := e.Deals.AdvanceDeal(e.As(e.AdminUser, nil, commissionAdminPerms), fx.deal, deals.AdvanceDealInput{ToStageID: fx.open}); err != nil {
		t.Fatalf("reopening the deal: %v", err)
	}
	if err := fx.gen.HandleEvent(context.Background(), lastStageChanged(t, e, fx.deal)); err != nil {
		t.Fatalf("the consumer refused the reopen: %v", err)
	}

	if got := liveEntries(t, e, fx); len(got) != 0 {
		t.Errorf("live entries = %v after the win was undone, want none — the money is reversed", got)
	}
	// The original is not deleted: a reader must still see what was earned and
	// that it was taken back.
	page, err := fx.ledger.List(e.As(e.AdminUser, nil, commissionAdminPerms), commissions.ListInput{DealID: &fx.deal})
	if err != nil {
		t.Fatalf("listing the ledger after the reversal: %v", err)
	}
	if len(page.Data) < 2 {
		t.Errorf("ledger holds %d rows, want the voided original AND its reversal", len(page.Data))
	}
}

// A partner with no tier has no arrangement, so there is nothing to pay. The
// win must still be recorded as won — a refusal here would wedge the consumer
// group on a deal that will never become accruable by itself.
func TestAWinForAPartnerWithNoTierAccruesNothingAndDoesNotFail(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	admin := e.As(e.AdminUser, nil, commissionAdminPerms)
	partnerOrg := orgIDOf(e.SeedOrg(t, "Untiered Partners", nil))
	if _, err := e.People.UpsertPartner(admin, people.UpsertPartnerInput{
		OrganizationID: partnerOrg, PartnerRole: "consulting",
	}); err != nil {
		t.Fatalf("making the organization a partner with no tier: %v", err)
	}
	deal := ids.From[ids.DealKind](e.SeedDeal(t, "Untiered rollout", pipeline, open, &e.Rep1))
	amount := int64(50_000)
	currency := "EUR"
	if _, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{
		PartnerOrganizationID: &partnerOrg, AmountMinor: &amount, Currency: &currency,
	}); err != nil {
		t.Fatalf("pricing the deal: %v", err)
	}
	ledger := commissions.NewStore(e.DB())
	gen := compose.NewCommissionGen(e.Pool, ledger, people.NewStore(e.DB()), slog.New(slog.DiscardHandler))

	if _, err := e.Deals.AdvanceDeal(admin, deal, deals.AdvanceDealInput{
		ToStageID: won, WonWithoutContractReason: WonByImport(),
	}); err != nil {
		t.Fatalf("winning the deal: %v", err)
	}
	if err := gen.HandleEvent(context.Background(), lastStageChanged(t, e, deal)); err != nil {
		t.Fatalf("a win that earns nothing must not fail the consumer: %v", err)
	}

	page, err := ledger.List(admin, commissions.ListInput{DealID: &deal})
	if err != nil {
		t.Fatalf("listing the ledger: %v", err)
	}
	if len(page.Data) != 0 {
		t.Errorf("ledger holds %d rows for a partner with no tier, want none", len(page.Data))
	}
}
