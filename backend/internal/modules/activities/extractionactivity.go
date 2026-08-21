// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ExtractionActivitySource names this source to the AI-activity projection. It
// is IDENTITY, not display: two sources must never collide on one occurrence
// key, and the display kind is a separate string that may be re-pointed without
// re-keying every row.
const ExtractionActivitySource = "attachment_extraction"

// ExtractionAITask is the api/ai-tasks.yaml task a document reading runs.
// Exported so a root-package fitness test can hold it to the generated task
// set — a module may not import the ai module to assert that about itself.
const ExtractionAITask = "document_extract"

// emitExtractionActivity publishes one reading's current state to the
// AI-activity projection.
//
// Derived ENTIRELY from the row, so no call site can disagree with another
// about what it just wrote — including the attempt, which is the row's own
// column rather than anything this function counts.
//
// The lease travels with the event because only this package knows it. The
// projection cannot ask (it may not import this module) and must not guess: a
// reader that renders a dead reading as live is the whole failure this closes.
func emitExtractionActivity(ctx context.Context, tx pgx.Tx, ledgerID ids.UUID, read ExtractionRead) error {
	lease := int(ExtractionReadLease.Seconds())
	task := ExtractionAITask
	payload := crmcontracts.InternalEventAiTaskStateChanged{
		Source: ExtractionActivitySource,
		// The reading's own row id. One attachment is read many times over its
		// life and each reading is its own occurrence, so the key is the
		// reading — never the document.
		OccurrenceKey: read.ID.String(),
		Kind:          ExtractionAITask,
		AiTask:        &task,
		Attempt:       read.Attempt,
		State:         read.Status,
		// The instant THIS attempt was enqueued, not the occurrence's first.
		// The projection ages a live row from here, and a reading re-queued an
		// hour after it was created is past its lease before any worker sees it
		// if it carries created_at instead.
		QueuedAt:     read.AttemptAt,
		StartedAt:    read.StartedAt,
		FinishedAt:   read.FinishedAt,
		LeaseSeconds: &lease,
		SubjectType:  ptrOrNil("attachment"),
		SubjectId:    contractUUID(read.AttachmentID),
		// StatusDetail is this module's own closed vocabulary on the failure
		// path and a rep-facing sentence on the empty-but-correct one. It is
		// never a provider's message — FinishExtractionRead's callers own that
		// rule — so it is safe to carry to a rail an ordinary rep reads.
		Summary: read.StatusDetail,
	}
	if read.Status == ExtractionReadFailed {
		payload.DegradeReason = read.StatusDetail
		payload.Summary = nil
	}
	if err := storekit.EmitPipelinePayload(ctx, tx, ledgerID, payload); err != nil {
		return fmt.Errorf("publish extraction reading activity: %w", err)
	}
	return nil
}

// logExtractionActivity writes the ledger row a state change needs when the
// transition has none of its own, and publishes the change against it.
//
// Every event carries a ledger trace link, and four of this source's six
// transitions never wrote one: a claim, a release, a re-arm and (before this)
// nothing at all recorded that the reading had moved. The system_log row is
// what keeps the outcome attributable, which is the condition on riding the bus
// without an entity ref at all.
func logExtractionActivity(ctx context.Context, tx pgx.Tx, read ExtractionRead) error {
	ledgerID, err := storekit.LogSystem(ctx, tx, "ai_task.state_changed", map[string]any{
		"source": ExtractionActivitySource, "occurrence_key": read.ID.String(),
		"state": read.Status, "attempt": read.Attempt,
	})
	if err != nil {
		return fmt.Errorf("log extraction reading state change: %w", err)
	}
	return emitExtractionActivity(ctx, tx, ledgerID, read)
}

// ptrOrNil is the payload's optional-string spelling: absent means absent, and
// an empty string in an optional field reads as present to every consumer.
func ptrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// contractUUID crosses the one type boundary between the kernel's id and the
// generated payload's — both are [16]byte, and the generator names its own.
func contractUUID(id ids.UUID) *openapi_types.UUID {
	if id.IsZero() {
		return nil
	}
	out := openapi_types.UUID(id)
	return &out
}
