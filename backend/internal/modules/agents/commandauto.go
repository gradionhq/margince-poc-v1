// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The four auto-execute commands (gradionhq/margince-poc-v1#928 task 7):
// logging an activity, drafting a reply, re-associating an activity, and
// running a report. All four are 🟢 today, so nothing stages them and neither
// question below is reached on today's tiers — the same standing the seven
// nested commands (commandnested.go) have.
//
// They are registered anyway, for that file's reason: a route with no command
// of its own falls back to stagedTargetByRoute's GUESS the moment a tier floor
// (#982) tightens it, and the guess is only ever as good as the route's shape.
// It happens to be right for three of these and empty for the fourth
// (runReport's route carries a `{report}` key, not an `{id}`), which is
// exactly the kind of accident this seam replaces with an answer the operation
// states itself.
//
// What does NOT happen here is the other half of the seam. Their TOOLS gain no
// StageInfo: a 🟢 tool has no staging path to move a guard off, so there is no
// second answer for the resolver to displace — and giving one a StageInfo
// would change what Registry.Stageable reports about the verb, which is what
// the contract's per-record-type tier floor consults before it may tighten it.
// That is a tiering decision, not a seam one, and it is not this task's to
// take.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// LogActivityCommand is one logged activity, whichever door asked for it. The
// body IS the activity's fields — the route names no record, because the
// activity does not exist yet.
type LogActivityCommand struct {
	Fields json.RawMessage
}

// NewLogActivityCall binds one logged activity to the resolver that answers
// for it. Like createResolver's, it holds no dependency: a create names no ROW,
// so there is nothing for Guards to read and nothing for Subject to describe
// beyond the command's own fields.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewLogActivityCall(cmd LogActivityCommand) GovernedCall {
	return bind[LogActivityCommand](logActivityResolver{}, cmd)
}

type logActivityResolver struct{}

// Subject names the record TYPE with no id and no pin — the shape every create
// stages (createResolver, command.go), because there is no row yet for either
// to describe.
func (logActivityResolver) Subject(_ context.Context, cmd LogActivityCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: string(datasource.EntityActivity),
		Summary:    describeGenericWrite("Log", string(datasource.EntityActivity), cmd.Fields),
	}, nil
}

// Guards stands down. It does not validate the fields against a shape the way
// createResolver.Guards does: log_activity's body IS crm.yaml's
// CreateActivityRequest, which the provider re-validates strictly at execution,
// and this verb never went through createShapes at either door — restating that
// vocabulary here would be a second, drifting answer to a question the store
// already answers.
func (logActivityResolver) Guards(_ context.Context, _ LogActivityCommand) error {
	return nil
}

// DraftEmailCommand is one drafted reply, whichever door asked for it — the
// routed activity is the thread being answered. It does not carry the intent:
// neither question below reads it.
type DraftEmailCommand struct {
	ActivityID ids.UUID
}

// NewDraftEmailCall binds one draft to the resolver that answers for it,
// reading the anchor through the record seam.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewDraftEmailCall(records datasource.SystemOfRecordProvider, cmd DraftEmailCommand) GovernedCall {
	return bind[DraftEmailCommand](&draftEmailResolver{
		anchor: anchoredRecord{records: records, entityType: datasource.EntityActivity},
	}, cmd)
}

type draftEmailResolver struct {
	anchor anchoredRecord
}

// Subject names the ANCHOR the draft answers, and pins its version: a draft is
// composed FROM that thread's content, so an approval given for drafting
// against the thread as it stands should not survive the thread changing.
func (r *draftEmailResolver) Subject(ctx context.Context, cmd DraftEmailCommand) (StageInfo, error) {
	rec, err := r.anchor.row(ctx, cmd.ActivityID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityActivity),
		TargetID:      cmd.ActivityID,
		TargetVersion: &rec.Version,
		Summary:       fmt.Sprintf("Draft a reply to activity %s", cmd.ActivityID),
	}, nil
}

