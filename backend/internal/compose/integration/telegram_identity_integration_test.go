// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The identity and lifecycle half of the Telegram acceptance suite
// (telegram-oa design §7, §10): what happens when two exact lanes disagree,
// when two first messages from one stranger arrive at once, and when an erased
// subject's account writes again. The fixture is in
// telegram_fixture_integration_test.go.
//
// All three are silent in production if they are wrong. A lane disagreement
// that merged would fuse two customers; a lost identity race would put one
// human on two records with the conversation on only one; and a resurrection
// after erasure is an Art. 17 breach that nothing errors on.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// adminStoreCtx binds the installation's real admin at the trust level the
// privacy and dedupe engines demand: the person.delete grant and an unbounded
// row scope. It is the same human the HTTP session authenticates, so a row this
// context writes is attributable to somebody who exists.
func (c *telegramEnv) adminStoreCtx(t *testing.T) context.Context {
	t.Helper()
	user, err := ids.Parse(c.admin)
	if err != nil {
		t.Fatalf("parsing the admin id: %v", err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), c.workspaceID(t))
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + c.admin, UserID: user,
		SeatType: principal.SeatFull, Permissions: AdminPerms,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// TestAC_TG_6_IdentityPhoneDisagreementIsAConflictNotAMerge is AC-TG-6: a
// Telegram identity and a phone number that resolve to DIFFERENT people
// produce a review-queue conflict, never a merge.
//
// It drives the two exported calls the ingress composition makes in this
// situation and nothing else — DedupePerson to route, EnqueueIdentityConflict
// to raise the review — and then reads the review back over the REAL queue
// endpoint, because a conflict a human never sees is indistinguishable from a
// silent merge from the outside.
//
// The routing assertion is what separates "conflict" from "refuse": the
// message still lands, deterministically, on the person the higher-precedence
// lane names. Both records must survive untouched.
func TestAC_TG_6_IdentityPhoneDisagreementIsAConflictNotAMerge(t *testing.T) {
	c := setupTelegramConnected(t)
	const account = "770601"
	phone := "+4915100000601"
	identity := connector.ChannelIdentity{Provider: "telegram", ChannelUserID: account, Username: "split"}

	channelPerson := c.seedPerson(t, "Channel Half", nil)
	phonePerson := c.seedPerson(t, "Phone Half", &phone)
	c.bindChannelIdentity(t, channelPerson, identity)

	// One candidate carrying both keys: the channel lane names one person, the
	// phone lane another.
	admin := c.adminStoreCtx(t)
	var routed ids.PersonID
	var conflict *people.LaneConflict
	if err := database.WithWorkspaceTx(admin, c.Pool, func(tx pgx.Tx) error {
		res, err := people.DedupePerson(admin, tx, people.PersonCandidate{
			FullName:          "Phone Half",
			Phones:            []string{phone},
			ChannelIdentities: []connector.ChannelIdentity{identity},
		})
		routed, conflict = res.PersonID, res.Conflict
		return err
	}); err != nil {
		t.Fatalf("resolving the conflicting candidate: %v", err)
	}

	if routed.String() != channelPerson {
		t.Fatalf("routed to %s, want the channel-lane person %s — an established binding outranks a shared phone, "+
			"and routing must stay deterministic while a conflict is open", routed, channelPerson)
	}
	if conflict == nil {
		t.Fatal("two exact lanes named different people and no conflict was reported — the disagreement would be resolved silently")
	}

	recorded, err := people.NewStore(c.Pool).EnqueueIdentityConflict(admin, *conflict,
		"telegram:"+fmt.Sprintf("%d", telegramBotID)+":"+account+":1", "connector:telegram")
	if err != nil {
		t.Fatalf("EnqueueIdentityConflict: %v", err)
	}
	if !recorded {
		t.Fatal("the first conflict on this pair recorded no review row")
	}

	c.assertConflictIsReviewable(t, channelPerson, phonePerson)
	c.assertNothingWasMerged(t, channelPerson, phonePerson, identity, phone)
}

// assertConflictIsReviewable reads the queue over the composed router: one open
// person candidate naming both halves, with evidence naming both lanes. A
// review row nobody can fetch is not a review.
func (c *telegramEnv) assertConflictIsReviewable(t *testing.T, channelPerson, phonePerson string) {
	t.Helper()
	var queue struct {
		Data []struct {
			LeftID   string `json:"left_id"`
			RightID  string `json:"right_id"`
			Status   string `json:"status"`
			Evidence []struct {
				LeftValue  *string `json:"left_value"`
				RightValue *string `json:"right_value"`
			} `json:"evidence"`
		} `json:"data"`
	}
	if status := c.Call(t, "GET", "/v1/dedupe/candidates?entity_type=person", nil, nil, &queue); status != http.StatusOK {
		t.Fatalf("GET /v1/dedupe/candidates → %d, want 200", status)
	}
	if len(queue.Data) != 1 {
		raw, err := json.Marshal(queue.Data)
		if err != nil {
			t.Fatalf("rendering the queue for the failure message: %v", err)
		}
		t.Fatalf("the review queue holds %d open person candidates, want exactly 1: %s", len(queue.Data), raw)
	}
	row := queue.Data[0]
	pair := map[string]bool{row.LeftID: true, row.RightID: true}
	if !pair[channelPerson] || !pair[phonePerson] {
		t.Fatalf("the candidate pairs {%s, %s}, want it to name {%s, %s}",
			row.LeftID, row.RightID, channelPerson, phonePerson)
	}
	if row.Status != "open" {
		t.Fatalf("candidate status = %q, want open — a conflict is resolved by a human, not by the ingest", row.Status)
	}
	lanes := map[string]bool{}
	for _, ev := range row.Evidence {
		if ev.LeftValue != nil {
			lanes[*ev.LeftValue] = true
		}
		if ev.RightValue != nil {
			lanes[*ev.RightValue] = true
		}
	}
	if !lanes["channel_identity"] || !lanes["phone"] {
		t.Fatalf("the review evidence %+v names neither both conflicting lanes; a reviewer cannot see WHY the two collided", row.Evidence)
	}
}

// assertNothingWasMerged is the "not a merge" half: both records are still
// live and distinct, each keeps its own key, and neither gained the other's.
// A merge would have archived one of them and relinked its satellites.
func (c *telegramEnv) assertNothingWasMerged(t *testing.T, channelPerson, phonePerson string, identity connector.ChannelIdentity, phone string) {
	t.Helper()
	for _, person := range []string{channelPerson, phonePerson} {
		if n := c.count(t,
			`SELECT count(*) FROM person WHERE id = $1 AND archived_at IS NULL AND merged_into_id IS NULL`,
			person); n != 1 {
			t.Fatalf("person %s was archived or merged away; a lane disagreement must not fuse two customers", person)
		}
	}
	if n := c.count(t, `
		SELECT count(*) FROM person_channel_identity
		 WHERE channel_user_id = $1 AND person_id = $2 AND archived_at IS NULL`,
		identity.ChannelUserID, channelPerson); n != 1 {
		t.Errorf("the channel identity no longer belongs to the person it was bound to")
	}
	if n := c.count(t, `SELECT count(*) FROM person_phone WHERE phone = $1 AND person_id = $2`,
		phone, phonePerson); n != 1 {
		t.Errorf("the phone no longer belongs to the person it was bound to")
	}
	// Nothing was written ONTO the rival either: the channel-lane person must
	// not have quietly acquired the phone that named someone else.
	if n := c.count(t, `SELECT count(*) FROM person_phone WHERE person_id = $1`, channelPerson); n != 0 {
		t.Errorf("the routed person gained the rival lane's phone; the conflict was half-merged")
	}
}

// seedPerson creates one person over the composed router, optionally with a
// phone. The HTTP surface is used rather than a direct insert so the fixture is
// a record the product itself would have produced.
func (c *telegramEnv) seedPerson(t *testing.T, name string, phone *string) string {
	t.Helper()
	body := apptest.AnyMap{"full_name": name}
	if phone != nil {
		body["phones"] = []apptest.AnyMap{{"phone": *phone, "phone_type": "mobile"}}
	}
	var created struct {
		ID string `json:"id"`
	}
	if status := c.Call(t, "POST", "/v1/people", body, nil, &created); status != http.StatusCreated {
		t.Fatalf("seeding person %q → %d", name, status)
	}
	return created.ID
}

// bindChannelIdentity binds one provider account to a person, as an inbound
// message would. Written as the table owner because what these tests are about
// is what the RESOLVE does with an existing binding, not how ingress puts it
// there (which AC-TG-3 already proves).
func (c *telegramEnv) bindChannelIdentity(t *testing.T, person string, identity connector.ChannelIdentity) {
	t.Helper()
	if err := inWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO person_channel_identity
			  (workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 'telegram', 'connector:telegram')`,
			c.ws, person, identity.Provider, identity.ChannelUserID, identity.Username)
		return err
	}); err != nil {
		t.Fatalf("binding the channel identity: %v", err)
	}
}

// channelEnsureForwarder is compose's channel resolver seam, forwarding to the
// people module. It is a 1:1 translation with no decision of its own — the
// composition's own adapter (compose/capture.go's peopleEnsurer) does exactly
// this, and that it is the one actually wired is asserted in package compose.
// It exists here because the concurrency test below needs to hold the Sink
// itself, in order to release two captures on one barrier.
type channelEnsureForwarder struct{ store *people.Store }

func (f channelEnsureForwarder) EnsureChannelCounterparty(ctx context.Context, in capture.EnsureChannelRequest) (capture.EnsureOutcome, error) {
	res, err := f.store.EnsureChannelCounterparty(ctx, people.EnsureChannelCounterpartyInput{
		Identity:    in.Identity,
		DisplayName: in.DisplayName,
		ActivityID:  ids.From[ids.ActivityKind](in.ActivityID),
		Source:      in.Source,
		CapturedBy:  in.CapturedBy,
	})
	if err != nil {
		return capture.EnsureOutcome{}, err
	}
	return capture.EnsureOutcome{PersonCreated: res.PersonCreated}, nil
}

// channelRecordFor builds the normalized record telegram.Normalize produces for
// one inbound message: an activity under its chat-scoped natural key, and the
// sender's channel identity as its counterparty.
func channelRecordFor(u telegramUpdate) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "telegram", SourceID: u.naturalKey()},
		Fields: capture.ActivityFields{
			Kind: "telegram", Body: u.text, Direction: connector.DirectionInbound,
			OccurredAt: time.Unix(telegramProviderDate, 0).UTC(),
		},
		Source:     "telegram:" + u.naturalKey(),
		CapturedBy: "connector:telegram",
		Counterparty: connector.Counterparty{
			Direction:   connector.DirectionInbound,
			DisplayName: u.firstName,
			ChannelIdentity: connector.ChannelIdentity{
				Provider: "telegram", ChannelUserID: u.account(), Username: u.username,
			},
		},
		ThreadKey: fmt.Sprintf("telegram:%d:%s", telegramBotID, u.account()),
	}
}

// channelConnectorCtx is the workspace-channel principal the ingest worker
// mints: a connector acting for NO human, permitted to create the activity it
// captures and the person that activity names, workspace-wide.
func (c *telegramEnv) channelConnectorCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), c.workspaceID(t))
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:telegram",
		Permissions: principal.Permissions{
			RoleKeys: []string{"channel"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true}, "person": {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// TestTwoConcurrentFirstMessagesYieldOnePersonAndTwoActivities is the identity
// race: two messages from ONE stranger, captured at the same instant, must
// converge on one Person while both messages stay on the timeline.
//
// Postgres is the synchronizer, not a sleep. Both goroutines are released on
// one barrier; the partial unique index over (provider, channel_user_id) makes
// the loser BLOCK on the winner's uncommitted bind, and the loser's own
// speculative person, its audit row and its outbox event are then withdrawn.
// The two activities never contend — their natural keys differ — which is
// exactly the asymmetry the test is asserting: the person converges, the
// conversation does not collapse.
func TestTwoConcurrentFirstMessagesYieldOnePersonAndTwoActivities(t *testing.T) {
	c := setupTelegramConnected(t)
	first := telegramUpdate{updateID: 5701, messageID: 71, senderID: 770701, username: "racer", firstName: "Nadia", text: "first"}
	second := telegramUpdate{updateID: 5702, messageID: 72, senderID: 770701, username: "racer", firstName: "Nadia", text: "second"}

	sink := capture.NewSink(c.Pool).
		WithChannelEnsurer(channelEnsureForwarder{store: people.NewStore(c.Pool)})
	ctx := c.channelConnectorCtx(t)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, u := range []telegramUpdate{first, second} {
		wg.Add(1)
		go func(slot int, update telegramUpdate) {
			defer wg.Done()
			<-start
			_, err := sink.Upsert(ctx, channelRecordFor(update))
			errs[slot] = err
		}(i, u)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent capture %d failed: %v", i+1, err)
		}
	}

	if n := c.channelIdentities(t, first); n != 1 {
		t.Fatalf("%d channel identities for one sender, want 1 — the account is one human across the installation", n)
	}
	if n := c.count(t, `SELECT count(*) FROM person WHERE archived_at IS NULL AND owner_id IS NULL`); n != 1 {
		t.Fatalf("%d ownerless people after two concurrent first messages, want 1 — "+
			"the loser's speculative person must leave no trace", n)
	}
	for _, u := range []telegramUpdate{first, second} {
		if n := c.telegramActivities(t, u); n != 1 {
			t.Fatalf("%d activities for message %s, want 1 — a lost identity race must not drop a message", n, u.naturalKey())
		}
	}

	// Both messages hang off the ONE surviving record: a conversation split
	// across two people is the failure this convergence exists to prevent.
	var links int
	var linkedPeople int
	if err := inWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT count(*), count(DISTINCT l.person_id)
			  FROM activity_link l JOIN activity a ON a.id = l.activity_id
			 WHERE a.source_system = 'telegram'`).Scan(&links, &linkedPeople)
	}); err != nil {
		t.Fatalf("reading the conversation's links: %v", err)
	}
	if links != 2 || linkedPeople != 1 {
		t.Fatalf("%d links across %d people, want both messages on the one counterparty", links, linkedPeople)
	}
}

