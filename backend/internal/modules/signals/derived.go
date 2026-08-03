// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

// Signals a producer derived rather than a human filed.
//
// The table has carried a `derived` source_channel since 0047 and nothing ever
// wrote one: the only entry point was POST /signals, which is human-only. This
// is the writer, and it stays in THIS module because the module owns the table
// — a producer computes WHICH accounts to raise, and hands the finding here to
// be written.
//
// A derived signal is an OBSERVATION. It is written directly, attributed to the
// producer that drew it, and carries the evidence a reader can open. What
// follows from one — a lifecycle change, a deal, a task — is a structural claim
// and stages for a human instead.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// resolutionResolved is a derived signal's resolution state: a producer that
// computed a finding about a KNOWN account has already resolved it — the
// resolver exists for raw signals that arrive naming nobody.
const resolutionResolved = "resolved"

// DerivedSignal is one finding a producer computed, in the words the reader
// will see. The producer owns the RULE; this module owns the row.
type DerivedSignal struct {
	Kind           string
	OrganizationID ids.UUID
	Summary        string
	Severity       string
	// Fingerprint identifies the finding by what it fired ON, so a producer
	// that runs hourly raises nothing new on an unchanged account and a
	// dismissal survives every later pass.
	Fingerprint string
	// Evidence is the records the finding was drawn from, each openable.
	Evidence []DerivedEvidence
	// Audit carries whatever the producer wants recorded about the derivation
	// beyond the row itself.
	Audit map[string]any
}

// DerivedEvidence is one citable record behind a derived signal.
type DerivedEvidence struct {
	Snippet    string
	ActivityID ids.UUID
}

// RecordDerived writes one derived signal inside the caller's transaction, or
// reports that the same finding was already raised.
//
// It returns false rather than an error on a repeat: a producer pass over an
// unchanged account is the normal case, not a failure, and the fingerprint
// index covers dismissed rows so a dismissal is never undone by the next pass.
func RecordDerived(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, in DerivedSignal, detectedAt time.Time) (bool, error) {
	evidence, err := json.Marshal(derivedEvidenceRows(in.Evidence))
	if err != nil {
		return false, fmt.Errorf("encode derived signal evidence: %w", err)
	}
	var signalID ids.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO signal
		  (workspace_id, kind, entity_type, entity_id, resolved_org_id, summary,
		   evidence, fingerprint, source_channel, resolution_state, severity,
		   status, detected_at, source, captured_by)
		VALUES ($1, $2, 'organization', $3, $3, $4, $5, $6,
		        'derived', 'resolved', $7, 'open', $8, 'signal-scan', $9)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		wsID, in.Kind, in.OrganizationID, in.Summary, evidence, in.Fingerprint,
		in.Severity, detectedAt, "agent:"+in.Kind).Scan(&signalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("write derived signal: %w", err)
	}

	// The write shape, in the caller's one transaction. The audit names the
	// SIGNAL: the account is its subject, not the record that changed.
	auditID, err := storekit.Audit(ctx, tx, "create", "signal", signalID, nil, in.Audit)
	if err != nil {
		return false, fmt.Errorf("audit derived signal: %w", err)
	}
	subjectType := "organization"
	subjectID := openapi_types.UUID(in.OrganizationID)
	if err := storekit.EmitEvent(ctx, tx, auditID, signalID, crmcontracts.PublicEventSignalDetected{
		SignalId:        openapi_types.UUID(signalID),
		Kind:            in.Kind,
		SourceChannel:   "derived",
		ResolutionState: resolutionResolved,
		Severity:        in.Severity,
		// The signal's SUBJECT. The envelope's own entity is the signal.
		SubjectEntityType: &subjectType,
		SubjectEntityId:   &subjectID,
	}); err != nil {
		return false, fmt.Errorf("emit signal.detected: %w", err)
	}
	return true, nil
}

// derivedEvidenceRows renders evidence in the per-claim shape SIG-DDL-1 fixes,
// so a derived signal's evidence reads the same as a human-filed one's.
func derivedEvidenceRows(in []DerivedEvidence) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, cited := range in {
		out = append(out, map[string]any{
			"snippet":     cited.Snippet,
			"source_type": "activity",
			"source_id":   cited.ActivityID.String(),
		})
	}
	return out
}

