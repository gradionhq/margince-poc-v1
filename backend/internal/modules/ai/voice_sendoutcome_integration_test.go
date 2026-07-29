// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package ai

// RecordSendOutcomeTx's refusals are SQL behaviour — a locked row, an
// erased row, a foreign owner, an already-decided outcome — so they are
// proven against a real Postgres rather than a hand-built fake
// transaction. Workspace isolation here rides RLS alone: this store adds
// no workspace_id predicate of its own, exactly like production.

import (
	"context"
	"crypto/sha256"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sendOutcomeClock is the store's injected now: updated_at is asserted
// against it, so the proof never reads the wall clock.
var sendOutcomeClock = time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC)

// draftRetentionUntil is the deadline RecordDraftedSignal stamps at draft
// time. Recording a send must leave it exactly where it was.
var draftRetentionUntil = sendOutcomeClock.Add(voiceLearningSignalRetention)

type sendOutcomeEnv struct {
	owner *pgx.Conn
	pool  *pgxpool.Pool
	store *VoiceStore
}

func setupSendOutcomeStore(t *testing.T) *sendOutcomeEnv {
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

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	store := NewVoiceStore(pool)
	store.now = func() time.Time { return sendOutcomeClock }
	return &sendOutcomeEnv{owner: owner, pool: pool, store: store}
}

// draftOptions varies the seeded signal for the refusal under test. The
// zero value is the ordinary case: a live drafted signal on a profile the
// acting human owns.
//
// The erasure stamp and the text it removes are separate switches, because
// real erasure sets both: seeding only one is how a case isolates which of the
// two gates a refusal actually came from.
type draftOptions struct {
	outcome            string // "" seeds the drafted state
	noGeneratedText    bool   // generated_original is NULL
	contentErased      bool   // content_erased_at is stamped
	archived           bool
	foreignOwner       bool // the profile belongs to another human
	agentActor         bool // the send is attributed to an agent, not the owner
	withoutVoiceUpdate bool // the actor's role grants no voice_profile update
}

// draftFixture is one seeded workspace with its user, profile, and the
// drafted learning signal a later send lands on.
type draftFixture struct {
	workspace ids.UUID
	profile   ids.UUID
	signal    ids.UUID
	draftRef  string
	actor     principal.Principal
	ctx       context.Context
}

