// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// The channel-shaped delivery over a migrated Postgres: staged with no RFC822
// identity, no subject and no address lists, and LOADED BACK — the load is the
// half that matters here. The mail scans read a NOT NULL string and a NOT NULL
// jsonb; a channel row supplies neither, so a schema change without a matching
// scan produces a delivery that stages perfectly and can never be dispatched.
//
// It rides the shared fixture in store_integration_test.go
// (storeEnv/setupStore/actorCtx).

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// telegramActivity inserts the timeline row a channel delivery reports on, so
// the fixture is the production shape rather than a mail activity borrowed for
// its foreign key.
func (e *storeEnv) telegramActivity(t *testing.T) ids.ActivityID {
	t.Helper()
	act := ids.New[ids.ActivityKind]()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, source, captured_by)
		 VALUES ($1, $2, 'telegram', 'connector:telegram', 'human:x')`,
		act, e.ws); err != nil {
		t.Fatal(err)
	}
	return act
}

func (e *storeEnv) stageChannel(t *testing.T, in StageChannelInput) ids.UUID {
	t.Helper()
	var id ids.UUID
	err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		var err error
		id, err = e.store.StageChannelTx(e.ctx, tx, in)
		return err
	})
	if err != nil {
		t.Fatalf("staging a channel delivery: %v", err)
	}
	return id
}

func TestStageAndLoadRoundTripAChannelDelivery(t *testing.T) {
	e := setupStore(t)
	activity := e.telegramActivity(t)
	in := StageChannelInput{
		ActivityID: activity,
		Provider:   "telegram",
		Recipient: connector.ChannelIdentity{
			Provider: "telegram", ChannelUserID: "778899", Username: "buyer",
		},
		Body:           "On its way today.",
		ConsentPurpose: "transactional",
		ReplyTo:        "4231",
	}
	id := e.stageChannel(t, in)

	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != id || got.ActivityID != activity || got.UserID != e.user {
		t.Fatalf("identity fields = %+v", got)
	}
	if !got.IsChannel() {
		t.Fatal("a staged channel delivery loaded back as mail-shaped; the dispatcher would render it as a mail message")
	}
	if got.ChannelRecipient() != in.Recipient.ChannelUserID {
		t.Errorf("recipient = %q, want %q", got.ChannelRecipient(), in.Recipient.ChannelUserID)
	}
	if got.Provider != in.Provider || got.Body != in.Body || got.ConsentPurpose != in.ConsentPurpose {
		t.Errorf("carried fields = %+v, want %+v", got, in)
	}
	if got.InReplyTo != in.ReplyTo {
		t.Errorf("reply anchor = %q, want %q", got.InReplyTo, in.ReplyTo)
	}
	// The mail half is ABSENT, not empty-but-present: nothing downstream may
	// render a subject line or a Message-ID a channel never had.
	if got.MessageID != "" || got.Subject != "" || got.ListUnsubscribe != "" {
		t.Errorf("mail fields survived on a channel delivery: %+v", got)
	}
	if len(got.Recipients) != 0 || len(got.Cc) != 0 || len(got.References) != 0 {
		t.Errorf("mail lists = %v/%v/%v, want all empty", got.Recipients, got.Cc, got.References)
	}
	// Load counts the attempt about to be made, on this shape too.
	if got.Attempts != 1 {
		t.Errorf("attempts after the first Load = %d, want 1", got.Attempts)
	}
	// And the same terminal transitions close it.
	if err := e.store.Park(e.ctx, id, "no route"); err != nil {
		t.Fatalf("parking a channel delivery: %v", err)
	}
	if _, err := e.store.Load(e.ctx, id); !errors.Is(err, ErrTerminal) {
		t.Fatalf("re-loading a parked channel delivery: %v, want ErrTerminal", err)
	}
}

// A channel delivery with nobody to reach is refused where the caller is still
// inside the transaction that would have written it — never staged to be refused
// later by a consent gate asked about nobody, which an operator reads as a
// customer who opted out.
func TestStagingAChannelDeliveryWithNoRecipientIsRefused(t *testing.T) {
	e := setupStore(t)
	activity := e.telegramActivity(t)
	err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		_, err := e.store.StageChannelTx(e.ctx, tx, StageChannelInput{
			ActivityID:     activity,
			Provider:       "telegram",
			Recipient:      connector.ChannelIdentity{Provider: "telegram"},
			Body:           "hello",
			ConsentPurpose: "transactional",
		})
		return err
	})
	if !errors.Is(err, ErrNoChannelRecipient) {
		t.Fatalf("staging with no recipient: %v, want ErrNoChannelRecipient", err)
	}
}

// The SCHEMA refuses a half-and-half row independently of the writer, so a
// second writer cannot reintroduce a delivery that is neither shape. The privacy
// scrub depends on this too: it splits into a mail arm and a channel arm because
// writing mail's empty address lists onto a channel row is refused here.
func TestTheSchemaRefusesADeliveryThatIsNeitherShape(t *testing.T) {
	e := setupStore(t)
	for _, tc := range []struct {
		name    string
		columns string
		values  string
	}{
		{
			"a channel recipient beside a subject",
			"channel_user_id, subject",
			"'778899', 'Re: pricing'",
		},
		{
			"a mail row with no addressees at all",
			"subject",
			"'Re: pricing'",
		},
		{
			"a channel row carrying a mail unsubscribe target",
			"channel_user_id, list_unsubscribe",
			"'778899', '<https://example.com/unsub>'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.owner.Exec(context.Background(), `
				INSERT INTO comms_outbound
				  (id, workspace_id, activity_id, user_id, provider, body, consent_purpose, status, `+tc.columns+`)
				VALUES ($1, $2, $3, $4, 'telegram', 'b', 'transactional', 'pending', `+tc.values+`)`,
				ids.NewV7(), e.ws, e.activity, e.user)
			if err == nil {
				t.Fatal("the row was accepted; comms_outbound_shape is not enforcing")
			}
		})
	}
}
