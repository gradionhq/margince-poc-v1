// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A document reading reports itself, end to end.
//
// Every reading here is BORN through activities.Store — StartExtractionReadQueued,
// BeginExtractionRead, ReleaseExtractionRead, FinishExtractionRead — and every
// envelope is READ BACK OUT of event_outbox rather than hand-built, so what the
// consumer receives is exactly what production staged. A hand-inserted
// ai_task_run row or a hand-built envelope would prove nothing about either
// half.
//
// The redis hop is not here and is not missing: compose may not import the
// redis client (.go-arch-lint.yml gives it no such dependency), so this
// dispatches through Consumer.HandleEvent — the exact call cmd/worker's
// runSubscriber makes once it has decoded an envelope off the bus — and the
// transport itself is proven by platform/events' own bus test.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// testWriter sends a consumer's log lines to the running test.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// testLogger sends a consumer's log lines into the running test, so a
// deterministic refusal — which the consumer acks away by design — shows up as
// a line rather than as a row that never appeared.
func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, nil))
}

// readingFixture is one real attachment on one real deal, plus the store that
// moves its reading and the consumer that projects what the moves announce.
type readingFixture struct {
	env      *Env
	ctx      context.Context
	store    *activities.Store
	handlers activities.Handlers
	consumer *aiactivity.Consumer
	deal     ids.UUID
	readID   ids.UUID
	// delivered is how far the fixture's subscriber has got. Without it drain
	// replays the WHOLE history on every call, and a replay of the original
	// human-attributed event papers over anything a later event got wrong —
	// which is how a test asserting attribution passes against a repair path
	// that files the row to nobody.
	delivered int
}

func newReadingFixture(t *testing.T) *readingFixture {
	t.Helper()
	e := Setup(t)
	ctx := e.Admin()
	handlers := activities.NewHandlers(e.DB()).WithUploadLimit(uploadCeiling).WithBlobstore(blobstore.NewMemory())
	pipeline, open, _ := DealFixture(t, e)
	deal := e.SeedDeal(t, "Reading Fixture Deal", pipeline, open, &e.Rep1)
	att := uploadDealAttachment(ctx, t, handlers, deal, "quote.pdf", []byte("quote bytes"))

	store := activities.NewStore(e.DB())
	read, _, err := store.StartExtractionReadQueued(ctx, ids.UUID(att.Id), "human:"+e.AdminUser.String(), nil)
	if err != nil {
		t.Fatalf("StartExtractionReadQueued: %v", err)
	}
	return &readingFixture{
		env:      e,
		ctx:      ctx,
		store:    store,
		handlers: handlers,
		deal:     deal,
		// The consumer logs INTO THE TEST, not into a discard. A deterministic
		// refusal is acked away by design, so a test whose logger swallows it
		// sees only a row that never appeared — which is a failure two steps
		// away from its cause.
		consumer: aiactivity.NewConsumer(aiactivity.NewStore(e.DB()), testLogger(t)),
		readID:   read.ID,
	}
}

// dbNow is the clock every one of these suites measures against.
//
// The database's, not the test host's: stale_after, finished_at and the
// reconcile window are all computed from timestamps the database stamped, so a
// cutoff taken from the host answers a different question by the size of the
// drift between them — and on a date boundary, a different question entirely.
func (f *readingFixture) dbNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	if err := f.env.Pool.QueryRow(context.Background(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("reading the database clock: %v", err)
	}
	return now
}

