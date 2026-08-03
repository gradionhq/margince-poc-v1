// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The lead half of the capture Sink: a captured prospect never becomes a
// person or organization directly (ADR-0008 — leads graduate, raw capture does
// not mint clean-core rows), and a collision with a live lead from another
// source stages a merge proposal instead of folding the two together.

package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// captureLead lands one lead behind the suppression and dedupe guards.
// A collision with a live lead from another source writes nothing in
// this transaction: it returns the incumbent's ref plus the collision
// (the incumbent's id and the captured fields) for the caller to stage
// after commit.
func (s *Sink) captureLead(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (datasource.EntityRef, *ids.LeadID, json.RawMessage, error) {
	// Provider payloads carry whitespace; every downstream email
	// comparison (suppression, dedupe, the DB lower()) assumes a
	// trimmed address.
	fields.Email = strings.TrimSpace(fields.Email)
	// The A13 resurrection guard: an erased subject's address
	// refuses re-capture — deletion sticks. The natural key, not
	// the address, names the skip (the log must not re-store PII).
	//
	// An address is the only identifier this path can be given: LeadFields
	// carries no channel identity, because a channel identity is a
	// person-resolution key and a lead is not a person (ADR-0008 — leads
	// graduate). So the channel twin of this probe has nothing to guard here; it
	// guards the path a channel record does take: Sink.Upsert's own transaction
	// (sinkchannel.go), under the account's advisory lock, with people's
	// EnsureChannelCounterparty probing again after that commit.
	//
	// Note the "natural key names the skip" rule above holds for THIS path only:
	// a mail natural key is a message-id, whereas a channel record's embeds the
	// account id itself, so the channel refusal names no identifier at all.
	if fields.Email != "" {
		suppressed, err := storekit.EmailSuppressed(ctx, tx, fields.Email)
		if err != nil {
			return datasource.EntityRef{}, nil, nil, err
		}
		if suppressed {
			return datasource.EntityRef{}, nil, nil, fmt.Errorf("capture: %s/%s matches the erasure suppression list: %w",
				rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, connector.ErrSkip)
		}
		existing, captured, err := s.findLeadCollision(ctx, tx, rec, fields)
		if err != nil {
			return datasource.EntityRef{}, nil, nil, err
		}
		if existing != nil {
			return datasource.EntityRef{Type: datasource.EntityLead, ID: existing.UUID}, existing, captured, nil
		}
	}
	id, created, err := s.upsertLead(ctx, tx, rec, fields)
	if err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	ref := datasource.EntityRef{Type: datasource.EntityLead, ID: id.UUID}
	if !created {
		return ref, nil, nil, nil
	}
	auditID, err := storekit.Audit(ctx, tx, "create", "lead", id.UUID, nil, fields)
	if err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, leadCreatedCapturePayload(rec.NaturalKey.SourceSystem)); err != nil {
		return datasource.EntityRef{}, nil, nil, err
	}
	return ref, nil, nil, nil
}

// leadCreatedCapturePayload builds the lead.created event for the
// capture auto-create path — the one emit site (of the event's two)
// that names an originating source system; the direct-create path
// (people/lead.go) sets no fields at all.
func leadCreatedCapturePayload(sourceSystem string) crmcontracts.PublicEventLeadCreated {
	return crmcontracts.PublicEventLeadCreated{SourceSystem: &sourceSystem}
}

func (s *Sink) upsertLead(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (ids.LeadID, bool, error) {
	if err := auth.Require(ctx, "lead", principal.ActionCreate); err != nil {
		return ids.LeadID{}, false, err
	}
	var id ids.LeadID
	err := tx.QueryRow(ctx, `
		INSERT INTO lead (workspace_id, full_name, email, company_name, title, source_system, source_id, source, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        NULLIF($1, ''), NULLIF(lower($2), ''), NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7, $8)
		ON CONFLICT (workspace_id, source_system, source_id) WHERE source_system IS NOT NULL AND source_id IS NOT NULL
		DO NOTHING
		RETURNING id`,
		fields.FullName, fields.Email, fields.CompanyName, fields.Title,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, captureSource(rec), rec.CapturedBy).Scan(&id)
	if err == nil {
		var stamps []storekit.FieldStamp
		for _, f := range []struct{ field, value string }{
			{"full_name", fields.FullName},
			{"email", fields.Email},
			{"company_name", fields.CompanyName},
			{"title", fields.Title},
		} {
			if f.value != "" {
				stamps = append(stamps, storekit.FieldStamp{Field: f.field})
			}
		}
		if err := storekit.StampFields(ctx, tx, "lead", id.UUID, captureSource(rec), rec.CapturedBy, stamps); err != nil {
			return ids.LeadID{}, false, err
		}
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.LeadID{}, false, fmt.Errorf("capture: lead upsert: %w", err)
	}
	// Replay: the natural key already landed. Returning a record is a read, so
	// the row scope binds here too — ownership can move after the first
	// capture, and a replay must not hand back a lead the caller lost sight of.
	err = tx.QueryRow(ctx,
		`SELECT id FROM lead WHERE source_system = $1 AND source_id = $2`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID).Scan(&id)
	if err != nil {
		return ids.LeadID{}, false, fmt.Errorf("capture: lead replay lookup: %w", err)
	}
	if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return ids.LeadID{}, false, skipInvisibleIncumbent(rec, "lead")
		}
		return ids.LeadID{}, false, err
	}
	return id, false, nil
}

// findLeadCollision looks for a LIVE lead already carrying this address from a
// DIFFERENT source. That is a collision, not a second row: the caller stages a
// merge proposal after the transaction commits rather than folding the two
// together here. A replay of the same natural key is not a collision — the
// idempotent upsert path handles it.
// The probe itself is storekit's, shared with the direct-create path: the two
// lead write shapes answer a claimed identity differently — that path refuses
// with the incumbent's id, this one stages a merge for a human — but they must
// not disagree about what "already claimed" means.
func (s *Sink) findLeadCollision(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields LeadFields) (*ids.LeadID, json.RawMessage, error) {
	existing, found, err := storekit.LiveLeadByEmail(ctx, tx, fields.Email, &storekit.LeadSourceKey{
		SourceSystem: rec.NaturalKey.SourceSystem,
		SourceID:     rec.NaturalKey.SourceID,
	})
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, nil
	}
	// An incumbent outside the granting human's row scope is not the
	// connector's to resolve: it can be neither merged into nor ignored in
	// favour of a second row, so the record is skipped and the address stays
	// where a human with the scope to see both can act on it.
	if err := auth.EnsureVisible(ctx, tx, "lead", existing.UUID); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil, nil, skipInvisibleIncumbent(rec, "lead")
		}
		return nil, nil, err
	}
	captured, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	return &existing, captured, nil
}
