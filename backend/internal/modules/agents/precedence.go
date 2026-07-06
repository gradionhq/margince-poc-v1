// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The per-field human-edit-precedence split (interfaces.md §2.1):
// update_record is 🟢, but an agent never silently undoes a person.
// When a green patch touches fields whose CURRENT value a human last
// wrote, exactly those fields are split off into a 🟡 staged approval
// while the remainder of the patch proceeds green in the same call.
// This file is the ONE spelling of that partition — the MCP tool and
// the REST agent gate both split through it, so the two transports
// cannot drift on which fields a human decision protects.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// PatchSplit is one patch partitioned by field ownership. Staged holds
// exactly the human-owned fields the patch would overwrite; Green holds
// the remainder, nil when every touched field is human-owned. Field
// values keep their original raw bytes — the green remainder reaches the
// store exactly as the agent sent it.
type PatchSplit struct {
	Conflicts []string
	Staged    json.RawMessage
	Green     json.RawMessage
}

// SplitHumanOwned partitions patch by the audit-trail ownership answer.
// No conflicts leaves the whole patch green (Staged nil, Green = patch).
// A nil ownership resolver fails closed: without the audit-trail lookup
// the precedence question cannot be answered, and "cannot check" must
// never degrade into "agent overwrites the human".
func SplitHumanOwned(ctx context.Context, ownership FieldOwnership, entityType string, id ids.UUID, patch json.RawMessage) (PatchSplit, error) {
	if ownership == nil {
		return PatchSplit{}, errors.New("crmagents: human-edit precedence has no field-ownership resolver — refusing the update")
	}
	conflicts, err := ownership.HumanOwnedConflicts(ctx, entityType, id, patch)
	if err != nil {
		return PatchSplit{}, err
	}
	if len(conflicts) == 0 {
		return PatchSplit{Green: patch}, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil {
		return PatchSplit{}, fmt.Errorf("crmagents: human-edit precedence: patch is not a JSON object: %w", err)
	}
	staged := make(map[string]json.RawMessage, len(conflicts))
	for _, field := range conflicts {
		value, present := fields[field]
		if !present {
			// The ownership probe answered for a field the patch does not
			// carry — a resolver defect, not a split to act on.
			return PatchSplit{}, fmt.Errorf("crmagents: human-edit precedence: conflict %q is not in the patch", field)
		}
		staged[field] = value
		delete(fields, field)
	}

	split := PatchSplit{Conflicts: conflicts}
	if split.Staged, err = json.Marshal(staged); err != nil {
		return PatchSplit{}, fmt.Errorf("crmagents: human-edit precedence: %w", err)
	}
	if len(fields) == 0 {
		return split, nil
	}
	if split.Green, err = json.Marshal(fields); err != nil {
		return PatchSplit{}, fmt.Errorf("crmagents: human-edit precedence: %w", err)
	}
	return split, nil
}
