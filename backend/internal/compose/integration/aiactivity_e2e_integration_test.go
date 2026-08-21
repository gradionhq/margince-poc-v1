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
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// readingFixture is one real attachment on one real deal, plus the store that
// moves its reading and the consumer that projects what the moves announce.
type readingFixture struct {
	env      *Env
	ctx      context.Context
	store    *activities.Store
	consumer *aiactivity.Consumer
	readID   ids.UUID
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
		env:   e,
		ctx:   ctx,
		store: store,
		consumer: aiactivity.NewConsumer(aiactivity.NewStore(e.DB()),
			slog.New(slog.NewTextHandler(io.Discard, nil))),
		readID: read.ID,
	}
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
			 ORDER BY seq`, f.readID.String())
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
	StaleAfter  *string
	SubjectType *string
}

func (f *readingFixture) projection(t *testing.T) projectedOccurrence {
	t.Helper()
	var got projectedOccurrence
	err := f.env.Pool.QueryRow(context.Background(), `
		SELECT kind, ai_task, state, attempt, actor_scope, actor_user_id,
		       stale_after::text, subject_type
		  FROM ai_task_run WHERE source = $1 AND occurrence_key = $2`,
		"attachment_extraction", f.readID.String()).
		Scan(&got.Kind, &got.AITask, &got.State, &got.Attempt, &got.ActorScope,
			&got.ActorUserID, &got.StaleAfter, &got.SubjectType)
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
		t.Fatalf("a settled occurrence carries stale_after %s; it is not claiming to work, so it has nothing to go stale", *got.StaleAfter)
	}
}
