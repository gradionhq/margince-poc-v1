// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// The store's core behaviour over a migrated Postgres: staging inside a
// caller-opened transaction, Load counting the attempt while it reads,
// RecordSent/Park/RecordFailure doing what their names say — RecordDeferral,
// the fourth status-guarded transition, is driven where the pacing policy
// reaches it (dispatcher_transmit_test.go) — the caller's transaction owning
// commit/rollback, and the (workspace_id, message_id) idempotency key. This
// file also carries the shared fixture
// (storeEnv/setupStore/actorCtx/stage/baseInput) the other
// store_*_integration_test.go files in this package ride:
// store_identity_integration_test.go (user_id is derived from the
// authenticated principal, never caller input),
// store_terminal_integration_test.go (a stale transition on an
// already-terminal row is a benign no-op, never a silent overwrite), and
// store_isolation_integration_test.go (RLS holds a delivery invisible and
// unmutable from any other workspace).

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// errTestRollback forces WithWorkspaceTx to roll back after a successful
// StageTx, so the rollback test can assert the row never committed.
var errTestRollback = errors.New("comms test: forced rollback")

// honouredIdentity is the reconciler every fixture-built store carries: the
// receipts driven from this file report no rewritten identity, so the seam is
// never reached. The cases that need it to do — or refuse — something build
// their own store (store_recordsent_integration_test.go).
type honouredIdentity struct{}

func (honouredIdentity) ReconcileMessageIdentityTx(context.Context, pgx.Tx, ids.ActivityID, string, string) error {
	return nil
}

type storeEnv struct {
	owner      *pgx.Conn
	store      *Store
	ctx        context.Context
	ws         ids.UUID
	user       ids.UserID
	activity   ids.ActivityID
	activity2  ids.ActivityID
	clockValue time.Time
}

func setupStore(t *testing.T) *storeEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &storeEnv{
		owner:      owner,
		ws:         ids.NewV7(),
		user:       ids.New[ids.UserKind](),
		activity:   ids.New[ids.ActivityKind](),
		activity2:  ids.New[ids.ActivityKind](),
		clockValue: time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC),
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		e.ws, "comms-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		e.user, e.ws, "rep-"+e.user.String()+"@comms.test"); err != nil {
		t.Fatal(err)
	}
	for _, act := range []ids.ActivityID{e.activity, e.activity2} {
		if _, err := owner.Exec(ctx,
			`INSERT INTO activity (id, workspace_id, kind, source, captured_by) VALUES ($1, $2, 'email', 'test', 'human:x')`,
			act, e.ws); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.store = NewStore(pool, func() time.Time { return e.clockValue }, honouredIdentity{})
	e.ctx = actorCtx(e.ws, e.user)
	return e
}

// actorCtx binds a workspace and an authenticated human actor — the shape
// StageTx requires to derive user_id, since sending is a human act with no
// caller-suppliable identity.
func actorCtx(ws ids.UUID, user ids.UserID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user.UUID,
	})
}

// stage runs StageTx in its own committed transaction — the shape every
// real caller uses (the activity and the delivery commit together).
func (e *storeEnv) stage(t *testing.T, in StageInput) ids.UUID {
	t.Helper()
	var id ids.UUID
	err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		var err error
		id, err = e.store.StageTx(e.ctx, tx, in)
		return err
	})
	if err != nil {
		t.Fatalf("staging a delivery: %v", err)
	}
	return id
}

func (e *storeEnv) baseInput(activity ids.ActivityID, messageID string) StageInput {
	return StageInput{
		ActivityID:      activity,
		Provider:        "gmail",
		MessageID:       messageID,
		Recipients:      []string{"buyer@example.com"},
		Cc:              []string{"cc@example.com"},
		Subject:         "Re: pricing",
		Body:            "As discussed.",
		ConsentPurpose:  "transactional",
		InReplyTo:       "parent@example.com",
		References:      []string{"root@example.com", "parent@example.com"},
		ThreadKey:       "thread-1",
		ListUnsubscribe: "<https://example.com/unsub>",
	}
}

