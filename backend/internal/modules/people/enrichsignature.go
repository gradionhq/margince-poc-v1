// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The signature-enrich apply half (ADR-0063, §2.9): the model's gated
// fields land here — fill-ONLY-empty (a human-set value is never touched;
// GATE-AI-4), every accepted field upserts its PO-DDL-12 evidence row so
// the value stays auditable back to the verbatim signature line, and the
// field-provenance stamp rides the same commit. org_name is evidence-only:
// it NEVER creates or renames an organization (the deterministic domain
// path owns employer derivation).

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// enrichSource is the DM-CONV-11 channel for signature-extracted fields.
const enrichSource = "capture_enrich"

// enrichCapturedBy is the acting identity on enrichment rows.
const enrichCapturedBy = "agent:enrich"

// SignatureField is one gated, evidence-carrying extraction.
type SignatureField struct {
	Name       string // title | phone | role | linkedin | org_name
	Value      string
	Evidence   string // verbatim snippet — the caller's gate already verified it
	Confidence float64
}

// SignatureApplyResult counts what one apply did — honest numbers for the
// digest, not fabrications.
type SignatureApplyResult struct {
	Applied int // evidence rows written (first verdict per field wins)
	Skipped int // fields already answered (occupied column or existing row)
}

// ApplySignatureFields lands one person's gated signature fields in one
// transaction. Column-backed fields (title; phone as a first phone row)
// fill only when empty; every field that lands writes its evidence row and
// provenance stamp. A field whose column is occupied or whose evidence row
// exists is counted skipped — the earlier answer (human or agent) stands.
func (s *Store) ApplySignatureFields(ctx context.Context, personID ids.PersonID, sourceActivity ids.UUID, fields []SignatureField) (SignatureApplyResult, error) {
	var res SignatureApplyResult
	if len(fields) == 0 {
		return res, nil
	}
	sourceRef := "activity:" + sourceActivity.String()
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var appliedFields []string
		for _, f := range fields {
			applied, err := s.applySignatureField(ctx, tx, personID, sourceRef, f)
			if err != nil {
				return err
			}
			if applied {
				res.Applied++
				appliedFields = append(appliedFields, f.Name)
			} else {
				res.Skipped++
			}
		}
		if len(appliedFields) == 0 {
			return nil
		}
		// The write shape: the enrichment is a person mutation, so the
		// audit row and the person.updated outbox event ride this commit.
		auditID, err := storekit.Audit(ctx, tx, "update", entityPerson, personID.UUID,
			nil, map[string]any{auditKeyFields: appliedFields, "source_ref": sourceRef})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, personID.UUID, crmcontracts.PublicEventPersonUpdated{
			ChangedFields: map[string]any{auditKeyFields: appliedFields, auditKeySource: enrichSource},
		})
	})
	if err != nil {
		return SignatureApplyResult{}, err
	}
	return res, nil
}

