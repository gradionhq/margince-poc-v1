// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The edge between capture and the timeline's attachment store.
//
// Capture reads the mailbox and owns the transaction; activities owns the
// attachment table. Neither imports the other, so the join is here — the same
// arrangement the counterparty ensurer already uses, and the reason the
// table-ownership fitness test passes with one writer per table.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// errStagedFileNotOurs marks a staged file that did not come from this
// adapter's own Stage call — a wiring mistake, never a runtime condition.
var errStagedFileNotOurs = errors.New(
	"compose: a captured file was staged by something other than the timeline store")

// capturedFileKeeper adapts the timeline store to capture's seam.
type capturedFileKeeper struct {
	store *activities.Store
}

var _ capture.FileKeeper = capturedFileKeeper{}

func (k capturedFileKeeper) Stage(
	ctx context.Context, files []capture.CapturedFile,
) ([]capture.StagedFile, error) {
	owned := make([]activities.CapturedFile, 0, len(files))
	for _, file := range files {
		owned = append(owned, activities.CapturedFile{
			PartID:       file.PartID,
			Filename:     file.Filename,
			ContentType:  file.ContentType,
			DeclaredType: file.DeclaredType,
			Body:         file.Body,
		})
	}
	staged, err := k.store.StageCapturedFiles(ctx, owned)
	if err != nil {
		return nil, err
	}
	// Returned opaque: capture carries these from Stage to Record and never
	// reads inside one, which is what keeps the row shape the owner's business.
	out := make([]capture.StagedFile, 0, len(staged))
	for _, file := range staged {
		out = append(out, file)
	}
	return out, nil
}

func (k capturedFileKeeper) Record(
	ctx context.Context, tx pgx.Tx, activityID ids.ActivityID,
	from capture.FileSource, staged []capture.StagedFile,
) error {
	owned := make([]activities.StagedFile, 0, len(staged))
	for _, file := range staged {
		typed, ok := file.(activities.StagedFile)
		if !ok {
			// The marker is satisfied by exactly one type today, so this cannot
			// happen through this adapter — but a silently skipped file is the
			// one outcome that must never be possible on a path whose promise
			// is that what arrived is what is stored.
			return errStagedFileNotOurs
		}
		owned = append(owned, typed)
	}
	return k.store.RecordCapturedFiles(ctx, tx, activityID, activities.CapturedFileSource{
		System:     from.System,
		MessageID:  from.MessageID,
		CapturedBy: from.CapturedBy,
	}, owned)
}
