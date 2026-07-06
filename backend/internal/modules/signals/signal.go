// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Signal CRUD (B-E08.1): the store owns the transactional write shape.
// A signal created ABOUT a known record enters resolution_state=resolved;
// a raw item (only a raw_ref) enters unresolved and waits for the
// resolver. Row scope follows the SUBJECT entity — a signal about a
// record the caller cannot see does not exist for them (existence-hiding,
// enforced through auth.SignalScopeClause, the one spelling shared with
// the approvals surface).

package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type CreateSignalInput struct {
	Kind          string
	SourceChannel string
	RawRef        *string
	EntityType    *string
	EntityID      *ids.UUID
	Severity      string
	Summary       string
	Evidence      []crmcontracts.SignalEvidence
	DetectedAt    *time.Time
	Source        string
}

func (s *Store) CreateSignal(ctx context.Context, in CreateSignalInput) (crmcontracts.Signal, error) {
	if err := auth.Require(ctx, "signal", principal.ActionCreate); err != nil {
		return crmcontracts.Signal{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Signal{}, err
	}
	if strings.TrimSpace(in.Summary) == "" {
		return crmcontracts.Signal{}, &RequiredFieldError{Field: "summary"}
	}
	if (in.EntityType == nil) != (in.EntityID == nil) {
		return crmcontracts.Signal{}, &RequiredFieldError{Field: "entity_id"}
	}

	sourceChannel := in.SourceChannel
	if sourceChannel == "" {
		sourceChannel = "derived"
	}
	severity := in.Severity
	if severity == "" {
		severity = "info"
	}
	// A subject-bearing signal is born attributed; a raw item waits for
	// the resolver (data-model §12.5 lifecycle).
	resolutionState := "resolved"
	if in.EntityType == nil {
		resolutionState = "unresolved"
	}
	detectedAt := time.Now().UTC()
	if in.DetectedAt != nil {
		detectedAt = in.DetectedAt.UTC()
	}
	evidence := in.Evidence
	if evidence == nil {
		evidence = []crmcontracts.SignalEvidence{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return crmcontracts.Signal{}, fmt.Errorf("marshal signal evidence: %w", err)
	}

	var out crmcontracts.Signal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if in.EntityType != nil {
			// The subject must exist AND be visible to the caller: the
			// polymorphic ref has no FK, so an unchecked id would persist
			// a cross-tenant (or out-of-scope) link.
			if err := auth.EnsureLinkTarget(ctx, tx, *in.EntityType, *in.EntityID); err != nil {
				return err
			}
		}
		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO signal (id, workspace_id, kind, source_channel, raw_ref, entity_type, entity_id,
			                     resolution_state, severity, summary, evidence, detected_at, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			id, storekit.MustWorkspace(ctx), in.Kind, sourceChannel, in.RawRef, in.EntityType, in.EntityID,
			resolutionState, severity, in.Summary, evidenceJSON, detectedAt, in.Source, by)
		if err != nil {
			return fmt.Errorf("insert signal: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "signal", id, nil,
			map[string]any{"kind": in.Kind, "source_channel": sourceChannel, "resolution_state": resolutionState})
		if err != nil {
			return fmt.Errorf("audit signal create: %w", err)
		}
		if out, err = readSignal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created signal: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "signal.detected", "signal", id, detectedPayload(out)); err != nil {
			return fmt.Errorf("emit signal.detected: %w", err)
		}
		return nil
	})
	return out, err
}

// detectedPayload is the events.md §5.11 signal.detected shape.
func detectedPayload(sig crmcontracts.Signal) map[string]any {
	payload := map[string]any{
		"signal_id":        sig.Id,
		"kind":             sig.Kind,
		"source_channel":   sig.SourceChannel,
		"resolution_state": sig.ResolutionState,
		"severity":         sig.Severity,
	}
	if sig.EntityType != nil {
		payload["entity_type"] = *sig.EntityType
		payload["entity_id"] = sig.EntityId
	}
	if sig.ResolutionConfidence != nil {
		payload["resolution_confidence"] = *sig.ResolutionConfidence
	}
	return payload
}

