// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// What the engine can DO to one over-age record: the executor table, the two
// questions the authoring surface asks of it, and the dispatch.
//
// Split from retention.go because the table is two things at once — the dispatch
// AND the authorable set — and both the nightly pass and the write path consult
// it. Keeping it beside the pass made it read as an implementation detail of the
// loop, which is exactly the reading that let scope and action be validated
// independently.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// retentionExecutor applies one action to one record inside the pass's audited
// transaction.
type retentionExecutor func(s *RetentionService, ctx context.Context, tx pgx.Tx, id ids.UUID) error

// retentionActions is every `object_type/action` pair the engine can perform —
// the dispatch table AND the authorable set, deliberately one map.
//
// Scope and action are chosen INDEPENDENTLY by whoever authors a policy, but
// only some of their combinations have an executor: there is no way to archive
// an ai_call_payload or to anonymize an activity. A write path that validated
// each half separately would admit `deal/won` + `erase`, and the pass would then
// abort on the first due record and stay red every night until somebody deleted
// the row — taking every LATER policy with it, because policies are ordered.
// Storage limitation would stop installation-wide, silently. So membership here
// is what ParseRetentionScope is for scopes: the one gate, consulted by the
// validator and by the pass.
//
// person/erase is registered with a NIL executor: it owns its own transaction
// (the Art. 17 cascade is ~30 statements plus object-store deletes), so apply
// dispatches it before opening one. Nil means "runs outside the transaction",
// never "unsupported" — membership is the key, not the value.
var retentionActions = map[string]retentionExecutor{
	"person/erase":          nil,
	"activity/archive":      (*RetentionService).archiveActivity,
	"activity/erase":        (*RetentionService).eraseActivityContent,
	"deal/archive":          (*RetentionService).archiveDeal,
	"ai_call_payload/erase": (*RetentionService).erasePayload,
	"lead/anonymize":        (*RetentionService).anonymizeLead,
	"person/anonymize":      (*RetentionService).anonymizePerson,
}

// The executors. Named methods rather than closures in the table above, because
// each runs a by-id UPDATE and updateguard_test.go walks named functions to
// assert every one of them either carries a concurrency guard or is a ratified
// exception — an anonymous function is invisible to it, and the retention sweep's
// deliberately unguarded absolute writes would stop being checked at all.

func (s *RetentionService) archiveActivity(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE activity SET archived_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	return s.invalidateGraph(ctx, tx, id)
}

func (*RetentionService) archiveDeal(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE deal SET archived_at = now() WHERE id = $1`, id)
	return err
}

// erasePayload deletes the row outright rather than scrubbing it in place —
// unlike activity/erase there is no metadata half of this record left to keep:
// ai_call_payload IS the special-category-adjacent content, and ai_call (the
// metadata row it FK-cascades from) survives untouched. The retention audit entry
// carries no payload bytes, only policy metadata.
func (*RetentionService) erasePayload(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM ai_call_payload WHERE id = $1`, id)
	return err
}

func (*RetentionService) anonymizeLead(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE lead SET full_name = 'Anonymized Lead', email = NULL, title = NULL,
		  company_name = NULL, candidate_org_key = NULL, raw = NULL,
		  archived_at = coalesce(archived_at, now())
		WHERE id = $1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`DELETE FROM embedding WHERE entity_type = 'lead' AND entity_id = $1`, id)
	return err
}

func (*RetentionService) anonymizePerson(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	return anonymizePersonRecord(ctx, tx, id)
}

// SupportsRetentionAction reports whether the engine can perform this action on
// this object type. The authoring surface refuses a pair it answers false for.
func SupportsRetentionAction(objectType, action string) bool {
	_, ok := retentionActions[objectType+"/"+action]
	return ok
}

// ActionsForScope is every action a given scope may be authored with, sorted, so
// a refusal can name the alternatives instead of leaving the caller to guess at
// a set the contract's two independent enums do not express.
func ActionsForScope(objectType string) []string {
	out := make([]string, 0, 3)
	for _, action := range []string{actionArchive, actionAnonymize, actionErase} {
		if SupportsRetentionAction(objectType, action) {
			out = append(out, action)
		}
	}
	return out
}

