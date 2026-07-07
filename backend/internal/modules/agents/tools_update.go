// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The update_record tool: 🟢 by tier, with the per-field human-edit-
// precedence split (interfaces.md §2.1) inside its Handle — human-owned
// fields become a 🟡 staged approval, the remainder applies green in the
// same call. The partition itself is SplitHumanOwned (precedence.go),
// shared with the REST agent gate.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// --- update_record (🟢 write — human-edited fields split off as 🟡) ---

type updateRecord struct {
	p         datasource.SystemOfRecordProvider
	ownership FieldOwnership
	// staging receives the per-field precedence split's 🟡 residue. Nil is
	// the no-approvals composition: a conflicting patch is then refused
	// outright rather than staged, never applied.
	staging Approvals
}

type updateRecordArgs struct {
	RecordType string          `json:"record_type"`
	ID         ids.UUID        `json:"id"`
	Fields     json.RawMessage `json:"fields"`
	IfVersion  *int64          `json:"if_version"`
}

func (t updateRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "update_record", Version: "1.0.0",
		RequiredScope: principal.ScopeWrite,
		Tier:          mcp.TierGreen,
		OpenAPIOp:     "updatePerson/updateOrganization/updateDeal/updateLead",
		InputSchema: schema(`{"type":"object","required":["record_type","id","fields"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","lead"]},
			"id":{"type":"string","format":"uuid"},
			"fields":{"type":"object","description":"The crm.yaml update-request body; only sent fields change. Fields a human last edited are not applied: they are staged for approval and named in the result's staged_approval"},
			"if_version":{"type":"integer","description":"Optimistic-concurrency guard: the last-seen record version"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved overwriting their edit; send it with exactly the staged replay arguments"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

// stagedApprovalNote is how an update result names the part of the patch
// a human still has to release: the staged fields, the approval to watch,
// and the exact replay call that redeems it once approved (ADR-0036: the
// staged sub-patch, not the original call, is the bound diff).
type stagedApprovalNote struct {
	ApprovalID ids.ApprovalID  `json:"approval_id"`
	Fields     []string        `json:"fields"`
	Replay     json.RawMessage `json:"replay"`
	Message    string          `json:"message"`
}

// Handle is the per-field human-edit-precedence split (interfaces.md
// §2.1): fields a human last wrote are staged 🟡 for approval, the rest
// of the patch applies 🟢 in the same call — a machine does not silently
// undo a person, and a person does not block the machine's own fields.
func (t updateRecord) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args updateRecordArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if approvalRedeemed(ctx) {
		// The dispatch layer consumed an approval bound to exactly this
		// call — the human already released the overwrite it performs.
		return t.apply(ctx, args, args.Fields)
	}
	split, err := SplitHumanOwned(ctx, t.ownership, args.RecordType, args.ID, args.Fields)
	if err != nil {
		return nil, err
	}
	if len(split.Conflicts) == 0 {
		return t.apply(ctx, args, args.Fields)
	}
	if t.staging == nil {
		return nil, fmt.Errorf("fields %s were last edited by a human, and this surface has no approvals engine to stage the overwrite: %w",
			strings.Join(split.Conflicts, ", "), apperrors.ErrRequiresApproval)
	}
	if split.Green == nil {
		// Every touched field is human-owned: nothing applies, the whole
		// call is the staged change — the approved retry IS this call.
		canonical, hash, err := diffhash.Canonical(in)
		if err != nil {
			return nil, err
		}
		id, err := t.stageConflicts(ctx, args, split, canonical, hash)
		if err != nil {
			return nil, err
		}
		return nil, &StagedApprovalError{ApprovalID: id}
	}

	// Mixed patch: the green remainder lands first, then the residue is
	// staged against the post-write version — the version the approving
	// human actually sees, so the pin (ADR-0036 §2) covers this call's
	// own green half instead of being invalidated by it.
	applied, err := t.applyRecord(ctx, args, split.Green)
	if err != nil {
		return nil, err
	}
	replay, err := json.Marshal(map[string]any{
		"record_type": args.RecordType, "id": args.ID, "fields": json.RawMessage(split.Staged),
	})
	if err != nil {
		return nil, err
	}
	// Canonicalizing the replay call reproduces byte-for-byte what the
	// dispatch layer will hash when the agent re-sends it with
	// approval_id — the split sub-patch is redeemable on its own terms.
	canonical, hash, err := diffhash.Canonical(replay)
	if err != nil {
		return nil, err
	}
	id, err := t.stageConflicts(ctx, args, split, canonical, hash)
	if err != nil {
		return nil, fmt.Errorf("the other fields were updated, but staging the human-edited fields (%s) failed: %w",
			strings.Join(split.Conflicts, ", "), err)
	}
	return json.Marshal(struct {
		wireRecord
		StagedApproval stagedApprovalNote `json:"staged_approval"`
	}{
		wireRecord: applied,
		StagedApproval: stagedApprovalNote{
			ApprovalID: id,
			Fields:     split.Conflicts,
			Replay:     canonical,
			Message: fmt.Sprintf(
				"fields %s were last edited by a human and were NOT applied; staged as approval %s — once a human approves it, call update_record with exactly the replay arguments plus \"approval_id\": %q",
				strings.Join(split.Conflicts, ", "), id, id.String()),
		},
	})
}

// stageConflicts records the 🟡 residue of one split update. The read
// here runs AFTER any green remainder landed, so the pinned version
// (ADR-0036 §2) is the state the approving human will actually judge —
// this call's own green half cannot invalidate its staged half.
func (t updateRecord) stageConflicts(ctx context.Context, args updateRecordArgs, split PatchSplit, canonical json.RawMessage, hash string) (ids.ApprovalID, error) {
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.ID})
	if err != nil {
		return ids.ApprovalID{}, err
	}
	return t.staging.Stage(ctx, StageRequest{
		Tool:           "update_record",
		ProposedChange: canonical,
		DiffHash:       hash,
		TargetType:     args.RecordType,
		TargetID:       args.ID,
		TargetVersion:  &rec.Version,
		Summary: fmt.Sprintf("Update %s %s: overwrite human-edited %s",
			args.RecordType, recordLabel(rec), strings.Join(split.Conflicts, ", ")),
	})
}

// apply writes the patch and answers with the post-write record.
func (t updateRecord) apply(ctx context.Context, args updateRecordArgs, patch json.RawMessage) (json.RawMessage, error) {
	rec, err := t.applyRecord(ctx, args, patch)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rec)
}

// applyRecord writes the patch and reads the result back — the caller
// needs the post-write state (server-derived fields, bumped version)
// whether it answers with the record alone or splices staging info in.
func (t updateRecord) applyRecord(ctx context.Context, args updateRecordArgs, patch json.RawMessage) (wireRecord, error) {
	ref, err := t.p.Update(ctx, datasource.UpdateInput{
		Ref:       datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.ID},
		Patch:     patch,
		Source:    toolSource,
		IfVersion: args.IfVersion,
	})
	if err != nil {
		return wireRecord{}, err
	}
	rec, err := t.p.Read(ctx, ref)
	if err != nil {
		return wireRecord{}, fmt.Errorf("crmagents: write landed but read-back failed: %w", err)
	}
	return wireRecord{
		RecordType: string(rec.Ref.Type), ID: rec.Ref.ID, Fields: rec.Fields, Version: rec.Version,
	}, nil
}