func (e *sendOutcomeEnv) seedDraft(t *testing.T, opts draftOptions) draftFixture {
	t.Helper()
	ctx := context.Background()
	workspace := ids.NewV7()
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'VoiceSend', $2, 'EUR')`,
		workspace, "vs-"+workspace.String()); err != nil {
		t.Fatal(err)
	}

	sender := e.seedUser(t, workspace, "sender")
	profileOwner := sender
	if opts.foreignOwner {
		profileOwner = e.seedUser(t, workspace, "colleague")
	}

	var profile ids.UUID
	if err := e.owner.QueryRow(ctx, `
		INSERT INTO voice_profile (workspace_id, owner_id, scope, status, source, captured_by)
		VALUES ($1, $2, 'user', 'ready', 'ui', $3) RETURNING id`,
		workspace, profileOwner, "human:"+profileOwner.String()).Scan(&profile); err != nil {
		t.Fatal(err)
	}

	draftRef := "draft-" + ids.NewV7().String()
	hash := sha256.Sum256([]byte(draftRef))
	outcome := opts.outcome
	if outcome == "" {
		outcome = voiceOutcomeDrafted
	}
	generated := any(seededDraftBody)
	if opts.noGeneratedText {
		generated = nil
	}
	var erasedAt, archivedAt any
	if opts.contentErased {
		erasedAt = sendOutcomeClock
	}
	if opts.archived {
		archivedAt = sendOutcomeClock
	}
	var signal ids.UUID
	if err := e.owner.QueryRow(ctx, `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, draft_ref_hash, outcome, generated_original,
		   retention_until, content_erased_at, archived_at, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'draft', $9) RETURNING id`,
		workspace, profile, hash[:], outcome, generated, draftRetentionUntil,
		erasedAt, archivedAt, "human:"+profileOwner.String()).Scan(&signal); err != nil {
		t.Fatal(err)
	}

	actor := principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + sender.String(), UserID: sender,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, RowScope: principal.RowScopeTeam,
			Objects: map[string]principal.ObjectGrant{
				"voice_profile": {Read: true, Update: true},
			},
		},
	}
	if opts.withoutVoiceUpdate {
		actor.Permissions.Objects = map[string]principal.ObjectGrant{"voice_profile": {Read: true}}
	}
	if opts.agentActor {
		actor.Type = principal.PrincipalAgent
		actor.ID = "agent:sdr"
		actor.PassportID = ids.NewV7()
		actor.OnBehalfOf = sender
	}

	callCtx := principal.WithCorrelationID(
		principal.WithActor(principal.WithWorkspaceID(ctx, workspace), actor),
		ids.NewV7())
	return draftFixture{workspace: workspace, profile: profile, signal: signal, draftRef: draftRef, actor: actor, ctx: callCtx}
}

// seededDraftBody is what the voice drafter produced; a test sends it
// verbatim (accepted) or sends an edit of it (edited_sent).
const seededDraftBody = "Thanks for the call today — I will send the pricing over tomorrow."

// editedSendBody is seededDraftBody with two tokens changed, the shape of
// a rep who reworded before hitting send.
const editedSendBody = "Thanks for the chat today — I will send the numbers over tomorrow."

func (e *sendOutcomeEnv) seedUser(t *testing.T, workspace ids.UUID, label string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := e.owner.QueryRow(context.Background(), `
		INSERT INTO app_user (workspace_id, email, display_name)
		VALUES ($1, $2, $3) RETURNING id`,
		workspace, label+"-"+ids.NewV7().String()+"@example.test", label).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// record runs the method the way task 3's send path will: inside a
// workspace transaction the CALLER owns, never one the store opens.
func (e *sendOutcomeEnv) record(ctx context.Context, t *testing.T, draftRef, finalBody string) bool {
	t.Helper()
	var recorded bool
	if err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		recorded, err = e.store.RecordSendOutcomeTx(ctx, tx, draftRef, finalBody)
		return err
	}); err != nil {
		t.Fatalf("RecordSendOutcomeTx: %v", err)
	}
	return recorded
}

// signalRow is the stored signal as the retention evaluator and any future
// corpus promotion would read it.
type signalRow struct {
	outcome           string
	similarity        *float64
	finalText         *string
	finalCapturedBy   *string
	qualifiesAsSource bool
	retentionUntil    time.Time
	version           int64
	updatedAt         *time.Time
}

func (e *sendOutcomeEnv) readSignal(t *testing.T, id ids.UUID) signalRow {
	t.Helper()
	var row signalRow
	if err := e.owner.QueryRow(context.Background(), `
		SELECT outcome, similarity::double precision, final_text, final_captured_by,
		       qualifies_as_source, retention_until, version, updated_at
		FROM voice_learning_signal WHERE id = $1`, id).Scan(
		&row.outcome, &row.similarity, &row.finalText, &row.finalCapturedBy,
		&row.qualifiesAsSource, &row.retentionUntil, &row.version, &row.updatedAt); err != nil {
		t.Fatal(err)
	}
	return row
}

// assertUntouched is the shared refusal assertion: the row is exactly as
// seeded — still drafted, never versioned, no outcome fields written.
func (e *sendOutcomeEnv) assertUntouched(t *testing.T, f draftFixture) {
	t.Helper()
	row := e.readSignal(t, f.signal)
	if row.outcome != voiceOutcomeDrafted {
		t.Errorf("outcome = %q, want %q — the refused send decided the signal anyway", row.outcome, voiceOutcomeDrafted)
	}
	if row.version != 1 || row.updatedAt != nil {
		t.Errorf("version = %d, updated_at = %v — the refused send wrote the row", row.version, row.updatedAt)
	}
	if row.similarity != nil || row.finalCapturedBy != nil || row.finalText != nil {
		t.Errorf("outcome fields written by a refused send: %+v", row)
	}
	if e.countAudits(t, f.signal) != 0 {
		t.Error("a refused send wrote an audit_log row")
	}
}

func (e *sendOutcomeEnv) countAudits(t *testing.T, signal ids.UUID) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*)::int FROM audit_log
		WHERE entity_type = 'voice_learning_signal' AND entity_id = $1`, signal).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// emittedOutcome returns the outcome the staged voice.draft_outcome_recorded