// StageTx writes a row every field of Load reads back, and Load's first
// call counts the attempt that is about to be made (attempts: 0 → 1).
func TestStageThenLoadRoundTripsEveryFieldAndCountsTheAttempt(t *testing.T) {
	e := setupStore(t)
	in := e.baseInput(e.activity, "msg-roundtrip@example.com")
	id := e.stage(t, in)

	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != id || got.ActivityID != e.activity || got.UserID != e.user {
		t.Fatalf("identity fields = %+v", got)
	}
	if got.Provider != in.Provider || got.MessageID != in.MessageID || got.Subject != in.Subject || got.Body != in.Body {
		t.Fatalf("scalar fields = %+v, want %+v", got, in)
	}
	if got.ConsentPurpose != in.ConsentPurpose || got.InReplyTo != in.InReplyTo || got.ListUnsubscribe != in.ListUnsubscribe {
		t.Fatalf("consent/threading fields = %+v", got)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "buyer@example.com" {
		t.Fatalf("recipients = %v", got.Recipients)
	}
	if len(got.Cc) != 1 || got.Cc[0] != "cc@example.com" {
		t.Fatalf("cc = %v", got.Cc)
	}
	if len(got.References) != 2 || got.References[0] != "root@example.com" {
		t.Fatalf("references = %v", got.References)
	}
	if got.Status != StatusPending {
		t.Fatalf("status = %q, want pending (staging never transmits)", got.Status)
	}
	// The one Load call above made the first attempt.
	if got.Attempts != 1 {
		t.Fatalf("attempts after one Load = %d, want 1 (Load counts the attempt it is about to make)", got.Attempts)
	}
	// created_at comes from the INJECTED clock, not the database's now(): the
	// maximum-age bound that parks a permanently deferred delivery is arithmetic
	// on this column, and a test that let the server set it could only assert
	// that age by sleeping.
	if !got.CreatedAt.Equal(e.clockValue) {
		t.Fatalf("created_at = %s, want the injected clock's %s", got.CreatedAt, e.clockValue)
	}
}

// A delivery with no addressee at all can only be refused later — the consent
// gate asks about an empty list and answers no — so it is refused here, in the
// caller's own transaction, rather than staged as a row that will park.
func TestStagingADeliveryWithNoAddresseeIsRefused(t *testing.T) {
	e := setupStore(t)
	in := e.baseInput(e.activity, "msg-noaddressee@example.com")
	in.Recipients, in.Cc = nil, nil

	err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		_, err := e.store.StageTx(e.ctx, tx, in)
		return err
	})
	if !errors.Is(err, ErrNoAddressee) {
		t.Fatalf("staging a delivery addressed to nobody → %v, want ErrNoAddressee", err)
	}
}

// A nil Go slice marshals to JSON null, which is a legal jsonb value — the row
// would load and the dispatcher would decode null into a nil slice, indistinguishable
// from a list that was written empty. The columns are lists, and both the writer
// and the schema say so.
func TestStagedListColumnsAreArraysEvenWhenTheCallerPassedNone(t *testing.T) {
	e := setupStore(t)
	in := e.baseInput(e.activity, "msg-nolists@example.com")
	in.Cc, in.References = nil, nil
	id := e.stage(t, in)

	var ccType, refsType string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT jsonb_typeof(cc), jsonb_typeof(references_chain) FROM comms_outbound WHERE id = $1`,
		id).Scan(&ccType, &refsType); err != nil {
		t.Fatal(err)
	}
	if ccType != "array" || refsType != "array" {
		t.Fatalf("stored cc=%s references_chain=%s, want array/array", ccType, refsType)
	}
}

// …and the schema refuses the shape independently of the writer, so a second
// writer cannot reintroduce it.
func TestTheSchemaRefusesANonArrayInAListColumn(t *testing.T) {
	e := setupStore(t)
	_, err := e.owner.Exec(context.Background(), `
		INSERT INTO comms_outbound
		  (id, workspace_id, activity_id, user_id, provider, message_id,
		   recipients, cc, subject, body, consent_purpose, status)
		VALUES ($1, $2, $3, $4, 'gmail', 'msg-nullrecipients@example.com',
		        'null'::jsonb, '[]'::jsonb, 's', 'b', 'transactional', 'pending')`,
		ids.NewV7(), e.ws, e.activity, e.user)
	if err == nil {
		t.Fatal("a delivery with JSON null recipients was accepted; the shape constraint is not enforcing")
	}
}

// A second Load on the same still-pending delivery counts a second attempt
// — the redelivery-without-a-claim case this store accepts by design.
func TestLoadCountsEveryAttemptWhileStillPending(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-retry@example.com"))

	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts after two Loads = %d, want 2", got.Attempts)
	}
}

// Load on an id that was never staged in this workspace is terminal too:
// there is nothing pending to load, and the caller must stop rather than
// dereference a row that is not there.
func TestLoadOfAnUnstagedIDIsTerminal(t *testing.T) {
	e := setupStore(t)
	if _, err := e.store.Load(e.ctx, ids.NewV7()); err != ErrTerminal {
		t.Fatalf("Load of an unstaged id: got %v, want ErrTerminal", err)
	}
}

// RecordSent closes a pending delivery to 'sent' and stamps the provider
// receipt; a further Load then reports ErrTerminal — a redelivered job must
// stop rather than transmit twice.
func TestRecordSentClosesTheDeliveryAndLoadThenReportsTerminal(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-sent@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load before send: %v", err)
	}
	if err := e.store.RecordSent(e.ctx, id, connector.SendReceipt{ProviderMessageID: "provider-receipt-1"}); err != nil {
		t.Fatalf("RecordSent: %v", err)
	}

	var status string
	var providerMessageID *string
	var sentAt *time.Time
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status, provider_message_id, sent_at FROM comms_outbound WHERE id = $1`, id).
		Scan(&status, &providerMessageID, &sentAt); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if status != StatusSent {
		t.Fatalf("status = %q, want sent", status)
	}
	if providerMessageID == nil || *providerMessageID != "provider-receipt-1" {
		t.Fatalf("provider_message_id = %v, want provider-receipt-1", providerMessageID)
	}
	if sentAt == nil || !sentAt.Equal(e.clockValue) {
		t.Fatalf("sent_at = %v, want %v (the injected clock)", sentAt, e.clockValue)
	}

	if _, err := e.store.Load(e.ctx, id); err != ErrTerminal {
		t.Fatalf("Load on a sent delivery: got %v, want ErrTerminal", err)
	}
}