// drain hands the consumer every ai_task.state_changed this reading staged,
// oldest first — what a subscriber that is keeping up receives.
func (f *readingFixture) drain(t *testing.T) {
	t.Helper()
	var raws [][]byte
	err := f.env.DB().Tx(context.Background(), func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT envelope FROM event_outbox
			 WHERE envelope->>'type' = 'ai_task.state_changed'
			   AND envelope->'payload'->>'occurrence_key' = $1
			 ORDER BY seq
			 OFFSET $2`, f.readID.String(), f.delivered)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			raws = append(raws, raw)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("reading the staged envelopes: %v", err)
	}
	if len(raws) == 0 {
		t.Fatal("the reading staged no ai_task.state_changed at all — nothing downstream could ever learn it exists")
	}
	for _, raw := range raws {
		var env kevents.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decoding a staged envelope: %v", err)
		}
		if err := f.consumer.HandleEvent(context.Background(), env); err != nil {
			t.Fatalf("the projection refused envelope %s: %v", env.EventID, err)
		}
		f.delivered++
	}
}

// projected is the occurrence as the projection holds it.
type projectedOccurrence struct {
	Kind        string
	AITask      *string
	State       string
	Attempt     int
	ActorScope  string
	ActorUserID *ids.UUID
	StartedAt   *time.Time
	StaleAfter  *time.Time
	SubjectType *string
}

func (f *readingFixture) projection(t *testing.T) projectedOccurrence {
	t.Helper()
	var got projectedOccurrence
	err := f.env.Pool.QueryRow(context.Background(), `
		SELECT kind, ai_task, state, attempt, actor_scope, actor_user_id,
		       started_at, stale_after, subject_type
		  FROM ai_task_run WHERE source = $1 AND occurrence_key = $2`,
		"attachment_extraction", f.readID.String()).
		Scan(&got.Kind, &got.AITask, &got.State, &got.Attempt, &got.ActorScope,
			&got.ActorUserID, &got.StartedAt, &got.StaleAfter, &got.SubjectType)
	if err != nil {
		t.Fatalf("reading the projected occurrence: %v", err)
	}
	return got
}

// A reading a human asked for is that human's, and it is theirs from the
// moment it is queued — not from the moment a worker picks it up.
func TestAQueuedReadingIsProjectedAsThePersonsOwnLiveWork(t *testing.T) {
	f := newReadingFixture(t)
	f.drain(t)

	got := f.projection(t)
	if got.State != "queued" || got.Attempt != 1 {
		t.Fatalf("state/attempt = %s/%d, want queued/1", got.State, got.Attempt)
	}
	if got.ActorScope != "personal" || got.ActorUserID == nil || *got.ActorUserID != f.env.AdminUser {
		t.Fatalf("actor = %s/%v, want personal/%s — the human who asked owns the occurrence", got.ActorScope, got.ActorUserID, f.env.AdminUser)
	}
	if got.Kind != "document_extract" || got.AITask == nil || *got.AITask != "document_extract" {
		t.Fatalf("kind/ai_task = %s/%v, want document_extract/document_extract", got.Kind, got.AITask)
	}
	if got.StaleAfter == nil {
		t.Fatal("a queued occurrence carries no stale_after, so a queue nobody drains would render as live forever")
	}
	if got.SubjectType == nil || *got.SubjectType != "attachment" {
		t.Fatalf("subject_type = %v, want attachment", got.SubjectType)
	}
}

// A worker that hands the reading back does not leave the projection saying it
// is running. This is the whole reason the guard orders by attempt: the release
// moves the row BACKWARDS, and it has to be believed.
func TestAReleasedReadingIsProjectedBackToQueuedAtTheNextAttempt(t *testing.T) {
	f := newReadingFixture(t)
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if claim.StartedAt == nil {
		t.Fatal("a claimed reading carries no start time")
	}
	f.drain(t)
	if got := f.projection(t); got.State != "running" || got.Attempt != 1 {
		t.Fatalf("after the claim, state/attempt = %s/%d, want running/1", got.State, got.Attempt)
	}

	if err := f.store.ReleaseExtractionRead(f.ctx, f.readID, *claim.StartedAt); err != nil {
		t.Fatalf("ReleaseExtractionRead: %v", err)
	}
	f.drain(t)

	got := f.projection(t)
	if got.State != "queued" || got.Attempt != 2 {
		t.Fatalf("after the release, state/attempt = %s/%d, want queued/2 — a row frozen at running is a reading the UI says is working and nobody holds", got.State, got.Attempt)
	}
}

// A finished reading settles, and it stops carrying a lease: nothing about a
// closed occurrence can go stale.
func TestAFinishedReadingSettlesInTheProjection(t *testing.T) {
	f := newReadingFixture(t)
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if err := f.store.FinishExtractionRead(f.ctx, f.readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadDone, ClaimedAt: *claim.StartedAt,
		Detail: "the document states none of the four fields",
	}); err != nil {
		t.Fatalf("FinishExtractionRead: %v", err)
	}
	f.drain(t)

	got := f.projection(t)
	if got.State != "done" {
		t.Fatalf("state = %s, want done", got.State)
	}
	if got.StaleAfter != nil {
		t.Fatalf("a settled occurrence carries stale_after %v; it is not claiming to work, so it has nothing to go stale", *got.StaleAfter)
	}
}