// envelope published for this profile — the wire vocabulary a subscriber
// actually reads.
func (e *sendOutcomeEnv) emittedOutcome(t *testing.T, profile ids.UUID) string {
	t.Helper()
	var outcome string
	if err := e.owner.QueryRow(context.Background(), `
		SELECT envelope->'payload'->>'outcome' FROM event_outbox
		WHERE envelope->>'type' = 'voice.draft_outcome_recorded'
		  AND envelope->'entity'->>'id' = $1::text
		ORDER BY id DESC LIMIT 1`, profile.String()).Scan(&outcome); err != nil {
		t.Fatalf("no voice.draft_outcome_recorded staged for profile %s: %v", profile, err)
	}
	return outcome
}

// An unedited send is the owner accepting the machine's draft: the outcome
// is recorded with full similarity, the audit + outbox rows commit with
// it, and NO correspondence text is persisted — final_text stays NULL
// because this row carries no person linkage, so Art. 17 erasure can never
// reach it and only the 180-day sweep would.
func TestRecordSendOutcomeAcceptsAnUneditedSend(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{})

	if !env.record(f.ctx, t, f.draftRef, seededDraftBody) {
		t.Fatal("recorded = false, want true for a live drafted signal the sender owns")
	}

	row := env.readSignal(t, f.signal)
	if row.outcome != voiceOutcomeAccepted {
		t.Errorf("outcome = %q, want %q", row.outcome, voiceOutcomeAccepted)
	}
	if row.similarity == nil || *row.similarity != 1 {
		t.Errorf("similarity = %v, want 1", row.similarity)
	}
	if row.finalText != nil {
		t.Errorf("final_text = %q — the sent correspondence must never be persisted here; this row outlives Art. 17 erasure", *row.finalText)
	}
	if row.finalCapturedBy == nil || *row.finalCapturedBy != f.actor.ID {
		t.Errorf("final_captured_by = %v, want %q", row.finalCapturedBy, f.actor.ID)
	}
	if row.qualifiesAsSource {
		t.Error("qualifies_as_source = true — corpus promotion is a later decision, not a side effect of sending")
	}
	if !row.retentionUntil.Equal(draftRetentionUntil) {
		t.Errorf("retention_until = %s, want the draft-time deadline %s untouched", row.retentionUntil, draftRetentionUntil)
	}
	if row.version != 2 {
		t.Errorf("version = %d, want 2", row.version)
	}
	if row.updatedAt == nil || !row.updatedAt.Equal(sendOutcomeClock) {
		t.Errorf("updated_at = %v, want the injected clock %s", row.updatedAt, sendOutcomeClock)
	}
	if got := env.countAudits(t, f.signal); got != 1 {
		t.Errorf("audit_log rows = %d, want 1 (the write shape commits domain + audit + outbox together)", got)
	}
	if got := env.emittedOutcome(t, f.profile); got != "sent_unedited" {
		t.Errorf("published outcome = %q, want %q — the DDL spelling is not the wire spelling", got, "sent_unedited")
	}
}

// A reworded send is the owner's own text, not the machine's: the outcome
// is edited_sent with the pinned similarity, and the sent body is still
// discarded after classification.
func TestRecordSendOutcomeRecordsAnEditedSend(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{})

	if !env.record(f.ctx, t, f.draftRef, editedSendBody) {
		t.Fatal("recorded = false, want true")
	}

	row := env.readSignal(t, f.signal)
	if row.outcome != voiceOutcomeEditedSent {
		t.Errorf("outcome = %q, want %q", row.outcome, voiceOutcomeEditedSent)
	}
	_, wantSimilarity := classifyVoiceSendOutcome(seededDraftBody, editedSendBody)
	if row.similarity == nil || *row.similarity <= 0 || *row.similarity >= 1 {
		t.Fatalf("similarity = %v, want the pinned metric's value strictly inside (0,1)", row.similarity)
	}
	// numeric(5,4) rounds the ratio; the stored value must still be the
	// pinned metric to four decimals, not some other number.
	if got, want := *row.similarity, math.Round(wantSimilarity*1e4)/1e4; math.Abs(got-want) > 1e-9 {
		t.Errorf("similarity = %v, want %v (the pinned metric, rounded by numeric(5,4))", got, want)
	}
	if row.finalText != nil {
		t.Errorf("final_text = %q — the edited body is classified and discarded, never stored", *row.finalText)
	}
	if got := env.emittedOutcome(t, f.profile); got != "sent_edited" {
		t.Errorf("published outcome = %q, want %q", got, "sent_edited")
	}
}