// Park ends a delivery no retry repairs and records why; a further Load
// then reports ErrTerminal for the same reason as a sent delivery.
func TestParkClosesTheDeliveryWithAReasonAndLoadThenReportsTerminal(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-parked@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load before park: %v", err)
	}
	if err := e.store.Park(e.ctx, id, "recipient permanently rejected"); err != nil {
		t.Fatalf("Park: %v", err)
	}

	var status string
	var reason *string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status, reason FROM comms_outbound WHERE id = $1`, id).Scan(&status, &reason); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if status != StatusParked {
		t.Fatalf("status = %q, want parked", status)
	}
	if reason == nil || *reason != "recipient permanently rejected" {
		t.Fatalf("reason = %v, want the parked explanation", reason)
	}

	if _, err := e.store.Load(e.ctx, id); err != ErrTerminal {
		t.Fatalf("Load on a parked delivery: got %v, want ErrTerminal", err)
	}
}

// RecordFailure notes a transient fault WITHOUT ending the delivery: status
// stays pending, so River's redelivery — and the next Load — still see it.
func TestRecordFailureLeavesTheDeliveryPendingForRetry(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-transient@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load before failure: %v", err)
	}
	if err := e.store.RecordFailure(e.ctx, id, "smtp 421: try again later"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	var status string
	var reason *string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status, reason FROM comms_outbound WHERE id = $1`, id).Scan(&status, &reason); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending — a transient failure must not end the delivery", status)
	}
	if reason == nil || *reason != "smtp 421: try again later" {
		t.Fatalf("reason = %v, want the transient explanation", reason)
	}

	// Still loadable: the redelivered job gets another attempt.
	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("Load after a transient failure: %v", err)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts after Load, RecordFailure, Load = %d, want 2", got.Attempts)
	}
}

// StageTx writes inside the CALLER's transaction: a rollback of that
// transaction must leave no delivery row behind, exactly as it leaves no
// activity row behind — the two commit or fail together.
func TestStageTxRollsBackWithItsCallerTransaction(t *testing.T) {
	e := setupStore(t)
	in := e.baseInput(e.activity, "msg-rollback@example.com")
	err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		if _, err := e.store.StageTx(e.ctx, tx, in); err != nil {
			return err
		}
		return errTestRollback
	})
	if err != errTestRollback {
		t.Fatalf("WithWorkspaceTx: got %v, want the forced rollback error", err)
	}

	var count int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM comms_outbound WHERE message_id = $1`, in.MessageID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows after a rolled-back stage = %d, want 0", count)
	}
}

// The (workspace_id, message_id) unique constraint is the idempotency key a
// re-ingested provider copy of the same message relies on: a second stage of
// the same message_id must fail, not silently duplicate.
//
// It fails in THIS system's words. The raw violation names the SQLSTATE, the
// constraint and the table; a caller is owed the fact it can act on and not the
// schema behind it — the same posture the no-actor guard takes a few lines
// above it in the store.
func TestStagingTheSameMessageIDTwiceConflicts(t *testing.T) {
	e := setupStore(t)
	in := e.baseInput(e.activity, "msg-dupe@example.com")
	e.stage(t, in)

	err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		_, err := e.store.StageTx(e.ctx, tx, e.baseInput(e.activity2, in.MessageID))
		return err
	})
	if !errors.Is(err, ErrDuplicateMessage) || !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("staging the same message_id twice → %v, want ErrDuplicateMessage (a conflict)", err)
	}
	for _, internal := range []string{"SQLSTATE", "constraint", "comms_outbound"} {
		if strings.Contains(err.Error(), internal) {
			t.Errorf("refusal %q leaks %q; a caller is owed the fact, not the schema", err, internal)
		}
	}
}
