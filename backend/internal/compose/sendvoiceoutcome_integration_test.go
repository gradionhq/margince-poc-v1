// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Whether the voice learning loop is WIRED, proven against a real database.
// This is the composition's own obligation and nothing else can carry it: the
// recorder answers every refusal with silence and a nil recorder is a
// deliberate no-op, so a send path composed without one behaves exactly like
// one composed with it — every module test still passes while the feature
// records nothing, forever. So these cases assert the row, never the absence
// of an error.
//
// Both stores the send path's file comment names are driven here. The tool
// surface's store is driven directly rather than through commsAdapter because
// agents.SendEmailArgs deliberately carries no draft reference (comms.go) —
// what the two transports share is the STORE, so that is where "both carry
// the recorder" is a statement about the wiring rather than about one
// transport's request shape.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// voiceSendBaseURL is the configured public origin these sends are
	// composed with; nothing here is a marketing send, so no link is minted
	// from it — it is present because a composed role always has one.
	voiceSendBaseURL = "https://mail.example.test"
	// voiceDraftBody is the text the model served. A send that carries it
	// verbatim is the owner accepting the draft.
	voiceDraftBody = "Thanks for the call — the pricing follows tomorrow."
)

// voiceSenderPerms is the scheduler fixture plus the voice_profile grant the
// learning write needs. It is a fresh Objects map rather than a copy of
// SchedulerPerms: the harness's fixture is shared by every suite in the
// process, and adding a grant to it in place would hand that grant to all of
// them.
var voiceSenderPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":        {Create: true, Read: true, Update: true},
		"activity":      {Create: true, Read: true, Update: true},
		"voice_profile": {Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

// voiceDraft is one seeded learning signal: the reference a send carries back
// and the row that reference resolves to.
type voiceDraft struct {
	profile ids.UUID
	signal  ids.UUID
	ref     string
}

// voiceSignalRow is the stored judgment as a later corpus decision reads it.
type voiceSignalRow struct {
	outcome    string
	similarity *float64
	version    int64
}

// voiceSendEnv is the fixture every case here starts from: a consented
// recipient, the anchor the send threads onto, and the acting human. The
// owner connection seeds and reads the signal table, which is RLS-forced and
// has no handler on this surface — the suite reads it as the retention
// evaluator would.
type voiceSendEnv struct {
	*integration.Env
	owner     *pgx.Conn
	profile   ids.UUID
	anchor    ids.UUID
	recipient string
	ctx       context.Context
}

// seedVoiceProfile gives the acting human the one live personal profile a
// served draft hangs off. One per user is a database constraint, so it
// belongs to the fixture rather than to a case.
func seedVoiceProfile(t *testing.T, owner *pgx.Conn, workspace, ownerUser ids.UUID) ids.UUID {
	t.Helper()
	var profile ids.UUID
	if err := owner.QueryRow(context.Background(), `
		INSERT INTO voice_profile (workspace_id, owner_id, scope, status, source, captured_by)
		VALUES ($1, $2, 'user', 'ready', 'ui', $3) RETURNING id`,
		workspace, ownerUser, "human:"+ownerUser.String()).Scan(&profile); err != nil {
		t.Fatalf("seeding the voice profile: %v", err)
	}
	return profile
}

// openDraft opens a drafted learning signal on the fixture's profile — the
// state RecordDraftedSignal leaves behind when a model serves a draft.
// profile_version stays NULL: these cases are about the send, and a built
// version would only add an FK to satisfy.
func (e *voiceSendEnv) openDraft(t *testing.T) voiceDraft {
	t.Helper()
	draft := voiceDraft{profile: e.profile, ref: "vd-" + ids.NewV7().String()}
	hash := sha256.Sum256([]byte(draft.ref))
	if err := e.owner.QueryRow(context.Background(), `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, draft_ref_hash, outcome, generated_original,
		   retention_until, source, captured_by)
		VALUES ($1, $2, $3, 'drafted', $4, $5, 'draft', $6) RETURNING id`,
		e.WS, draft.profile, hash[:], voiceDraftBody,
		time.Now().UTC().Add(180*24*time.Hour), "human:"+e.Rep1.String()).Scan(&draft.signal); err != nil {
		t.Fatalf("seeding the drafted learning signal: %v", err)
	}
	return draft
}

func (e *voiceSendEnv) readVoiceSignal(t *testing.T, signal ids.UUID) voiceSignalRow {
	t.Helper()
	var row voiceSignalRow
	if err := e.owner.QueryRow(context.Background(), `
		SELECT outcome, similarity::double precision, version
		FROM voice_learning_signal WHERE id = $1`, signal).Scan(
		&row.outcome, &row.similarity, &row.version); err != nil {
		t.Fatalf("reading the learning signal: %v", err)
	}
	return row
}

// assertAccepted is the whole point of this suite: the send resolved the
// signal. A nil recorder leaves the row exactly as seeded, so this is the
// assertion that goes red when the wiring is removed.
func (e *voiceSendEnv) assertAccepted(t *testing.T, signal ids.UUID, transport string) {
	t.Helper()
	row := e.readVoiceSignal(t, signal)
	if row.outcome != "accepted" || row.version != 2 {
		t.Fatalf("%s: signal = %+v, want an accepted outcome at version 2 — a send path composed without the recorder leaves it drafted at version 1", transport, row)
	}
	if row.similarity == nil || *row.similarity != 1 {
		t.Fatalf("%s: similarity = %v, want 1 for a draft sent verbatim", transport, row.similarity)
	}
}

func setupVoiceSend(t *testing.T) *voiceSendEnv {
	t.Helper()
	e := integration.Setup(t)

	const recipient = "reader@buyer.test"
	person := e.SeedPerson(t, "Draft Reader", &e.Rep1)
	addPersonEmail(t, e, person, recipient)
	admin := e.Admin()
	store := consent.NewStore(e.Pool)
	purpose, err := store.CreatePurpose(admin, "transactional", "Transactional", false)
	if err != nil {
		t.Fatalf("create purpose: %v", err)
	}
	if _, err := store.Record(admin, consent.RecordInput{
		PersonID: ids.From[ids.PersonKind](person), PurposeID: purpose.ID, NewState: "granted",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	owner := integration.OwnerConn(t)
	return &voiceSendEnv{
		Env: e, owner: owner, profile: seedVoiceProfile(t, owner, e.WS, e.Rep1),
		anchor: seedReplyAnchor(t, e), recipient: recipient,
		ctx: e.As(e.Rep1, []ids.UUID{e.Team1}, voiceSenderPerms),
	}
}

// draftedSend is the send input a rep submits after a model served them a
// draft: the reference travels with the body they approved.
func (e *voiceSendEnv) draftedSend(ref string) activities.SendEmailInput {
	return activities.SendEmailInput{
		Recipients: []string{e.recipient}, Subject: "Pricing",
		Body: voiceDraftBody, ConsentPurpose: "transactional", DraftRef: ref,
	}
}

// composedSendServer assembles the HTTP surface the way New does — the option
// loop, then the ONE projection — so what the case drives is the wiring a
// deployment gets rather than a store built by hand for the test.
func composedSendServer(t *testing.T, e *voiceSendEnv, stager DeliveryMachinery) Server {
	t.Helper()
	srv := newServer(e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)),
		identity.NewHandlers(identity.NewService(e.Pool)), deals.NewHandlers(e.Pool, InstallationBaseCurrency()))
	for _, opt := range []Option{WithPublicBaseURL(voiceSendBaseURL), WithDelivery(stager)} {
		opt(&srv, e.Pool)
	}
	srv.applySendPath(e.Pool)
	return srv
}

func TestBothSendTransportsCarryTheDraftOutcomeRecorder(t *testing.T) {
	e := setupVoiceSend(t)

	t.Run("the store the tool surface sends through", func(t *testing.T) {
		draft := e.openDraft(t)
		stager := &recordingStager{}
		store := sendStore(e.Pool, SendPath{PublicBaseURL: voiceSendBaseURL, Delivery: stager})

		if _, err := store.SendEmail(e.ctx, activities.FromActivity(ids.From[ids.ActivityKind](e.anchor)),
			e.draftedSend(draft.ref), consent.NewGate(consent.NewStore(e.Pool)), stager); err != nil {
			t.Fatalf("send through the tool surface's store: %v", err)
		}
		assertStaged(t, stager, 1, "the send through the tool surface's store")
		e.assertAccepted(t, draft.signal, "the tool surface's store")
	})

	t.Run("the store the HTTP handlers send through", func(t *testing.T) {
		draft := e.openDraft(t)
		stager := &recordingStager{}
		srv := composedSendServer(t, e, stager)

		body, err := json.Marshal(crmcontracts.SendEmailRequest{
			To: []openapi_types.Email{openapi_types.Email(e.recipient)}, Subject: "Pricing",
			Body: voiceDraftBody, ConsentPurpose: "transactional", DraftRef: &draft.ref,
		})
		if err != nil {
			t.Fatalf("marshaling the send request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost,
			"/v1/activities/"+e.anchor.String()+"/send-email", bytes.NewReader(body)).WithContext(e.ctx)
		rec := httptest.NewRecorder()
		srv.SendEmail(rec, req, crmcontracts.Id(e.anchor), crmcontracts.SendEmailParams{})

		if rec.Code != http.StatusAccepted {
			t.Fatalf("send-email → %d, want 202 (body %s)", rec.Code, rec.Body)
		}
		assertStaged(t, stager, 1, "the send through the HTTP handlers")
		e.assertAccepted(t, draft.signal, "the HTTP handlers' store")
	})
}

// gatedDraftOutcome wraps the REAL recorder so a case can hold the send's
// transaction open at the moment it owns the signal row's lock. Nothing else
// makes a second send genuinely contend for that lock: without the gate the
// two sends serialize by luck, and a suite that ran them one after the other
// would assume the terminal-outcome guard rather than demonstrate it.
type gatedDraftOutcome struct {
	inner activities.DraftOutcomeRecorder
	// locked carries the backend pid whose transaction now holds the row, so
	// the case can wait for a backend blocked by THAT one specifically.
	locked  chan int32
	release chan struct{}
}

func (g gatedDraftOutcome) RecordSendOutcomeTx(ctx context.Context, tx pgx.Tx, draftRef, finalBody string) (bool, error) {
	recorded, err := g.inner.RecordSendOutcomeTx(ctx, tx, draftRef, finalBody)
	if err != nil {
		return recorded, err
	}
	var pid int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		return recorded, err
	}
	g.locked <- pid
	<-g.release
	return recorded, nil
}

// witnessDraftOutcome reports what the real recorder answered the losing
// send. The answer is invisible to the send itself — every refusal is silent
// — so without this the case could only show that the row was not written
// twice, not that the loser was told there was nothing to record.
type witnessDraftOutcome struct {
	inner    activities.DraftOutcomeRecorder
	recorded bool
	err      error
}

func (w *witnessDraftOutcome) RecordSendOutcomeTx(ctx context.Context, tx pgx.Tx, draftRef, finalBody string) (bool, error) {
	w.recorded, w.err = w.inner.RecordSendOutcomeTx(ctx, tx, draftRef, finalBody)
	return w.recorded, w.err
}

// awaitLockHolder returns the backend the first send's transaction is holding
// the signal row on. Without a deadline a send path composed with NO recorder
// would park this case until the whole run times out, which reads as an
// infrastructure fault rather than as the missing wiring it is.
func awaitLockHolder(t *testing.T, locked <-chan int32) int32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case holder := <-locked:
		return holder
	case <-ctx.Done():
		t.Fatal("the first send never reached the draft-outcome recorder — this send path is composed without one, so there is no lock to race for")
		return 0
	}
}

