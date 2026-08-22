// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The release snapshot, typed once so the writer (publish) and the only reader
// that interprets it (the buyer edge) cannot drift on a key. The seller's
// release read hands the snapshot over as an opaque object and does not care.

import (
	"encoding/json"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// releaseSnapshot is what a release freezes: every editorial value a buyer
// reads. Task DEFINITIONS are here; task completion deliberately is not.
type releaseSnapshot struct {
	Title          string              `json:"title"`
	DealID         openapi_types.UUID  `json:"deal_id"`
	ReleasedAt     time.Time           `json:"released_at"`
	WelcomeMessage *string             `json:"welcome_message,omitempty"`
	StewardUserID  *openapi_types.UUID `json:"steward_user_id,omitempty"`
	Tasks          []snapshotTask      `json:"tasks"`
}

// snapshotTask is a to-do as published: what it says, who owes it, where it
// sits. Nothing about whether it is done.
type snapshotTask struct {
	ID       openapi_types.UUID `json:"id"`
	Side     string             `json:"side"`
	Title    string             `json:"title"`
	Position int                `json:"position"`
}

// snapshotOf copies every buyer-visible editorial value into the frozen
// projection. What is NOT here matters as much as what is: no live CRM read
// reaches the buyer through a release, so a deal renamed after publication does
// not silently rewrite what the buyer was shown.
func snapshotOf(room crmcontracts.DealRoom, tasks []crmcontracts.DealRoomTask) releaseSnapshot {
	snap := releaseSnapshot{
		Title:          room.Title,
		DealID:         room.DealId,
		ReleasedAt:     time.Now().UTC(),
		WelcomeMessage: room.WelcomeMessage,
		StewardUserID:  room.StewardUserId,
		// Never nil: a release with no tasks says so with an empty list, so a
		// reader cannot mistake "published before tasks existed" for "no tasks".
		Tasks: make([]snapshotTask, 0, len(tasks)),
	}
	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, snapshotTask{
			ID: t.Id, Side: string(t.Side), Title: t.Title, Position: t.Position,
		})
	}
	return snap
}

// decodeSnapshot reads a stored release back. A release published before task
// definitions rode the snapshot decodes with no tasks, which is the truth about
// what that release showed.
func decodeSnapshot(raw []byte) (releaseSnapshot, error) {
	var snap releaseSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return releaseSnapshot{}, fmt.Errorf("decode release snapshot: %w", err)
	}
	return snap, nil
}