// apply runs ONE action on ONE record in one audited transaction.
func (s *RetentionService) apply(ctx context.Context, pol retentionPolicy, id ids.UUID) error {
	pair := pol.ObjectType + "/" + pol.Action
	executor, supported := retentionActions[pair]
	if !supported {
		// Unreachable through the authoring surface, which refuses an
		// unsupported pair, and through the pass, which skips the policy before
		// selecting a record. Kept because an unsupported pair must never be
		// mistaken for a completed action.
		return fmt.Errorf("retention: no executor for %s", pair)
	}
	if executor == nil {
		return s.eraser.ErasePerson(ctx, id, "retention")
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := executor(s, ctx, tx, id); err != nil {
			return err
		}
		// Retention audits under the verb of the action it ran —
		// archive, anonymize and erase are all in the closed audit
		// vocabulary (0053) — so a governance read can tell a retention
		// anonymize from a user edit, and the field-history projection
		// can treat anonymize/erase as its scrub boundary instead of
		// parsing payload shapes. The policy metadata rides the evidence
		// column, and before/after stay nil: this row records that a
		// policy acted, not a field diff, so a projectable verb like
		// archive must carry no payload the field-history diff could
		// mistake for record fields.
		auditID, err := storekit.AuditWithEvidence(ctx, tx, pol.Action, pol.ObjectType, id, nil, nil, map[string]any{
			evidenceKeyRetentionAction: pol.Action, "policy": pol.ID, "retain_days": pol.RetainDays,
		})
		if err != nil {
			return err
		}
		policyID := pol.ID
		return storekit.EmitEventForEntity(ctx, tx, auditID, pol.ObjectType, id, retentionAppliedPayload(pol.Action, &policyID, nil))
	})
}

// eraseActivityContent is the activity/erase action. Transcript free-text is
// the special-category risk; the record of the meeting stays, its content goes
// — including any attached recording/transcript file (objects first, so the
// purge shares the person-erase durability guarantee).
func (s *RetentionService) eraseActivityContent(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE activity SET body = NULL, subject = $2, archived_at = coalesce(archived_at, now()) WHERE id = $1`,
		id, erasedActivitySubject)
	if err == nil {
		_, err = tx.Exec(ctx,
			`DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = $1`, id)
	}
	if err == nil {
		err = s.invalidateGraph(ctx, tx, id)
	}
	if err == nil {
		err = s.eraser.eraseAttachments(ctx, tx, `entity_type = 'activity' AND entity_id = $1`, id)
	}
	if err == nil {
		// An outbound message ages out on the schedule of the activity
		// it belongs to: the send log holds the same recipients,
		// subject and body, and a policy that emptied one while the
		// other kept serving them would age out nothing.
		err = redactDeliveries(ctx, tx, []ids.UUID{id}, erasedActivitySubject)
	}
	return err
}

// anonymizePersonRecord is the person/anonymize action: the same in-place
// anonymization the eraser performs, minus the suppression list — the subject
// may lawfully return.
func anonymizePersonRecord(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	// The subject's addresses, read BEFORE person_email is deleted
	// below. The graph structures name them by raw address as well as
	// by person id — that is what the address arm of a participant row
	// IS — so a sweep that only matched person_id would leave the
	// address behind, still readable and still re-matchable. Same trap
	// the eraser hit with the subject's NAME, one column over.
	subjectEmails, err := collectStrings(ctx, tx,
		`SELECT lower(email) FROM person_email WHERE person_id = $1`, id)
	if err != nil {
		return err
	}
	// The NAME too, and before the anonymization below overwrites it —
	// the ghost sweep matches on it, and by then it is the tombstone.
	var subjectName string
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(full_name, '') FROM person WHERE id = $1`, id).Scan(&subjectName); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE person SET first_name = NULL, last_name = NULL, full_name = $2,
		  title = NULL, raw = NULL,
		  address_line1 = NULL, address_line2 = NULL, address_city = NULL,
		  address_region = NULL, address_postal_code = NULL, address_country = NULL,
		  archived_at = coalesce(archived_at, now())
		WHERE id = $1`, id, erasedName)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM person_social WHERE person_id = $1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM person_email WHERE person_id = $1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM person_phone WHERE person_id = $1`, id)
	}
	if err == nil {
		// The enrichment sidecar holds the subject's title and employer with
		// the verbatim sentence naming them. Anonymizing the person row above
		// cascades to nothing, so a sweep that skipped this would leave the
		// quote standing beside an "Erased Subject" record.
		_, err = tx.Exec(ctx, `DELETE FROM person_profile_field WHERE person_id = $1`, id)
	}
	if err == nil {
		// Purchased provider values, and the runs that bought them. Same
		// reasoning as the sidecar above and the same statements the erasure
		// path uses: anonymize-in-place cascades to nothing, so without these
		// the person page would show a bought email and employer beside an
		// "Erased Subject" name.
		_, err = tx.Exec(ctx, `DELETE FROM person_provider_claim WHERE person_id = $1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx,
			`UPDATE provider_run SET`+storekit.ScrubProviderRunColumns+` WHERE person_id = $1`, id)
	}
	if err == nil {
		// The channel identity is a resolution key on the subject as
		// much as their address: left behind, it would keep binding
		// inbound messages to the row this sweep just anonymized.
		_, err = tx.Exec(ctx,
			`DELETE FROM person_channel_identity WHERE person_id = $1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx,
			`DELETE FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, id)
	}
	if err == nil {
		err = scrubPersonGraphTraces(ctx, tx, id, subjectEmails, subjectName)
	}
	return err
}