// TestErasedSubjectsNextMessageIsAcceptedAndPersistsNothing holds Art. 17
// against the one path that can silently undo it: the erased subject's account
// writing again. Nothing errors when this is wrong — the person is quietly
// recreated, or their words are quietly re-stored, and the erasure certificate
// becomes a lie.
//
// The suppression list alone is not enough, and that is the whole point of this
// case. It stops the PERSON from being recreated, but the poll persists the
// verbatim update — numeric sender id, @username, first and last name, full
// message body — as the only copy of the message, and the Sink would then commit
// the body onto an activity too. No later erasure can reach any of it: both the
// raw purge and the suppression are driven off person_channel_identity rows,
// which the first erasure deleted and the suppression guarantees are never
// recreated, and raw_capture has no retention policy. So an erased subject
// would stay fully re-identifiable, indefinitely, from data written AFTER their
// erasure. Refusing to persist is the only moment this can be stopped.
//
// The second message is deliberately a NEW update, not a replay of the erased
// one: a replay would be absorbed by the raw layer's own dedupe and would prove
// nothing about the suppression list.
func TestErasedSubjectsNextMessageIsAcceptedAndPersistsNothing(t *testing.T) {
	c := setupTelegramConnected(t)
	before := telegramUpdate{updateID: 5801, messageID: 81, senderID: 770801, username: "forgetme", firstName: "Iris", text: "please delete me"}

	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{})
	startTelegramWorker(t, runner)
	c.arrive(t, sub, before)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())
	_, personID := c.capturedMessage(t, before)

	// The probe has to be honest before the erasure, or "suppressed" afterwards
	// measures nothing.
	if c.accountSuppressed(t, before.account()) {
		t.Fatal("a live account already reads as suppressed — the probe cannot detect an erasure")
	}

	person, err := ids.Parse(personID)
	if err != nil {
		t.Fatal(err)
	}
	if err := privacy.NewEraser(c.Pool).ErasePerson(c.adminStoreCtx(t), person, "acceptance-suite"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}
	if n := c.channelIdentities(t, before); n != 0 {
		t.Fatalf("%d channel identities survived the erasure, want 0", n)
	}
	if !c.accountSuppressed(t, before.account()) {
		t.Fatal("the erased account is not on the suppression list — their next message would recreate them")
	}

	jobsBeforeSecondMessage := c.ingestJobs(t)
	after := telegramUpdate{updateID: 5802, messageID: 82, senderID: 770801, username: "forgetme", firstName: "Iris", text: "hello again"}
	c.arrive(t, sub, after)

	// The batch was still ACKNOWLEDGED. Refusing to persist must not refuse to
	// advance the cursor: a cursor held back by an erased subject's messages would
	// re-fetch them on every poll forever and block every later customer's message
	// behind them.
	if cursor := c.pollCursor(t); cursor != after.updateID+1 {
		t.Errorf("the poll cursor is at %d after refusing update %d, want %d — a cursor wedged behind an erased subject blocks every later customer's message",
			cursor, after.updateID, after.updateID+1)
	}

	// Nothing was written, at any layer. raw_capture first, because it is the
	// verbatim payload and the one no later erasure could reach.
	if n := c.rawCaptures(t, after.updateID); n != 0 {
		t.Errorf("%d raw captures for an erased subject's message, want 0 — "+
			"their id, handle, name and message text were stored where no erasure can reach them", n)
	}
	if n := c.ingestJobs(t); n != jobsBeforeSecondMessage {
		t.Errorf("ingest jobs went from %d to %d — an update that must not be stored must not be queued for capture either",
			jobsBeforeSecondMessage, n)
	}
	if n := c.telegramActivities(t, after); n != 0 {
		t.Errorf("%d activities for an erased subject's message, want 0 — activity.body is their words", n)
	}
	if n := c.channelIdentities(t, after); n != 0 {
		t.Errorf("%d channel identities exist after the erased account wrote again, want 0 — the erasure was undone", n)
	}
	if !c.accountSuppressed(t, after.account()) {
		t.Error("the suppression entry did not survive the second message")
	}
}

// accountSuppressed asks the SAME probe the ingest path asks before it ensures
// anybody: is this provider account an erased subject's?
func (c *telegramEnv) accountSuppressed(t *testing.T, account string) bool {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), c.workspaceID(t))
	var answer bool
	if err := database.WithWorkspaceTx(ctx, c.Pool, func(tx pgx.Tx) error {
		var err error
		answer, err = storekit.ChannelIdentitySuppressed(ctx, tx, "telegram", account)
		return err
	}); err != nil {
		t.Fatalf("suppression probe: %v", err)
	}
	return answer
}