// A draft reference nothing was ever drafted for is an ordinary send, not
// an error: absence is a value here, and the send must not fail over a
// learning concern.
func TestRecordSendOutcomeTreatsAnUnknownDraftReferenceAsNothingToRecord(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{})

	if env.record(f.ctx, t, "draft-"+ids.NewV7().String(), seededDraftBody) {
		t.Error("recorded = true for a reference with no signal row")
	}
	if env.record(f.ctx, t, "", seededDraftBody) {
		t.Error("recorded = true for an empty draft reference")
	}
	env.assertUntouched(t, f)
}

// The load-bearing refusal. Erasure NULLs the content in place and leaves
// the row drafted; without the content_erased_at gate the lookup would
// find it, compare the sent body against a NULL original, misclassify as
// edited_sent — and re-materialise a similarity over plaintext an erasure
// already removed.
//
// Erasure trips two gates at once, so each is proven on its own. A suite that
// only ever seeded the pair would stay green with either one deleted, and a
// GDPR gate that no test can fail is a gate nobody is holding.
func TestRecordSendOutcomeRefusesAnErasedOrArchivedSignal(t *testing.T) {
	env := setupSendOutcomeStore(t)

	t.Run("content erased by retention or Art. 17", func(t *testing.T) {
		f := env.seedDraft(t, draftOptions{contentErased: true, noGeneratedText: true})
		if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
			t.Error("recorded = true for a signal whose content was erased")
		}
		env.assertUntouched(t, f)
	})

	t.Run("the erasure stamp alone, with the served text still on the row", func(t *testing.T) {
		// Fabricated: no product path leaves the stamp without clearing the
		// text. That is the point — it is the only state in which the answer
		// can come from the content_erased_at predicate and nothing else, so
		// it is what holds that predicate in place.
		f := env.seedDraft(t, draftOptions{contentErased: true})
		if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
			t.Error("recorded = true for a signal stamped as erased")
		}
		env.assertUntouched(t, f)
	})

	t.Run("archived signal", func(t *testing.T) {
		f := env.seedDraft(t, draftOptions{archived: true})
		if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
			t.Error("recorded = true for an archived signal")
		}
		env.assertUntouched(t, f)
	})

	t.Run("live row with no generated text to compare against", func(t *testing.T) {
		f := env.seedDraft(t, draftOptions{noGeneratedText: true})
		if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
			t.Error("recorded = true with no original to classify against")
		}
		env.assertUntouched(t, f)
	})
}

// A signal on someone else's profile answers exactly like an absent one.
// Failing the send here would itself be an existence oracle: absent → sent,
// foreign → error is a distinguishable probe for another human's drafts.
func TestRecordSendOutcomeRefusesAForeignOwnersSignalLikeAnAbsentOne(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{foreignOwner: true})

	if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
		t.Error("recorded = true for a signal on another human's voice profile")
	}
	env.assertUntouched(t, f)
}

// The same indistinguishability, one layer down. Draft references are
// deterministic, so a colleague can compute one for a row they may not touch;
// answering "nothing to record" is not enough if the row was locked on the way
// to that answer. That lock waits for the owner's own in-flight send, and a
// wait is observable — it separates "absent" from "someone else's" exactly as
// loudly as an error would.
func TestRecordSendOutcomeNeverLocksAForeignOwnersSignal(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{foreignOwner: true})
	env.holdSignalLock(t, f.signal)

	recorded, err := env.recordUnderLockTimeout(f.ctx, t, f.draftRef, seededDraftBody)
	if err != nil {
		t.Fatalf("RecordSendOutcomeTx: %v — a foreign reference queued behind the owner's lock instead of reading as absent", err)
	}
	if recorded {
		t.Error("recorded = true for a signal on another human's voice profile")
	}
	env.assertUntouched(t, f)
}

