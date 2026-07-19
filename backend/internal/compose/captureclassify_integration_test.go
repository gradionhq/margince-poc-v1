// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The classify engine over a real Postgres: the backlog is the unlabeled
// partial index; one pass labels confident verdicts, re-asks the doubtful
// one solo, commits per call, and a budget stop ends the pass cleanly with
// everything already labeled kept. A noise label touches NOTHING but the
// two label columns (§3.2 — no mutation from a label).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// scriptedClassifyBrain answers each call from a script: batch calls get
// per-id verdicts, with confidence taken from the per-id map; calls after
// the script runs dry answer budget-exhausted.
type scriptedClassifyBrain struct {
	confidence   map[string]float64 // by activity id; default 0.9
	labels       map[string]string  // by activity id; default "noise"
	calls        int
	budgetOut    bool
	budgetOnSolo bool // the budget runs dry exactly on a solo re-ask
	soloStaysLow bool // the solo re-ask still cannot clear the floor
}

func (s *scriptedClassifyBrain) Complete(_ context.Context, req model.Request) (model.Response, error) {
	s.calls++
	if s.budgetOut {
		return model.Response{}, ai.ErrBudgetExhausted
	}
	idPattern := regexpMustIDs(req.Messages[0].Content)
	if s.budgetOnSolo && len(idPattern) == 1 {
		return model.Response{}, ai.ErrBudgetExhausted
	}
	results := make([]map[string]any, 0, len(idPattern))
	for _, id := range idPattern {
		label := s.labels[id]
		if label == "" {
			label = "noise"
		}
		conf, ok := s.confidence[id]
		if !ok {
			conf = 0.9
		}
		// A solo re-ask (single-id call) upgrades the doubtful verdict —
		// the scripted stand-in for the C-C fallback tier.
		if len(idPattern) == 1 && conf < classifyConfidenceFloor && !s.soloStaysLow {
			conf = 0.95
		}
		results = append(results, map[string]any{"id": id, "label": label, "confidence": conf})
	}
	payload, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Text: string(payload)}, nil
}

// regexpMustIDs pulls the source_id attributes out of the prompt.
func regexpMustIDs(prompt string) []string {
	var out []string
	rest := prompt
	for {
		i := indexAfter(rest, `source_id="`)
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := indexAfter(rest, `"`)
		if j < 0 {
			return out
		}
		out = append(out, rest[:j-1])
		rest = rest[j:]
	}
}

func indexAfter(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i + len(sub)
		}
	}
	return -1
}

func seedUnlabeledEmail(t *testing.T, e *integration.Env, subject string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity (id, workspace_id, kind, subject, body, source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'email', $3, 'body text', 'gmail', $4, 'gmail:'||$4, 'connector:gmail')`,
			id, e.WS, subject, fmt.Sprintf("cls-%s", id))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func labelOf(t *testing.T, e *integration.Env, id ids.UUID) *string {
	t.Helper()
	var label *string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT capture_label FROM activity WHERE id = $1`, id).Scan(&label)
	})
	if err != nil {
		t.Fatal(err)
	}
	return label
}

func TestCaptureClassifyPass(t *testing.T) {
	e := integration.Setup(t)

	confident := seedUnlabeledEmail(t, e, "please send the offer")
	doubtful := seedUnlabeledEmail(t, e, "hmm")
	brain := &scriptedClassifyBrain{
		labels:     map[string]string{confident.String(): "commitment"},
		confidence: map[string]float64{doubtful.String(): 0.4},
	}
	classifier := NewCaptureClassifier(e.Pool, brain, slog.New(slog.DiscardHandler))

	if err := classifier.Run(context.Background(), 0); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if l := labelOf(t, e, confident); l == nil || *l != "commitment" {
		t.Fatalf("confident label = %v, want commitment", l)
	}
	// The doubtful one was re-asked solo (its own call) and then committed.
	if l := labelOf(t, e, doubtful); l == nil || *l != "noise" {
		t.Fatalf("doubtful label = %v, want noise after the solo re-ask", l)
	}
	if brain.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (one batch + one solo re-ask)", brain.calls)
	}

	t.Run("an empty backlog costs zero model calls", func(t *testing.T) {
		before := brain.calls
		if err := classifier.Run(context.Background(), 0); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if brain.calls != before {
			t.Fatal("an empty backlog must not touch the model")
		}
	})

	t.Run("a budget stop ends the pass cleanly and keeps what landed", func(t *testing.T) {
		kept := seedUnlabeledEmail(t, e, "still here")
		brain.budgetOut = true
		if err := classifier.Run(context.Background(), 0); err != nil {
			t.Fatalf("a budget stop must not be an error: %v", err)
		}
		if l := labelOf(t, e, kept); l != nil {
			t.Fatal("a budget-stopped row must stay unlabeled for the next cycle")
		}
		if l := labelOf(t, e, confident); l == nil || *l != "commitment" {
			t.Fatal("already-committed labels must survive a later budget stop")
		}
	})

	t.Run("a batch that cannot clear the floor ends the pass, not the worker", func(t *testing.T) {
		// Every verdict — batch and solo — stays below the floor: nothing
		// commits, and Run must still terminate instead of refetching the
		// same rows forever.
		brain.budgetOut = false
		brain.soloStaysLow = true
		stuck := seedUnlabeledEmail(t, e, "???")
		brain.confidence[stuck.String()] = 0.3
		before := brain.calls
		if err := classifier.Run(context.Background(), 0); err != nil {
			t.Fatalf("a no-progress pass must not be an error: %v", err)
		}
		if l := labelOf(t, e, stuck); l != nil {
			t.Fatal("a below-floor row must stay unlabeled")
		}
		// One pass over the leftover backlog labels what it can, then the
		// stuck row's batch+solo repeat once and the loop breaks — a
		// bounded handful of calls, never an unbounded refetch spin.
		if brain.calls-before > 4 {
			t.Fatalf("model calls = %d for one no-progress pass — the loop is refetching", brain.calls-before)
		}
		brain.soloStaysLow = false
	})

	t.Run("a budget stop mid-run keeps the same pass's own commits", func(t *testing.T) {
		// The budget dies ON the solo re-ask, after the batch call already
		// succeeded — proving the per-call commit checkpoint: what the
		// batch labeled stays, only the doubtful row waits for next cycle.
		brain.budgetOut = false
		brain.budgetOnSolo = true
		sure := seedUnlabeledEmail(t, e, "confirming our agreement")
		shaky := seedUnlabeledEmail(t, e, "??")
		brain.labels[sure.String()] = "commitment"
		brain.confidence[shaky.String()] = 0.4
		if err := classifier.Run(context.Background(), 0); err != nil {
			t.Fatalf("a mid-run budget stop must not be an error: %v", err)
		}
		if l := labelOf(t, e, sure); l == nil || *l != "commitment" {
			t.Fatal("the batch's committed label must survive the solo re-ask's budget stop")
		}
		if l := labelOf(t, e, shaky); l != nil {
			t.Fatal("the budget-stopped doubtful row must stay unlabeled for the next cycle")
		}
	})
}