func (s *Store) applySignatureField(ctx context.Context, tx pgx.Tx, personID ids.PersonID, sourceRef string, f SignatureField) (bool, error) {
	value := strings.TrimSpace(f.Value)
	if value == "" {
		return false, nil
	}
	wsID := workspaceID(ctx)

	// The evidence row is the admission ticket: one row per (person, field),
	// first verdict wins — a later pass can never overwrite it.
	tag, err := tx.Exec(ctx, `
		INSERT INTO person_profile_field (workspace_id, person_id, field, value, evidence_snippet, source_ref, confidence, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (person_id, field) DO NOTHING`,
		wsID, personID, f.Name, value, f.Evidence, sourceRef, f.Confidence, enrichSource, enrichCapturedBy)
	if err != nil {
		return false, fmt.Errorf("people: signature evidence row (%s): %w", f.Name, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	switch f.Name {
	case "title":
		// Fill-only-empty: the NULL predicate is the CAS — an occupied
		// title (human or otherwise) is never touched (GATE-AI-4).
		tag, err := tx.Exec(ctx, `
			UPDATE person SET title = $2 WHERE id = $1 AND title IS NULL`, personID, value)
		if err != nil {
			return false, fmt.Errorf("people: signature title fill: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// A concurrent writer filled the title after the candidate
			// query: the evidence row must not claim a value that never
			// applied — withdraw it and count the field skipped.
			return false, revokeSignatureEvidence(ctx, tx, personID, f.Name)
		}
	case "phone":
		// A first phone only — a person with any live phone row keeps it.
		tag, err := tx.Exec(ctx, `
			INSERT INTO person_phone (workspace_id, person_id, phone, phone_type, is_primary, position, source, captured_by)
			SELECT $1, $2, $3, 'work', true, 0, $4, $5
			WHERE NOT EXISTS (
				SELECT 1 FROM person_phone WHERE person_id = $2 AND archived_at IS NULL)`,
			wsID, personID, value, enrichSource, enrichCapturedBy)
		if err != nil {
			return false, fmt.Errorf("people: signature phone fill: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return false, revokeSignatureEvidence(ctx, tx, personID, f.Name)
		}
	}
	// role / linkedin / org_name live only in the sidecar: no record column
	// to fill, and org_name in particular must never touch an organization.

	if err := storekit.StampFields(ctx, tx, entityPerson, personID.UUID, sourceRef, enrichCapturedBy,
		[]storekit.FieldStamp{{Field: f.Name}}); err != nil {
		return false, err
	}
	return true, nil
}

// revokeSignatureEvidence withdraws the just-inserted evidence row when
// its guarded column fill lost the race: evidence must never claim a
// value the record does not carry.
func revokeSignatureEvidence(ctx context.Context, tx pgx.Tx, personID ids.PersonID, field string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM person_profile_field
		WHERE person_id = $1 AND field = $2 AND source = $3`,
		personID, field, enrichSource); err != nil {
		return fmt.Errorf("people: withdrawing unapplied signature evidence (%s): %w", field, err)
	}
	return nil
}

// MarkSignatureRead advances the read watermark to THIS mail, whatever the read
// yielded. It is what stops a person whose signature states nothing the pass
// wants from being re-read — and re-billed — every night: they return as a
// candidate only when mail NEWER than the watermark arrives.
//
// The watermark is the message's own occurred_at and it only moves forward
// (GREATEST): a cursor that could rewind would reopen the person as soon as the
// mail it names is archived, or whenever two workers finished out of order.
//
// Bookkeeping, not a domain mutation, so it carries no audit or outbox row — the
// same posture as the capture auto-enrich cursor. Deliberately outside the apply
// transaction too: losing the watermark after a successful apply costs one
// repeated read whose evidence rows are all first-verdict-wins inserts, so the
// repeat changes nothing.
func (s *Store) MarkSignatureRead(ctx context.Context, personID ids.PersonID, activityID ids.UUID) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// occurred_at is read from the activity rather than taken from the
		// caller: the watermark and the candidate query must compare the same
		// number, and only one of them can be the row itself.
		tag, err := tx.Exec(ctx, `
			INSERT INTO person_signature_enrich_state (workspace_id, person_id, activity_id, last_activity_at)
			SELECT $1, $2, a.id, a.occurred_at FROM activity a WHERE a.id = $3
			ON CONFLICT (person_id) DO UPDATE
			SET activity_id = EXCLUDED.activity_id,
			    last_activity_at = GREATEST(person_signature_enrich_state.last_activity_at,
			                                EXCLUDED.last_activity_at),
			    attempted_at = now()`,
			workspaceID(ctx), personID, activityID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// The activity is gone (erased, archived away) between the candidate
			// query and here. Nothing to watermark against; the next pass reads
			// whatever mail this person has now.
			return nil
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("people: recording the signature read: %w", err)
	}
	return nil
}

// SignatureCandidate is one person the enrich pass should look at: a
// connector-created person with an unanswered signature field, and the mail to
// read the signature from.
type SignatureCandidate struct {
	PersonID   ids.PersonID
	FullName   string
	Email      string
	ActivityID ids.UUID
	Body       string // the latest inbound email's stored body
}

// SignatureCandidates lists connector-created people whose signature still
// has something to say, each with their most recent inbound linked email —
// the §2.9 candidate set, bounded for one pass.
//
// "Something to say" is per FIELD, not per person (the PO-F-2a amendment): a
// person is a candidate while they lack a title AND a phone, OR while they
// lack the `org_name` evidence the name-promotion rule reads. The predicate
// used to retire a person the moment ANY profile-field row existed, so one
// accepted title permanently silenced the company name their signature also
// states.
//
// Asking is bounded by person_signature_enrich_state: the pass watermarks the
// mail it read, and a person returns as a candidate only when mail newer than
// the watermark arrives. Without that a person whose signature simply never
// states a company would be re-read every night forever, paying a model call
// each time. The comparison is on occurred_at, not on the activity id — an
// identity check would reopen the person the moment the mail it names is
// archived, and the query would then pay to re-read an OLDER signature.
func (s *Store) SignatureCandidates(ctx context.Context, limit int) ([]SignatureCandidate, error) {
	var out []SignatureCandidate
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id, p.full_name, coalesce(pe.email, ''), a.id, coalesce(a.body, '')
			FROM person p
			LEFT JOIN person_email pe ON pe.person_id = p.id AND pe.is_primary AND pe.archived_at IS NULL
			JOIN LATERAL (
				SELECT a.id, a.body, a.occurred_at
				FROM activity_link al
				JOIN activity a ON a.id = al.activity_id
				WHERE al.person_id = p.id AND al.entity_type = 'person'
				  AND a.kind = 'email' AND a.direction = 'inbound' AND a.archived_at IS NULL
				ORDER BY a.occurred_at DESC
				LIMIT 1
			) a ON true
			LEFT JOIN person_signature_enrich_state st ON st.person_id = p.id
			WHERE p.captured_by LIKE 'connector:%' AND p.archived_at IS NULL AND p.merged_into_id IS NULL
			  AND (
				(p.title IS NULL
				  AND NOT EXISTS (SELECT 1 FROM person_phone ph WHERE ph.person_id = p.id AND ph.archived_at IS NULL)
				  AND NOT EXISTS (SELECT 1 FROM person_profile_field f
				                  WHERE f.person_id = p.id AND f.field IN ('title', 'phone')))
				OR NOT EXISTS (SELECT 1 FROM person_profile_field f
				               WHERE f.person_id = p.id AND f.field = 'org_name')
			  )
			  AND (st.last_activity_at IS NULL OR a.occurred_at > st.last_activity_at)
			ORDER BY p.created_at
			LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c SignatureCandidate
			if err := rows.Scan(&c.PersonID, &c.FullName, &c.Email, &c.ActivityID, &c.Body); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("people: listing signature candidates: %w", err)
	}
	return out, nil
}