// holdSignalLock takes a row lock on the signal from its own connection and
// holds it for the rest of the test, standing in for the owner's concurrent
// send. Plain reads are unaffected, so the assertions still see the row.
func (e *sendOutcomeEnv) holdSignalLock(t *testing.T, signal ids.UUID) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatalf("opening the lock holder's connection: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing the lock holder's connection: %v", err)
		}
	})
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the lock holder's transaction: %v", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(context.Background()); err != nil {
			t.Errorf("releasing the held lock: %v", err)
		}
	})
	var locked ids.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM voice_learning_signal WHERE id = $1 FOR UPDATE`, signal).Scan(&locked); err != nil {
		t.Fatalf("taking the lock on the signal: %v", err)
	}
}

// recordUnderLockTimeout runs the method with a bound on how long any lock it
// takes may block. A call that must take no lock never spends the bound, so
// the proof is a returned value rather than a test that waits on the clock; a
// call that does take one surfaces as an error instead of a hang.
func (e *sendOutcomeEnv) recordUnderLockTimeout(ctx context.Context, t *testing.T, draftRef, finalBody string) (bool, error) {
	t.Helper()
	var recorded bool
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			return err
		}
		var err error
		recorded, err = e.store.RecordSendOutcomeTx(ctx, tx, draftRef, finalBody)
		return err
	})
	return recorded, err
}

// The outcome is the OWNER's judgment (ADR-0066 §4): an agent's edit is
// not the owner's authored text, so a non-human principal records nothing.
func TestRecordSendOutcomeRefusesANonHumanPrincipal(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{agentActor: true})

	if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
		t.Error("recorded = true for an agent principal")
	}
	env.assertUntouched(t, f)
}

// The object grant still gates the write, but a missing grant never fails
// the human's send — it simply records no learning signal.
func TestRecordSendOutcomeRefusesAnActorWithoutTheVoiceProfileGrant(t *testing.T) {
	env := setupSendOutcomeStore(t)
	f := env.seedDraft(t, draftOptions{withoutVoiceUpdate: true})

	if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
		t.Error("recorded = true for an actor whose role grants no voice_profile update")
	}
	env.assertUntouched(t, f)
}

// A decided outcome is terminal. Two sends may legitimately carry the same
// reference — they are two emails — and the first transaction to take the
// row's lock owns the outcome; a redelivery never rewrites it.
func TestRecordSendOutcomeLeavesADecidedSignalAlone(t *testing.T) {
	env := setupSendOutcomeStore(t)

	t.Run("a redelivered send after the outcome was recorded", func(t *testing.T) {
		f := env.seedDraft(t, draftOptions{})
		if !env.record(f.ctx, t, f.draftRef, seededDraftBody) {
			t.Fatal("the first send recorded nothing")
		}
		if env.record(f.ctx, t, f.draftRef, editedSendBody) {
			t.Error("recorded = true on the second send — a redelivery overwrote a decided outcome")
		}
		row := env.readSignal(t, f.signal)
		if row.outcome != voiceOutcomeAccepted || row.version != 2 {
			t.Errorf("row = %+v, want the first send's accepted outcome at version 2", row)
		}
	})

	t.Run("a draft the owner already rejected", func(t *testing.T) {
		f := env.seedDraft(t, draftOptions{outcome: voiceOutcomeRejected})
		if env.record(f.ctx, t, f.draftRef, seededDraftBody) {
			t.Error("recorded = true for a rejected draft")
		}
		row := env.readSignal(t, f.signal)
		if row.outcome != voiceOutcomeRejected || row.version != 1 {
			t.Errorf("row = %+v, want the rejection untouched", row)
		}
	})
}

// This store adds no workspace_id predicate: the GUC transaction is the
// only gate, so another tenant's signal must read as absent even when the
// draft reference is known.
func TestRecordSendOutcomeCannotReachAnotherWorkspacesSignal(t *testing.T) {
	env := setupSendOutcomeStore(t)
	victim := env.seedDraft(t, draftOptions{})
	attacker := env.seedDraft(t, draftOptions{})

	if env.record(attacker.ctx, t, victim.draftRef, seededDraftBody) {
		t.Error("recorded = true across a workspace boundary")
	}
	env.assertUntouched(t, victim)

	// Sanity: the same reference in its OWN workspace does record, so the
	// refusal above is RLS working rather than a fixture that seeded nothing.
	if !env.record(victim.ctx, t, victim.draftRef, seededDraftBody) {
		t.Fatal("the owning workspace could not record its own signal")
	}
}