func (s *Store) GetSignal(ctx context.Context, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Signal, error) {
	if err := auth.Require(ctx, "signal", principal.ActionRead); err != nil {
		return crmcontracts.Signal{}, err
	}
	var out crmcontracts.Signal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureSignalVisible(ctx, tx, id); err != nil {
			return err
		}
		var err error
		out, err = readSignal(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListSignalsInput struct {
	Cursor          *string
	Limit           *int
	Status          *string
	Kind            *string
	ResolutionState *string
	IncludeArchived bool
}

func (s *Store) ListSignals(ctx context.Context, in ListSignalsInput) ([]crmcontracts.Signal, storekit.Page, error) {
	if err := auth.Require(ctx, "signal", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	if !in.IncludeArchived {
		where = append(where, "s.archived_at IS NULL")
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf("s.status = $%d", arg(*in.Status)))
	}
	if in.Kind != nil {
		where = append(where, storekit.SQLf("s.kind = $%d", arg(*in.Kind)))
	}
	if in.ResolutionState != nil {
		where = append(where, storekit.SQLf("s.resolution_state = $%d", arg(*in.ResolutionState)))
	}
	scope, err := auth.SignalScopeClause(ctx, "s", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(s.created_at, s.id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var signals []crmcontracts.Signal
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+signalColumns("s")+` FROM signal s WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY s.created_at DESC, s.id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sig, err := scanSignal(rows)
			if err != nil {
				return err
			}
			signals = append(signals, sig)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(signals) > limit {
			signals = signals[:limit]
			last := signals[len(signals)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if signals == nil {
		signals = []crmcontracts.Signal{}
	}
	return signals, page, err
}

type UpdateSignalInput struct {
	Status    *string
	Note      *string
	Severity  *string
	IfVersion *int64
}

// humanOutcomes are the triage moves that append the human-outcome row to
// signal_resolution (data-model §12.5: outcome/note/resolved_by, distinct
// from the resolver's match-basis columns).
var humanOutcomes = map[string]bool{"acknowledged": true, "resolved": true, "dismissed": true}

func (s *Store) UpdateSignal(ctx context.Context, id ids.UUID, in UpdateSignalInput) (crmcontracts.Signal, error) {
	if err := auth.Require(ctx, "signal", principal.ActionUpdate); err != nil {
		return crmcontracts.Signal{}, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return crmcontracts.Signal{}, err
	}
	var out crmcontracts.Signal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureSignalVisible(ctx, tx, id); err != nil {
			return err
		}
		current, err := readSignal(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		if in.Status != nil {
			p.Set("status", string(current.Status), *in.Status)
		}
		if in.Severity != nil {
			p.Set("severity", string(current.Severity), *in.Severity)
		}
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.Apply(ctx, tx, "signal", id, in.IfVersion); err != nil {
			return fmt.Errorf("apply signal patch: %w", err)
		}
		if in.Status != nil && humanOutcomes[*in.Status] && *in.Status != string(current.Status) {
			if _, err := tx.Exec(ctx,
				`INSERT INTO signal_resolution (id, workspace_id, signal_id, outcome, note, resolved_by, source, captured_by)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				ids.NewV7(), storekit.MustWorkspace(ctx), id, *in.Status, in.Note,
				storekit.UUIDOrNil(actor.UserID), "ui", actor.ID); err != nil {
				return fmt.Errorf("append signal outcome: %w", err)
			}
		}
		if _, err := storekit.Audit(ctx, tx, "update", "signal", id, p.Before(), p.After()); err != nil {
			return fmt.Errorf("audit signal update: %w", err)
		}
		if out, err = readSignal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated signal: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) ArchiveSignal(ctx context.Context, id ids.UUID) (crmcontracts.Signal, error) {
	if err := auth.Require(ctx, "signal", principal.ActionDelete); err != nil {
		return crmcontracts.Signal{}, err
	}
	var out crmcontracts.Signal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureSignalVisible(ctx, tx, id); err != nil {
			return err
		}
		if _, err := readSignal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE signal SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id); err != nil {
			return fmt.Errorf("archive signal: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", "signal", id, nil, nil); err != nil {
			return fmt.Errorf("audit signal archive: %w", err)
		}
		var err error
		if out, err = readSignal(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return fmt.Errorf("read archived signal: %w", err)
		}
		return nil
	})
	return out, err
}

// signalColumns spells the SELECT list once; alias qualifies for queries
// that join or scope.
func signalColumns(alias string) string {
	cols := []string{"id", "workspace_id", "kind", "source_channel", "raw_ref", "entity_type", "entity_id",
		"resolution_state", "resolution_confidence::float8", "resolved_org_id", "resolved_person_id",
		"severity", "summary", "evidence", "status", "detected_at", "source", "captured_by",
		"version", "created_at", "updated_at", "archived_at"}
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

func readSignal(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Signal, error) {
	q := `SELECT ` + signalColumns("s") + ` FROM signal s WHERE s.id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND s.archived_at IS NULL`
	}
	sig, err := scanSignal(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Signal{}, apperrors.ErrNotFound
	}
	return sig, err
}

func scanSignal(row pgx.Row) (crmcontracts.Signal, error) {
	var sig crmcontracts.Signal
	var id, wsID ids.UUID
	var kind, sourceChannel, resolutionState, severity, status string
	var entityType *string
	var entityID, resolvedOrgID, resolvedPersonID *ids.UUID
	var confidence *float64
	var evidenceJSON []byte
	var capturedBy string
	var version int64

	err := row.Scan(&id, &wsID, &kind, &sourceChannel, &sig.RawRef, &entityType, &entityID,
		&resolutionState, &confidence, &resolvedOrgID, &resolvedPersonID,
		&severity, &sig.Summary, &evidenceJSON, &status, &sig.DetectedAt, &sig.Source, &capturedBy,
		&version, &sig.CreatedAt, &sig.UpdatedAt, &sig.ArchivedAt)
	if err != nil {
		return sig, err
	}
	if err := json.Unmarshal(evidenceJSON, &sig.Evidence); err != nil {
		return sig, fmt.Errorf("signal evidence is not the contract shape: %w", err)
	}
	sig.Id = openapi_types.UUID(id)
	sig.WorkspaceId = openapi_types.UUID(wsID)
	sig.Kind = crmcontracts.SignalKind(kind)
	sig.SourceChannel = crmcontracts.SignalSourceChannel(sourceChannel)
	sig.ResolutionState = crmcontracts.SignalResolutionState(resolutionState)
	sig.Severity = crmcontracts.SignalSeverity(severity)
	sig.Status = crmcontracts.SignalStatus(status)
	if entityType != nil {
		converted := crmcontracts.SignalEntityType(*entityType)
		sig.EntityType = &converted
	}
	sig.EntityId = uuidPtr(entityID)
	sig.ResolvedOrgId = uuidPtr(resolvedOrgID)
	sig.ResolvedPersonId = uuidPtr(resolvedPersonID)
	if confidence != nil {
		converted := float32(*confidence)
		sig.ResolutionConfidence = &converted
	}
	sig.CapturedBy = &capturedBy
	sig.Version = &version
	return sig, nil
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}