// columnStatus and statusOpen are the one spelling of the triage column and
// the state this transition leaves: the CAS predicate and the audit's
// before-image must agree, and two literals would drift.
const (
	columnStatus = "status"
	statusOpen   = "open"
)

// AcknowledgeTx marks one open signal acknowledged inside the caller's
// transaction, and reports whether it moved.
//
// It exists for the approval effects: a human accepting the consequence of a
// signal has, by that act, seen it, and a page that moved the account while
// still shouting the signal that moved it contradicts itself. The move rides
// the SAME transaction as the structural write, so neither can land alone.
//
// Unlike UpdateSignal this takes no version pin. The caller is a released
// approval, not an editor with a stale copy in a browser tab: the only thing
// it needs to be true is that the signal is still open, and the WHERE clause
// is that check. A signal a human already triaged is left exactly as they
// left it, and the false says so.
func AcknowledgeTx(ctx context.Context, tx pgx.Tx, signalID ids.UUID) (bool, error) {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return false, err
	}
	// The status predicate IS the CAS: a signal a human already triaged has
	// left 'open' behind, the update matches nothing, and the false below says
	// their judgement stands.
	tag, err := tx.Exec(ctx, `
		UPDATE signal SET status = 'acknowledged', version = version + 1, updated_at = now()
		 WHERE id = $1 AND status = 'open' AND archived_at IS NULL`, signalID)
	if err != nil {
		return false, fmt.Errorf("acknowledge the signal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	// The append-only human-outcome row (data-model §12.5), the same one the
	// triage endpoint writes — the outcome is a human's, whoever's hands the
	// write passed through.
	if _, err := tx.Exec(ctx,
		`INSERT INTO signal_resolution (id, workspace_id, signal_id, outcome, resolved_by, source, captured_by)
		 VALUES ($1, $2, $3, 'acknowledged', $4, 'approval', $5)`,
		ids.NewV7(), storekit.MustWorkspace(ctx), signalID,
		storekit.UUIDOrNil(actor.UserID), actor.ID); err != nil {
		return false, fmt.Errorf("append the signal outcome: %w", err)
	}
	if _, err := storekit.Audit(ctx, tx, "update", "signal", signalID,
		map[string]any{columnStatus: statusOpen},
		map[string]any{columnStatus: "acknowledged"}); err != nil {
		return false, fmt.Errorf("audit the acknowledgement: %w", err)
	}
	return true, nil
}

// AcknowledgeOpenForOrgTx marks every open signal of one kind on one account
// acknowledged, and reports how many moved.
//
// An account can carry several signals saying the same thing — three
// conversations that each mention the contract ending are three rows. A human
// answering the proposal that came out of one of them has answered the
// question all of them ask, and settling only the cited row leaves the rest
// open forever: the record has left the stage the reconciler looks for, so no
// later pass considers them again and nothing else will ever close them.
//
// It takes the KIND rather than a list of ids because the decision was about
// the account's situation, not about which row happened to be quoted on the
// card the reader saw.
func AcknowledgeOpenForOrgTx(
	ctx context.Context, tx pgx.Tx, orgID ids.UUID, kind string,
) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM signal
		 WHERE resolved_org_id = $1 AND kind = $2
		   AND status = 'open' AND archived_at IS NULL
		 ORDER BY detected_at`, orgID, kind)
	if err != nil {
		return 0, fmt.Errorf("list the account's open signals: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[idsUUID])
	if err != nil {
		return 0, fmt.Errorf("read the account's open signals: %w", err)
	}
	settled := 0
	for _, signalID := range ids {
		// Each through the same CAS the single-signal path takes, so a row a
		// human triaged between the read and this write keeps their outcome.
		moved, err := AcknowledgeTx(ctx, tx, signalID)
		if err != nil {
			return settled, err
		}
		if moved {
			settled++
		}
	}
	return settled, nil
}

// idsUUID names the scan target for the id list above. pgx.RowTo needs a
// concrete type and ids.UUID is already one; the alias exists only so the
// generic call reads as what it collects.
type idsUUID = ids.UUID
