// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The signature-enrich pass over a real Postgres: an evidence-grounded
// title and phone land fill-only-empty with their PO-DDL-12 evidence rows;
// a fabricated snippet is dropped by the code-side gate; an occupied field
// is never touched; and a person once enriched leaves the candidate set.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// signatureScriptBrain answers every call with a fixed field set.
type signatureScriptBrain struct {
	fields []map[string]any
	calls  int
}

func (s *signatureScriptBrain) Complete(context.Context, model.Request) (model.Response, error) {
	s.calls++
	payload, err := json.Marshal(map[string]any{"fields": s.fields})
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Text: string(payload)}, nil
}

// seedEnrichPerson plants one connector-created person with a linked
// inbound email whose body carries the signature.
func seedEnrichPerson(t *testing.T, e *integration.Env, email, body string) ids.UUID {
	t.Helper()
	person := ids.NewV7()
	activity := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			VALUES ($1, $2, 'Bob Person', 'gmail:seed', 'connector:gmail')`, person, e.WS); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, source, captured_by)
			VALUES ($1, $2, $3, 'work', true, 'gmail:seed', 'connector:gmail')`, e.WS, person, email); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, body, direction, source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'email', 'hello', $3, 'inbound', 'gmail', $4, 'gmail:seed', 'connector:gmail')`,
			activity, e.WS, body, activity.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES ($1, $2, 'person', $3)`, e.WS, activity, person)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return person
}

func TestSignatureEnrichPass(t *testing.T) {
	e := integration.Setup(t)
	body := "Hi,\n\nsounds good.\n\nBest,\nBob Person\nCTO\n+49 30 1234567\nAcme GmbH"
	person := seedEnrichPerson(t, e, "bob@acme.example", body)

	brain := &signatureScriptBrain{fields: []map[string]any{
		{"field": "title", "value": "CTO", "evidence_snippet": "CTO", "confidence": 0.9},
		{"field": "phone", "value": "+49 30 1234567", "evidence_snippet": "+49 30 1234567", "confidence": 0.85},
		// Fabricated: the snippet is nowhere in the signature — the gate
		// must drop it in code, whatever the model claims.
		{"field": "linkedin", "value": "linkedin.com/in/bob", "evidence_snippet": "linkedin.com/in/bob", "confidence": 0.9},
	}}
	enricher := NewCaptureEnricher(e.Pool, brain, slog.New(slog.DiscardHandler))
	if err := enricher.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var title *string
	var phones, evidence, linkedinRows int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT title FROM person WHERE id = $1`, person).Scan(&title); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM person_phone WHERE person_id = $1`, person).Scan(&phones); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM person_profile_field WHERE person_id = $1`, person).Scan(&evidence); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM person_profile_field WHERE person_id = $1 AND field = 'linkedin'`, person).Scan(&linkedinRows)
	})
	if err != nil {
		t.Fatal(err)
	}
	if title == nil || *title != "CTO" {
		t.Fatalf("title = %v, want the evidence-grounded CTO", title)
	}
	if phones != 1 {
		t.Fatalf("%d phone rows, want the one signature phone", phones)
	}
	if evidence != 2 {
		t.Fatalf("%d evidence rows, want 2 (title + phone; the fabricated linkedin dropped)", evidence)
	}
	if linkedinRows != 0 {
		t.Fatal("a fabricated snippet must never produce an evidence row")
	}

	t.Run("an enriched person leaves the candidate set", func(t *testing.T) {
		before := brain.calls
		if err := enricher.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if brain.calls != before {
			t.Fatal("an already-enriched person must not be re-asked")
		}
	})

	t.Run("an occupied title is never touched", func(t *testing.T) {
		occupied := seedEnrichPerson(t, e, "carol@acme.example",
			"Cheers,\nCarol\nVP Sales\n+49 30 7654321")
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(),
				`UPDATE person SET title = 'Handwritten Title' WHERE id = $1`, occupied)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		// Occupied title exits the candidate set entirely (title IS NULL is
		// part of the candidacy predicate) — the pass reads nothing for her.
		before := brain.calls
		if err := enricher.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if brain.calls != before {
			t.Fatal("a person with a human-set title must not be a candidate")
		}
		var title string
		err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT title FROM person WHERE id = $1`, occupied).Scan(&title)
		})
		if err != nil {
			t.Fatal(err)
		}
		if title != "Handwritten Title" {
			t.Fatalf("title = %q — the human's answer was touched", title)
		}
	})
}

// faultyEnrichBrain answers every call with a fixed error or garbage —
// the model failure modes the pass must absorb without losing the fleet.
type faultyEnrichBrain struct {
	err     error
	garbage bool
	calls   int
}

func (f *faultyEnrichBrain) Complete(context.Context, model.Request) (model.Response, error) {
	f.calls++
	if f.err != nil {
		return model.Response{}, f.err
	}
	if f.garbage {
		return model.Response{Text: "not json at all {{{"}, nil
	}
	return model.Response{Text: "{}"}, nil
}

func TestSignatureEnrichAbsorbsModelFailures(t *testing.T) {
	e := integration.Setup(t)
	seedEnrichPerson(t, e, "flaky@acme.example", "Thanks,\nFlaky Person\nCOO\n+49 30 1111111")

	t.Run("garbage output fails the candidate, not the pass", func(t *testing.T) {
		brain := &faultyEnrichBrain{garbage: true}
		enricher := NewCaptureEnricher(e.Pool, brain, slog.New(slog.DiscardHandler))
		if err := enricher.Run(context.Background()); err != nil {
			t.Fatalf("a per-candidate model failure must not fail the pass: %v", err)
		}
		if brain.calls == 0 {
			t.Fatal("the candidate was never asked")
		}
		// Nothing landed: no evidence row for a verdict nobody could parse.
		if n := enrichEvidenceCount(t, e, "flaky@acme.example"); n != 0 {
			t.Fatalf("%d evidence rows from a garbage verdict, want 0", n)
		}
	})

	t.Run("a budget stop ends the pass cleanly", func(t *testing.T) {
		brain := &faultyEnrichBrain{err: ai.ErrBudgetExhausted}
		enricher := NewCaptureEnricher(e.Pool, brain, slog.New(slog.DiscardHandler))
		if err := enricher.Run(context.Background()); err != nil {
			t.Fatalf("a budget stop must not be an error: %v", err)
		}
		if brain.calls != 1 {
			t.Fatalf("model calls = %d, want 1 — the stop must end the pass, not walk the fleet", brain.calls)
		}
	})
}

// enrichEvidenceCount counts the person's evidence rows by primary email.
func enrichEvidenceCount(t *testing.T, e *integration.Env, email string) int {
	t.Helper()
	var n int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT count(*) FROM person_profile_field f
			JOIN person_email pe ON pe.person_id = f.person_id
			WHERE pe.email = $1`, email).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
