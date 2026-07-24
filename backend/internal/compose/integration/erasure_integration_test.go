// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Right-to-erasure end-to-end: PII gone from the normalized rows, raw
// capture and embeddings; search returns nothing; the tombstone proves
// the erasure without re-storing PII; the suppression list makes
// re-capture skip the subject; legal hold blocks the whole path.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const subjectEmail = "selma.subject@example.test"

// seedSubject plants a person with an email, a linked activity, a raw
// capture payload mentioning them, and one embedding row.
func seedSubject(t *testing.T, e *Env) ids.UUID {
	t.Helper()
	personID := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, first_name, title, source, captured_by)
			 VALUES ($1, `+wsClause+`, 'Selma Subject', 'Selma', 'CFO', 'manual', 'human:x')`, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_email (workspace_id, person_id, email, source, captured_by)
			 VALUES (`+wsClause+`, $1, $2, 'manual', 'human:x')`, personID, subjectEmail); err != nil {
			return err
		}
		activityID := ids.NewV7()
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
			 VALUES ($1, `+wsClause+`, 'note', 'Met Selma', now(), 'manual', 'human:x')`, activityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			 VALUES (`+wsClause+`, $1, 'person', $2)`, activityID, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO raw_capture (workspace_id, source_system, source_id, payload)
			 VALUES (`+wsClause+`, 'gmail', 'msg-1', jsonb_build_object('from', $1::text, 'body', 'quarterly numbers'))`,
			subjectEmail); err != nil {
			return err
		}
		// The identity is a composite provider/model@dims stamp, not the
		// legacy fixed label — erasure must remove the row regardless of
		// which identity produced it (the DELETE at erasure.go carries no
		// model filter; a mixed-width store must still erase fully).
		vector := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 1023), ",") + ",0.1]"
		_, err := tx.Exec(ctx,
			`INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
			 VALUES (`+wsClause+`, 'person', $1, 0, 'h', 'gemini/gemini-embedding-001@1024', $2::vector)`, personID, vector)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return personID
}

// assertSubjectErased verifies every store the subject touched after an
// erasure: emails, embeddings, search, suppression entry, PII-free
// tombstone, scrubbed raw capture.
func assertSubjectErased(t *testing.T, e *Env, personID ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		checks := []struct {
			what  string
			query string
			want  int
		}{
			{"person_email rows", `SELECT count(*) FROM person_email WHERE person_id = $1`, 0},
			{"embeddings", `SELECT count(*) FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, 0},
			{"search hits for the name", `SELECT count(*) FROM person WHERE id = $1 AND search_tsv @@ plainto_tsquery('simple', 'Selma')`, 0},
			{"suppression entries", `SELECT count(*) FROM erasure_suppression WHERE kind = 'email'`, 1},
			{"erase tombstones", `SELECT count(*) FROM audit_log WHERE action = 'erase' AND entity_id = $1`, 1},
		}
		for _, c := range checks {
			var got int
			args := []any{}
			if strings.Contains(c.query, "$1") {
				args = append(args, personID)
			}
			if err := tx.QueryRow(ctx, c.query, args...).Scan(&got); err != nil {
				return fmt.Errorf("%s: %w", c.what, err)
			}
			if got != c.want {
				return fmt.Errorf("%s = %d, want %d", c.what, got, c.want)
			}
		}
		var name string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM person WHERE id = $1`, personID).Scan(&name); err != nil {
			return err
		}
		if name != "Erased Subject" {
			return fmt.Errorf("person name = %q", name)
		}
		var rawLeft int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM raw_capture WHERE payload::text ILIKE '%' || $1 || '%'`, subjectEmail).Scan(&rawLeft); err != nil {
			return err
		}
		if rawLeft != 0 {
			return fmt.Errorf("raw capture still mentions the subject (%d rows)", rawLeft)
		}
		// The tombstone certifies WITHOUT re-storing: no address, no name.
		var piiInTombstone bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM audit_log WHERE action = 'erase' AND entity_id = $1
			  AND (after::text ILIKE '%' || $2 || '%' OR after::text ILIKE '%Selma%'))`,
			personID, subjectEmail).Scan(&piiInTombstone); err != nil {
			return err
		}
		if piiInTombstone {
			return errors.New("the erasure tombstone re-stores PII")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestErasureRemovesPIIEverywhereAndSticksViaSuppression(t *testing.T) {
	e := Setup(t)
	personID := seedSubject(t, e)
	admin := e.Admin()

	// The SAR sees the full picture BEFORE erasure — Art. 15 assembly.
	pkg, err := privacy.AssembleSAR(admin, e.Pool, ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Subject["full_name"] != "Selma Subject" || len(pkg.Emails) != 1 ||
		len(pkg.Activities) != 1 || len(pkg.RawCapture) != 1 {
		t.Fatalf("SAR incomplete: subject=%v emails=%d activities=%d raw=%d",
			pkg.Subject["full_name"], len(pkg.Emails), len(pkg.Activities), len(pkg.RawCapture))
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(admin, personID, "test"); err != nil {
		t.Fatal(err)
	}

	assertSubjectErased(t, e, personID)

	// Re-capture of the erased address is skipped, not resurrected.
	sink := capture.NewSink(e.Pool)
	connCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	connCtx = principal.WithCorrelationID(connCtx, ids.NewV7())
	connCtx = principal.WithActor(connCtx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:test",
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"lead": {Create: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	_, err = sink.Upsert(connCtx, connector.NormalizedRecord{
		EntityType: "lead",
		NaturalKey: connector.NaturalKey{SourceSystem: "apollo", SourceID: "l-1"},
		Fields:     capture.LeadFields{FullName: "Selma Subject", Email: subjectEmail},
		Source:     "apollo:l-1",
		CapturedBy: "connector:test",
	})
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("re-capture of an erased subject → %v, want ErrSkip", err)
	}

	// A subject under legal hold cannot be erased.
	held := seedSubject(t, e)
	err = database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE person SET legal_hold = true WHERE id = $1`, held)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := privacy.NewEraser(e.Pool).ErasePerson(admin, held, "test"); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("erasing a held subject → %v, want ErrConflict", err)
	}
}