// Guards refuses an anchor the caller cannot see or whose authority lives
// elsewhere — the same two refusals patchResolver.Guards makes for its own
// target.
func (r *draftEmailResolver) Guards(ctx context.Context, cmd DraftEmailCommand) error {
	return r.anchor.refuse(ctx, cmd.ActivityID)
}

// RelinkActivityCommand is one activity re-association, whichever door asked
// for it: the routed activity and the record it is being linked to. It does
// not carry replace_existing_of_type — whether the link moves or is added
// alongside is the executor's business, and neither question below reads it.
type RelinkActivityCommand struct {
	ActivityID ids.UUID
	EntityType string
	EntityID   ids.UUID
}

// NewRelinkActivityCall binds one re-association to the resolver that answers
// for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRelinkActivityCall(records datasource.SystemOfRecordProvider, cmd RelinkActivityCommand) GovernedCall {
	return bind[RelinkActivityCommand](&relinkActivityResolver{
		activity: anchoredRecord{records: records, entityType: datasource.EntityActivity},
	}, cmd)
}

type relinkActivityResolver struct {
	activity anchoredRecord
}

// Subject names the ACTIVITY the approval binds to — the row that changes —
// and pins its version, with the destination carried into the summary: moving
// a captured email onto THIS deal and onto that one are different decisions
// wearing one shape.
func (r *relinkActivityResolver) Subject(ctx context.Context, cmd RelinkActivityCommand) (StageInfo, error) {
	rec, err := r.activity.row(ctx, cmd.ActivityID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityActivity),
		TargetID:      cmd.ActivityID,
		TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Re-associate activity %s to %s %s",
			cmd.ActivityID, cmd.EntityType, cmd.EntityID),
	}, nil
}

// Guards refuses a destination type that is not a link target — the same
// vocabulary the store refuses on, asked before a human is — and then the
// activity itself, the same two ways patchResolver.Guards refuses its own
// target.
//
// It does not read the DESTINATION record. The store refuses one the caller
// cannot see, at execution, and closing that here would mean a second read
// against a type this resolver is not built around; it is the same bound
// readStageableLinks closes for the verbs that NAME their records, and this
// one is filed rather than implied away: gradionhq/margince-poc-v1#1021 is
// where a target's visibility question gets its home.
func (r *relinkActivityResolver) Guards(ctx context.Context, cmd RelinkActivityCommand) error {
	if !relinkTargets[cmd.EntityType] {
		return &BadArgsError{Cause: fmt.Errorf("entity_type %q is not a link target", cmd.EntityType)}
	}
	return r.activity.refuse(ctx, cmd.ActivityID)
}

// RunReportCommand is one report run, whichever door asked for it. The report
// KEY is the whole of it: the plan arguments narrow what is counted, and the
// engine — not this seam — owns which of them a report accepts.
type RunReportCommand struct {
	Report string
}

// NewRunReportCall binds one report run to the resolver that answers for it.
// It holds no dependency: a report names no record at all.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRunReportCall(cmd RunReportCommand) GovernedCall {
	return bind[RunReportCommand](runReportResolver{}, cmd)
}

type runReportResolver struct{}

// Subject names NO record, and that is the honest answer rather than a gap: a
// report is an aggregate over rows the caller's own scope already bounds, so
// there is no row an approval could bind to, pin, or be probed against. What
// it does supply is the KEY — the one thing that says which aggregate is being
// released — where the route walk it replaces could only offer an empty target
// with no name attached.
func (runReportResolver) Subject(_ context.Context, cmd RunReportCommand) (StageInfo, error) {
	return StageInfo{Summary: fmt.Sprintf("Run report %s", cmd.Report)}, nil
}

// Guards stands down: the report key's vocabulary is the engine's catalog,
// which this module is handed rather than owns (ReportRunner, tools_report.go),
// and a key outside it is refused by the engine at execution with the catalog
// in hand. Restating it here would be a second answer that drifts the moment an
// installation's catalog does.
func (runReportResolver) Guards(_ context.Context, _ RunReportCommand) error {
	return nil
}
