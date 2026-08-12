// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// A tier the CONTRACT tightens for ONE record type, made visible to the tool door.
//
// ADR-0026's tighten-only floor is declared per contract OPERATION — `createProject`
// is confirm-first where `createPerson` is not — while a tool's tier is declared per
// VERB. The REST door resolves the operation and sees the tightening; this door
// resolves `create_record` and could not, so the write a route staged for a human
// ran unattended through the verb that performs it (#982).
//
// The floor asks the question the contract already answers: for THIS call's
// (verb, record_type), what is the tightest tier any operation declares? The answer
// is derived from the generated policy table at the composition root, so the
// contract stays the one source and a new annotation cannot bind one door only.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// TierFloor answers the tier the contract declares for this verb against this
// record type, and whether it declares one at all.
type TierFloor func(tool, recordType string) (mcp.RiskTier, bool)

// WithTierFloor injects the contract's per-record-type tier declarations. A
// registry composed without one admits every call at its verb's declared tier,
// which is the pre-#982 behaviour; the composed api surface always passes one,
// and the integration lane is what proves that wiring rather than this comment.
func WithTierFloor(floor TierFloor) RegistryOption {
	return func(r *Registry) { r.tierFloor = floor }
}

// recordTypedTool is a tool whose arguments NAME the record type it acts on, so a
// tier tightened for one type can be resolved for a CALL rather than for a verb.
// A tool that names no record type has nothing to key on and keeps its declared
// tier — which is correct, because a tool serving one record type declares that
// type's tier already.
//
// RecordTypeOf answers a bare string rather than a string and an error: an
// unreadable argument object is not this seam's to report. Returning "" leaves the
// declared tier in place and lets requireDeclaredArgs and the handler's own strict
// decode produce the message that names the field, rather than two surfaces
// answering for one mistake.
//
// ServesRecordType is the other half, and it is what keeps the floor from making
// things worse: a verb that cannot carry out the effect for a record type must not
// be tightened for it, because tightening converts an immediate refusal into a
// staged approval a human releases onto a call that was never going to run.
type recordTypedTool interface {
	RecordTypeOf(args json.RawMessage) string
	ServesRecordType(recordType string) bool
}

// recordTypeArg reads the `record_type` argument the generic verbs share.
func recordTypeArg(args json.RawMessage) string {
	var a struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return ""
	}
	return a.RecordType
}

// tightened applies the contract's floor to the spec this call is admitted
// against.
//
// It can only TIGHTEN, never loosen — the same one-way rule compose.operationSpec
// applies on the REST door (A34/ADR-0026), so a verb declared confirm-first stays
// confirm-first whatever an operation says, and a floor that is not confirm-first
// changes nothing. Clearing TierResolver with the tier is deliberate and matches
// the REST door: a resolver that could answer 🟢 must not survive a decision to
// stage.
func (r *Registry) tightened(t mcp.Tool, spec mcp.ToolSpec, args json.RawMessage) mcp.ToolSpec {
	if r.tierFloor == nil {
		return spec
	}
	typed, ok := t.(recordTypedTool)
	if !ok {
		return spec
	}
	recordType := typed.RecordTypeOf(args)
	if recordType == "" {
		return spec
	}
	// A type this verb cannot carry out is left at its declared tier, so the call
	// meets the provider's refusal directly. Tightening it would stage an approval
	// a human releases onto a call that then dies at the provider with the
	// one-shot authority already spent — strictly worse than refusing now.
	if !typed.ServesRecordType(recordType) {
		return spec
	}
	floor, declared := r.tierFloor(spec.Name, recordType)
	if !declared || floor != mcp.TierConfirmationRequired {
		return spec
	}
	spec.Tier, spec.TierResolver = mcp.TierConfirmationRequired, nil
	return spec
}

// summaryFieldLimit bounds how many field names a staged generic write
// enumerates, so one very wide patch cannot fill an inbox line.
const summaryFieldLimit = 8

// describeGenericWrite is the one line the inbox shows for a create or an update
// staged through a generic verb — the act, the record type, and which fields the
// call sets, sorted so two renderings of one call read alike.
//
// It names the fields rather than their values. The sibling 🟡 tools render
// values because their arguments ARE the effect a human weighs — who a mail
// reaches, when a meeting lands. A record patch's values are the record, and the
// staged row carries the whole of them in proposed_change, which the inbox shows
// beside this line; repeating them here would truncate exactly the long text a
// reader would then have to open proposed_change to see anyway.
func describeGenericWrite(act, recordType string, fields json.RawMessage) string {
	head := fmt.Sprintf("%s a %s", act, recordType)
	var patch map[string]json.RawMessage
	if json.Unmarshal(fields, &patch) != nil || len(patch) == 0 {
		return head
	}
	names := make([]string, 0, len(patch))
	for name := range patch {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > summaryFieldLimit {
		names = append(names[:summaryFieldLimit],
			fmt.Sprintf("+%d more", len(names)-summaryFieldLimit))
	}
	return head + ", setting " + strings.Join(names, ", ")
}