// waitForBlockedOn returns once some backend is waiting on a lock the given
// backend holds. It polls rather than pauses: a sleep would be a guess about
// how long the second send takes to reach the lock, and the query's own round
// trip is the pacing. The deadline turns a wait that never resolves into a
// legible failure instead of a hung suite.
func waitForBlockedOn(t *testing.T, e *voiceSendEnv, holder int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		if ctx.Err() != nil {
			t.Fatal("no backend ever blocked on the first send's row lock — the second send did not contend for it, so this case proves nothing")
		}
		var blocked int
		if err := e.Pool.QueryRow(ctx,
			`SELECT count(*)::int FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`,
			holder).Scan(&blocked); err != nil {
			t.Fatalf("probing for a backend blocked by %d: %v", holder, err)
		}
		if blocked > 0 {
			return
		}
	}
}

// Two sends may legitimately carry one draft reference — they are two emails,
// and both must go out. The judgment is not two-valued though: the first
// transaction to take the row's lock owns the outcome, and the second finds
// the WHERE outcome = 'drafted' guard closed. This is the genuine race, with
// the loser blocked on the winner's open transaction, not two serial calls.
func TestConcurrentSendsSharingADraftReferenceLeaveOneOutcomeAndBothTransmit(t *testing.T) {
	e := setupVoiceSend(t)
	draft := e.openDraft(t)

	winner := gatedDraftOutcome{
		inner: ai.NewVoiceStore(e.Pool), locked: make(chan int32), release: make(chan struct{}),
	}
	loser := &witnessDraftOutcome{inner: ai.NewVoiceStore(e.Pool)}
	winnerStager, loserStager := &recordingStager{}, &recordingStager{}
	winnerStore := sendStore(e.Pool, SendPath{
		PublicBaseURL: voiceSendBaseURL, Delivery: winnerStager, DraftOutcome: winner,
	})
	loserStore := sendStore(e.Pool, SendPath{
		PublicBaseURL: voiceSendBaseURL, Delivery: loserStager, DraftOutcome: loser,
	})

	gate := consent.NewGate(consent.NewStore(e.Pool))
	anchor := ids.From[ids.ActivityKind](e.anchor)
	var wg sync.WaitGroup
	var winnerErr, loserErr error
	// The release runs on every exit path, so a failed assertion below ends
	// the test instead of parking the first send's goroutine forever.
	release := sync.OnceFunc(func() { close(winner.release) })
	defer release()

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, winnerErr = winnerStore.SendEmail(e.ctx, activities.FromActivity(anchor), e.draftedSend(draft.ref), gate, winnerStager)
	}()
	holder := awaitLockHolder(t, winner.locked)

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, loserErr = loserStore.SendEmail(e.ctx, activities.FromActivity(anchor), e.draftedSend(draft.ref), gate, loserStager)
	}()
	waitForBlockedOn(t, e, holder)

	release()
	wg.Wait()

	if winnerErr != nil || loserErr != nil {
		t.Fatalf("sends → %v / %v, want both to go out: a contended learning signal must never cost a message", winnerErr, loserErr)
	}
	assertStaged(t, winnerStager, 1, "the send that won the lock")
	assertStaged(t, loserStager, 1, "the send that waited for it")
	if loser.recorded || loser.err != nil {
		t.Fatalf("the waiting send recorded = %v (err %v), want it told there was nothing left to record", loser.recorded, loser.err)
	}
	e.assertAccepted(t, draft.signal, "the send that won the lock")
}
