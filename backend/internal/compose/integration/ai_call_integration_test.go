// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The AI tracing spine under real RLS (Layer 1 + Layer 3): a call writes
// one ai_call metadata row and — only when payload capture is on — one
// ai_call_payload content row in the same transaction, so the content can
// never outlive its metadata. The payload is the special-category-adjacent
// half: it ages out at 365d via the retention engine while the ai_call
// metadata row survives, and the Art. 17 erasure cascade purges any payload
// whose captured text mentions the erased subject's identifiers.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestCallMeterWritesTraceAndOptInPayload proves the two-row invariant: a
// call with a Payload writes ai_call + ai_call_payload with the columns
// round-tripping, and a call without a Payload writes ai_call only.
func TestCallMeterWritesTraceAndOptInPayload(t *testing.T) {
	e := Setup(t)
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	meter := ai.NewCallMeter(e.Pool)

	corr := ids.NewV7()
	// The stored payload is post-SecretStripper content: the request body
	// carries the message text, and it must round-trip verbatim — nothing
	// the caller never put there (a raw credential) may appear.
	request := json.RawMessage(`{"system":"be concise","messages":[{"role":"user","content":"summarize Q3"}]}`)
	response := json.RawMessage(`{"text":"Q3 was up 12%"}`)
	if err := meter.Record(ctx, ai.Call{
		CorrelationID:      &corr,
		Task:               ai.TaskSummarize,
		Tier:               ai.TierCheapCloud,
		Provider:           "anthropic",
		ModelID:            "claude-cheap",
		RequestFingerprint: "fp-1",
		TokensIn:           100,
		TokensOut:          40,
		Payload:            &ai.Payload{Request: request, Response: response},
	}); err != nil {
		t.Fatalf("record with payload: %v", err)
	}

	// One call row, one payload row, columns round-trip.
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call`); n != 1 {
		t.Fatalf("ai_call rows = %d, want 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call_payload`); n != 1 {
		t.Fatalf("ai_call_payload rows = %d, want 1", n)
	}
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var provider, modelID, storedCorr string
		var tokensIn, tokensOut int64
		if err := tx.QueryRow(context.Background(),
			`SELECT provider, model_id, correlation_id::text, tokens_in, tokens_out FROM ai_call`).
			Scan(&provider, &modelID, &storedCorr, &tokensIn, &tokensOut); err != nil {
			return err
		}
		if provider != "anthropic" || modelID != "claude-cheap" || storedCorr != corr.String() ||
			tokensIn != 100 || tokensOut != 40 {
			t.Errorf("ai_call columns wrong: provider=%q model=%q corr=%q in=%d out=%d",
				provider, modelID, storedCorr, tokensIn, tokensOut)
		}
		var reqText, respText string
		if err := tx.QueryRow(context.Background(),
			`SELECT request_payload::text, response_payload::text FROM ai_call_payload`).
			Scan(&reqText, &respText); err != nil {
			return err
		}
		if !strings.Contains(reqText, "summarize Q3") || !strings.Contains(respText, "Q3 was up") {
			t.Errorf("payload did not round-trip: req=%q resp=%q", reqText, respText)
		}
		// No secret the caller never planted leaked into the store.
		if strings.Contains(reqText, "sk-") || strings.Contains(respText, "sk-") {
			t.Errorf("payload carries an unplanted secret: req=%q resp=%q", reqText, respText)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A call WITHOUT a payload writes metadata only.
	if err := meter.Record(ctx, ai.Call{
		Task: ai.TaskSummarize, Tier: ai.TierPremium, Provider: "anthropic",
		ModelID: "claude-premium", RequestFingerprint: "fp-2", TokensIn: 10, TokensOut: 5,
	}); err != nil {
		t.Fatalf("record without payload: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call`); n != 2 {
		t.Fatalf("ai_call rows after payload-less call = %d, want 2", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call_payload`); n != 1 {
		t.Fatalf("ai_call_payload rows after payload-less call = %d, want 1 (no new payload)", n)
	}
}

// seedAgedPayload plants an ai_call plus its ai_call_payload dated
// daysBack days ago (occurred_at cannot go through CallMeter, which
// defaults to now()), returning the ai_call id.
func seedAgedPayload(t *testing.T, e *Env, daysBack int, requestJSON string) ids.UUID {
	t.Helper()
	callID := ids.NewV7()
	wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
	e.WsExec(t, `INSERT INTO ai_call (id, workspace_id, task, request_fingerprint, occurred_at)
		VALUES ($1, `+wsClause+`, 'summarize', 'fp-aged', now() - make_interval(days => $2))`,
		callID, daysBack)
	e.WsExec(t, `INSERT INTO ai_call_payload (workspace_id, ai_call_id, request_payload, response_payload, occurred_at)
		VALUES (`+wsClause+`, $1, $2::jsonb, '{}'::jsonb, now() - make_interval(days => $3))`,
		callID, requestJSON, daysBack)
	return callID
}

// TestAICallPayloadRetentionAgesOutContentKeepingMetadata drives the
// retention engine over an over-age payload: the special-category-adjacent
// content row is deleted, the ai_call metadata row survives.
func TestAICallPayloadRetentionAgesOutContentKeepingMetadata(t *testing.T) {
	e := Setup(t)
	callID := seedAgedPayload(t, e, 400, `{"messages":[{"role":"user","content":"old chatter"}]}`)

	// The bootstrap-seeded payload policy: 365-day erase of the content.
	e.WsExec(t, `INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'ai_call_payload', 'content', 365, 'erase')`)

	svc := privacy.NewRetentionService(e.Pool, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM ai_call_payload WHERE ai_call_id = $1`, callID); n != 0 {
		t.Fatalf("over-age payload not aged out: %d rows remain", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call WHERE id = $1`, callID); n != 1 {
		t.Fatalf("ai_call metadata row was destroyed by retention: %d rows, want 1", n)
	}
}

// TestAICallPayloadErasureCascadePurgesSubjectMentions proves the Art. 17
// cascade reaches captured payloads whose text mentions the erased subject.
func TestAICallPayloadErasureCascadePurgesSubjectMentions(t *testing.T) {
	e := Setup(t)
	personID := seedSubject(t, e) // plants the subject with subjectEmail

	// A captured payload whose request text names the subject's address, and
	// a control payload that never mentions them.
	mentions := seedAgedPayload(t, e, 1,
		`{"messages":[{"role":"user","content":"draft a reply to `+subjectEmail+`"}]}`)
	control := seedAgedPayload(t, e, 1,
		`{"messages":[{"role":"user","content":"unrelated request"}]}`)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), personID, "test"); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM ai_call_payload WHERE ai_call_id = $1`, mentions); n != 0 {
		t.Fatalf("payload mentioning the subject was not purged: %d rows remain", n)
	}
	// The subject was never mentioned here, so it stays — the retention
	// path (365d) is its guaranteed age-out, not the erasure cascade.
	if n := e.WsCount(t, `SELECT count(*) FROM ai_call_payload WHERE ai_call_id = $1`, control); n != 1 {
		t.Fatalf("control payload wrongly purged: %d rows, want 1", n)
	}
}