// TestErasurePreservesActivityUnderTransitiveHold pins F-011: a person is
// not itself held, but one of its subject-only activities is ALSO linked to
// an organization under legal_hold. Retention freezes such an activity
// transitively ("a hold on the subject must cover the evidence about them"),
// so the person-erase cascade must not destroy it either. A sibling
// subject-only activity with no hold IS redacted, proving the predicate
// discriminates rather than blanket-skipping.
func TestErasurePreservesActivityUnderTransitiveHold(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	personID := ids.NewV7()
	heldActivity := ids.NewV7()
	freeActivity := ids.NewV7()
	orgID := ids.NewV7()

	err := database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, first_name, source, captured_by)
			 VALUES ($1, `+ws+`, 'Held Subject', 'Held', 'manual', 'human:x')`, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO organization (id, workspace_id, display_name, legal_hold, source, captured_by)
			 VALUES ($1, `+ws+`, 'Counterparty GmbH', true, 'manual', 'human:x')`, orgID); err != nil {
			return err
		}
		// The held-evidence email: subject-only to the person, but also linked
		// to the org under legal_hold.
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
			 VALUES ($1, `+ws+`, 'email', 'Contract terms', 'The signed terms are attached.', now(), 'manual', 'human:x')`,
			heldActivity); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			 VALUES (`+ws+`, $1, 'person', $2)`, heldActivity, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
			 VALUES (`+ws+`, $1, 'organization', $2)`, heldActivity, orgID); err != nil {
			return err
		}
		// The sibling email: subject-only and NOT transitively held.
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
			 VALUES ($1, `+ws+`, 'email', 'Lunch?', 'Free on Friday?', now(), 'manual', 'human:x')`,
			freeActivity); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			 VALUES (`+ws+`, $1, 'person', $2)`, freeActivity, personID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(admin, personID, "test"); err != nil {
		t.Fatalf("erasing an unheld person → %v", err)
	}

	err = database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		// The person's own PII is still erased — the cascade ran.
		var name string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM person WHERE id = $1`, personID).Scan(&name); err != nil {
			return err
		}
		if name != "Erased Subject" {
			return fmt.Errorf("person not erased: full_name = %q", name)
		}
		// The transitively-held email is untouched: retention would freeze it,
		// so the erase cascade must too.
		var subject string
		var body *string
		if err := tx.QueryRow(ctx,
			`SELECT subject, body FROM activity WHERE id = $1`, heldActivity).Scan(&subject, &body); err != nil {
			return err
		}
		if subject != "Contract terms" || body == nil || *body != "The signed terms are attached." {
			return fmt.Errorf("held evidence was destroyed: subject=%q body=%v", subject, body)
		}
		// The sibling email carries no hold and IS redacted.
		var freeSubject string
		var freeBody *string
		if err := tx.QueryRow(ctx,
			`SELECT subject, body FROM activity WHERE id = $1`, freeActivity).Scan(&freeSubject, &freeBody); err != nil {
			return err
		}
		if freeSubject != "Erased Subject" || freeBody != nil {
			return fmt.Errorf("unheld subject-only email not redacted: subject=%q body=%v", freeSubject, freeBody)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
