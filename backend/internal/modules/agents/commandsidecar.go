// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The organization-sidecar commands: a fact or a profile field is not a
// column on `organization` itself but a row keyed by a SECOND path segment
// (factKey, field) naming WHICH sidecar row the call is about. The approval
// still binds to the organization — that is the row whose visibility and
// system-of-record the human's decision actually depends on — but the
// operand has to travel with the command, or two different facts on the same
// organization become one indistinguishable call the moment either is
// staged (gradionhq/margince-poc-v1#928 task 5: "their operand lives in the
// URL path").

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// organizationSidecarRecordType is every command in this file's fixed
// target: the organization the fact or profile field belongs to. There is
// nothing on the record seam a fact or profile field row could be pointed at
// on its own.
const organizationSidecarRecordType = string(datasource.EntityOrganization)

// ConfirmFactCommand is one organization fact confirmation, whichever door
// asked for it. It carries no body: PO-AC-N-3 confirmation changes no value,
// only who last agreed with the machine's extraction.
type ConfirmFactCommand struct {
	ID      ids.UUID
	FactKey string
}

// NewConfirmFactCall binds one fact confirmation to the resolver that
// answers for it, reading through the record seam the organization itself
// writes through.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewConfirmFactCall(records datasource.SystemOfRecordProvider, cmd ConfirmFactCommand) GovernedCall {
	return bind[ConfirmFactCommand](confirmFactResolver{
		target: routedRecordTarget{records: records, recordType: organizationSidecarRecordType},
	}, cmd)
}

type confirmFactResolver struct {
	target routedRecordTarget
}

// Subject names the ORGANIZATION the approval binds to — a fact has no row
// of its own on the seam — with the fact key carried into the summary: the
// door-agnostic line GovernedCall.Subject owes this operation, distinct per
// fact even though no door renders it today (REST takes its own line from
// restSummary; no tool reaches this command at all — agentgatestaging.go).
// It reads nothing: unlike archiveResolver's, there is no per-record label
// to compose (routedRecordTarget's own doc says why).
func (r confirmFactResolver) Subject(_ context.Context, cmd ConfirmFactCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: organizationSidecarRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Confirm fact %s on organization %s", cmd.FactKey, cmd.ID),
	}, nil
}

// Guards refuses, before anything is staged, an organization the caller
// cannot see or whose authority lives elsewhere — the same two refusals
// patchResolver.Guards makes for its own target. It does NOT check whether
// FactKey names an existing fact: that read is the handler's, not this
// approval's, and restating it here would be a second copy of the
// executor's own rule.
func (r confirmFactResolver) Guards(ctx context.Context, cmd ConfirmFactCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// UpdateFactCommand is one organization fact correction, whichever door
// asked for it — the routed organization id and the fact key being
// corrected. It does NOT carry the corrected value: neither Guards nor
// Subject reads one (the body travels separately, into diff_hash, and
// nothing here renders it — see Subject's own doc), so a field with no
// reader is not carried, the same call task 4's review made for
// PatchCommand.IfVersion.
type UpdateFactCommand struct {
	ID      ids.UUID
	FactKey string
}

// NewUpdateFactCall binds one fact correction to the resolver that answers
// for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewUpdateFactCall(records datasource.SystemOfRecordProvider, cmd UpdateFactCommand) GovernedCall {
	return bind[UpdateFactCommand](updateFactResolver{
		target: routedRecordTarget{records: records, recordType: organizationSidecarRecordType},
	}, cmd)
}

type updateFactResolver struct {
	target routedRecordTarget
}

// Subject, Guards: the same shape as confirmFactResolver's.
func (r updateFactResolver) Subject(_ context.Context, cmd UpdateFactCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: organizationSidecarRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Update fact %s on organization %s", cmd.FactKey, cmd.ID),
	}, nil
}

func (r updateFactResolver) Guards(ctx context.Context, cmd UpdateFactCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// ConfirmProfileFieldCommand is one organization profile-field confirmation,
// whichever door asked for it — Field is the closed-vocabulary profile-field
// key (`display_name`, `icp`, …), never a fact key.
type ConfirmProfileFieldCommand struct {
	ID    ids.UUID
	Field string
}

// NewConfirmProfileFieldCall binds one profile-field confirmation to the
// resolver that answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewConfirmProfileFieldCall(records datasource.SystemOfRecordProvider, cmd ConfirmProfileFieldCommand) GovernedCall {
	return bind[ConfirmProfileFieldCommand](confirmProfileFieldResolver{
		target: routedRecordTarget{records: records, recordType: organizationSidecarRecordType},
	}, cmd)
}

type confirmProfileFieldResolver struct {
	target routedRecordTarget
}

// Subject, Guards: the same shape as confirmFactResolver's, naming Field
// instead of FactKey as the summary's operand.
func (r confirmProfileFieldResolver) Subject(_ context.Context, cmd ConfirmProfileFieldCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: organizationSidecarRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Confirm profile field %s on organization %s", cmd.Field, cmd.ID),
	}, nil
}

func (r confirmProfileFieldResolver) Guards(ctx context.Context, cmd ConfirmProfileFieldCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// UpdateProfileFieldCommand is one organization profile-field correction,
// whichever door asked for it — the routed organization id and the field
// being corrected. It does not carry the corrected value, the same reason
// UpdateFactCommand's own doc gives.
type UpdateProfileFieldCommand struct {
	ID    ids.UUID
	Field string
}

// NewUpdateProfileFieldCall binds one profile-field correction to the
// resolver that answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewUpdateProfileFieldCall(records datasource.SystemOfRecordProvider, cmd UpdateProfileFieldCommand) GovernedCall {
	return bind[UpdateProfileFieldCommand](updateProfileFieldResolver{
		target: routedRecordTarget{records: records, recordType: organizationSidecarRecordType},
	}, cmd)
}

type updateProfileFieldResolver struct {
	target routedRecordTarget
}

func (r updateProfileFieldResolver) Subject(_ context.Context, cmd UpdateProfileFieldCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: organizationSidecarRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Update profile field %s on organization %s", cmd.Field, cmd.ID),
	}, nil
}

func (r updateProfileFieldResolver) Guards(ctx context.Context, cmd UpdateProfileFieldCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}
