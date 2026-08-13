// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The remaining four confirm-first commands the tool schema cannot express
// (gradionhq/margince-poc-v1#928 task 5): retiring a custom field (no body
// at all), replacing a picklist's option set, and attaching or detaching a
// project stakeholder. None of them is a whole-record field patch, so none
// of them belongs in command.go's patchResolver — but two of the four
// (retire, options) target `custom_field`, a type the record seam has never
// served (command.go's servedByTheRecordSeam), while the other two target
// `project`, which it serves like any other record. routedRecordTarget
// (command.go) carries that distinction for all four rather than each
// resolver restating it.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

const (
	// customFieldRecordType names this file's first two commands' fixed
	// target. It is not a datasource.EntityType — a custom field is
	// catalog/schema metadata, never a record the seam can point at
	// (servedByTheRecordSeam stands down for it, same as six of the twelve
	// archivable types) — so it is spelled as a plain string, the same way
	// ArchiveCommand/PatchCommand carry a record type the seam may or may not
	// recognize.
	customFieldRecordType = "custom_field"
	// projectRecordType names the fixed target of this file's stakeholder
	// commands.
	projectRecordType = string(datasource.EntityProject)
)

// RetireCustomFieldCommand is one custom-field retirement, whichever door
// asked for it. It carries no body — CUSTOM-FIELDS-WIRE-4 is a bare status
// flip, no field beyond the routed id.
type RetireCustomFieldCommand struct {
	ID ids.UUID
}

// NewRetireCustomFieldCall binds one retirement to the resolver that
// answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRetireCustomFieldCall(records datasource.SystemOfRecordProvider, cmd RetireCustomFieldCommand) GovernedCall {
	return bind[RetireCustomFieldCommand](retireCustomFieldResolver{
		target: routedRecordTarget{records: records, recordType: customFieldRecordType},
	}, cmd)
}

type retireCustomFieldResolver struct {
	target routedRecordTarget
}

// Subject names the custom field by id — the seam has no row for it to read
// a better label from (routedRecordTarget.refuse stands down every time),
// the same as an archive of the six record-seam-unserved archivable types
// (command.go).
func (r retireCustomFieldResolver) Subject(_ context.Context, cmd RetireCustomFieldCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: customFieldRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Retire custom field %s", cmd.ID),
	}, nil
}

// Guards stands down: the record seam has no row for a custom field today,
// so there is nothing here to read and nothing to refuse — but the check is
// servedByTheRecordSeam itself, not a hand-restated "custom_field has no
// row" opinion, so a future seam widening (#1021) is picked up automatically
// rather than silently left blind.
func (r retireCustomFieldResolver) Guards(ctx context.Context, cmd RetireCustomFieldCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// UpdateCustomFieldOptionsCommand is one picklist option-set replacement,
// whichever door asked for it — the routed custom field id only. It does
// not carry the replacement options: neither Guards nor Subject reads them
// (the body travels separately, into diff_hash), the same reason
// UpdateFactCommand's own doc (commandsidecar.go) gives for dropping the
// corrected value.
type UpdateCustomFieldOptionsCommand struct {
	ID ids.UUID
}

// NewUpdateCustomFieldOptionsCall binds one option-set replacement to the
// resolver that answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewUpdateCustomFieldOptionsCall(records datasource.SystemOfRecordProvider, cmd UpdateCustomFieldOptionsCommand) GovernedCall {
	return bind[UpdateCustomFieldOptionsCommand](updateCustomFieldOptionsResolver{
		target: routedRecordTarget{records: records, recordType: customFieldRecordType},
	}, cmd)
}

type updateCustomFieldOptionsResolver struct {
	target routedRecordTarget
}

// Subject, Guards: the same shape as retireCustomFieldResolver's.
func (r updateCustomFieldOptionsResolver) Subject(_ context.Context, cmd UpdateCustomFieldOptionsCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: customFieldRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Update options for custom field %s", cmd.ID),
	}, nil
}

// Guards does not validate the option set: whether it is non-empty or
// applies to a picklist field is the engine's own rule (customfields.Service),
// re-checked at redemption, and restating it here would drift from it.
func (r updateCustomFieldOptionsResolver) Guards(ctx context.Context, cmd UpdateCustomFieldOptionsCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// SetStakeholderCommand is one project-stakeholder attach or re-role,
// whichever door asked for it — the routed project id only. It does not
// carry the attached person or role: neither Guards nor Subject reads them
// (setStakeholderResolver.Subject's own doc says why), the same reason
// UpdateFactCommand's own doc (commandsidecar.go) gives for dropping a
// value nothing here reads.
type SetStakeholderCommand struct {
	ID ids.UUID
}

// NewSetStakeholderCall binds one attach/re-role to the resolver that
// answers for it, reading through the record seam the project itself
// writes through.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewSetStakeholderCall(records datasource.SystemOfRecordProvider, cmd SetStakeholderCommand) GovernedCall {
	return bind[SetStakeholderCommand](setStakeholderResolver{
		target: routedRecordTarget{records: records, recordType: projectRecordType},
	}, cmd)
}

type setStakeholderResolver struct {
	target routedRecordTarget
}

// Subject names the PROJECT the approval binds to — a stakeholder edge has
// no row of its own on the seam. Unlike removeStakeholderResolver's, there
// is no path operand to carry into the summary: person_id and role arrive in
// the BODY here, and the body's own fields are what the inbox shows beside
// this line (proposed_change), the same reasoning patchResolver's own
// Subject gives for not repeating a patch's values.
func (r setStakeholderResolver) Subject(_ context.Context, cmd SetStakeholderCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: projectRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Set a stakeholder on project %s", cmd.ID),
	}, nil
}

// Guards refuses, before anything is staged, a project the caller cannot see
// or whose authority lives elsewhere — the same two refusals
// patchResolver.Guards makes for its own target. It does not check whether
// the named person exists or is already a stakeholder: those reads are the
// handler's, not this approval's.
func (r setStakeholderResolver) Guards(ctx context.Context, cmd SetStakeholderCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// RemoveStakeholderCommand is one project-stakeholder detach, whichever door
// asked for it. PersonID is a second PATH parameter, not a body field — the
// operand this whole task exists to carry.
type RemoveStakeholderCommand struct {
	ID       ids.UUID
	PersonID ids.UUID
}

// NewRemoveStakeholderCall binds one detach to the resolver that answers
// for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRemoveStakeholderCall(records datasource.SystemOfRecordProvider, cmd RemoveStakeholderCommand) GovernedCall {
	return bind[RemoveStakeholderCommand](removeStakeholderResolver{
		target: routedRecordTarget{records: records, recordType: projectRecordType},
	}, cmd)
}

type removeStakeholderResolver struct {
	target routedRecordTarget
}

// Subject names the PROJECT the approval binds to, with the person being
// detached carried into the summary: the door-agnostic line
// GovernedCall.Subject owes this operation, distinct per person, even
// though no door renders it today (confirmFactResolver's own doc says why).
func (r removeStakeholderResolver) Subject(_ context.Context, cmd RemoveStakeholderCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: projectRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Remove stakeholder %s from project %s", cmd.PersonID, cmd.ID),
	}, nil
}

// Guards: the same two refusals as setStakeholderResolver's. It does not
// check whether PersonID is currently a stakeholder — the edge's own
// existence is the handler's rule, and this approval binds to the project
// regardless of whether the edge is still there when it is redeemed.
func (r removeStakeholderResolver) Guards(ctx context.Context, cmd RemoveStakeholderCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}
