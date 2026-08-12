// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// One governed call, whichever door asked for it.
//
// The two doors used to decide independently what a call was about: the tool
// door asked the tool, and the REST door GUESSED — the route's {id} parameter
// paired with the operation's declared record type. A guess and a fact cannot
// be held in agreement by review, and these two drifted repeatedly.
//
// What the doors share here is not the tool's arguments: half the REST
// operations have no expressible tool call, and hashing a projection of a
// request while executing the raw request lets an operand drift between them.
// It is a typed COMMAND — the operation's own vocabulary, where a path operand
// is a field like any other — and one resolver over it.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// GovernanceResolver answers, for one typed command, everything the gate must
// know before it admits or stages the call it describes.
//
// Both doors resolve through the SAME resolver: the tool decodes its arguments
// into the command, the REST descriptor decodes the request into the command,
// and neither door constructs the other's wire form. That is what makes an
// obligation added here impossible to add to one door and forget on the other.
type GovernanceResolver[T any] interface {
	// Subject names the record the approval binds to, the line the human reads,
	// and (where the target has one) the version to pin.
	Subject(ctx context.Context, cmd T) (StageInfo, error)
	// Guards refuses, BEFORE anything is staged, what the executor would refuse
	// afterwards — so a human's one-shot approval is never spent on a call that
	// was never going to run.
	Guards(ctx context.Context, cmd T) error
}

// GovernedCall is a command already bound to the resolver that speaks it.
//
// A door decodes a command where its concrete type is known and hands THIS to
// everything downstream. Without the binding, a door holding a decoded command
// would need a type switch of its own to find the resolver again — a second
// table, keyed by the same operations as the first, free to disagree with it.
// That disagreement is the fault this seam exists to remove, so the seam does
// not reintroduce it one layer down.
type GovernedCall interface {
	Subject(ctx context.Context) (StageInfo, error)
	Guards(ctx context.Context) error
}

// bind pairs one command with the resolver that speaks its language.
//
// Unexported, and reached only through a family's own New…Call constructor
// below: what leaves this package is a call, never a resolver, so a resolver
// cannot be built once and reused for a second command.
//
//nolint:ireturn // the erasure IS the return type: the bound pair cannot be named without the type parameter the caller has just spent, which is the whole reason a door can carry it
func bind[T any](resolver GovernanceResolver[T], cmd T) GovernedCall {
	return boundCommand[T]{resolver: resolver, cmd: cmd}
}

type boundCommand[T any] struct {
	resolver GovernanceResolver[T]
	cmd      T
}

func (b boundCommand[T]) Subject(ctx context.Context) (StageInfo, error) {
	return b.resolver.Subject(ctx, b.cmd)
}

func (b boundCommand[T]) Guards(ctx context.Context) error {
	return b.resolver.Guards(ctx, b.cmd)
}

// StageSubject asks a bound call both questions in the order that makes them
// mean anything: the refusals first, so a call that was never going to run is
// never described to a human, and the subject after. Every door stages through
// here, which is what keeps that order from being something each door has to
// remember.
func StageSubject(ctx context.Context, call GovernedCall) (StageInfo, error) {
	if err := call.Guards(ctx); err != nil {
		return StageInfo{}, err
	}
	return call.Subject(ctx)
}

// ArchiveCommand is one archive, whichever door asked for it.
type ArchiveCommand struct {
	RecordType string
	ID         ids.UUID
}

// NewArchiveCall binds one archive to the resolver that answers for it, reading
// through the record seam the archive itself writes through.
//
// A CALL is what leaves this package, not a resolver, and that is what keeps
// the memo below safe. The remembered row was read under the calling
// principal's row scope, and the memo is keyed on the command rather than on
// who asked — so a resolver hoisted out of one call and reused in another would
// hand the second caller the first caller's read and skip the visibility check
// the second was owed. Binding at construction makes that unreachable rather
// than discouraged.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewArchiveCall(records datasource.SystemOfRecordProvider, cmd ArchiveCommand) GovernedCall {
	return bind[ArchiveCommand](&archiveResolver{records: records}, cmd)
}

type archiveResolver struct {
	records datasource.SystemOfRecordProvider
	// seen is the command rec was read for, so a resolver asked about a second
	// target reads that target rather than answering about the first.
	seen ArchiveCommand
	rec  datasource.Record
	read bool
}

// target reads the row the command names, once.
//
// Both questions below are answered from it, for two reasons. A row read twice
// can change between the readings, and the answers would then describe
// different records — an authority judgment about one, a summary about
// another. And the read is not cheap: the seam resolves the installation's
// mode before it reaches the record, so asking twice doubles the round trips a
// staging spends on one row.
//
// served is false for a record type the seam does not speak: there is no row
// then, and nothing to say about one.
func (a *archiveResolver) target(ctx context.Context, cmd ArchiveCommand) (rec datasource.Record, served bool, err error) {
	if !servedByTheRecordSeam(cmd.RecordType) {
		return datasource.Record{}, false, nil
	}
	if a.read && a.seen == cmd {
		return a.rec, true, nil
	}
	rec, err = a.records.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(cmd.RecordType), ID: cmd.ID})
	if err != nil {
		return datasource.Record{}, false, err
	}
	a.seen, a.rec, a.read = cmd, rec, true
	return rec, true, nil
}

// Subject names the row the approval binds to.
//
// It supplies NO version pin. approvals.resolveTargetVersion takes the pin
// server-side inside the staging transaction — the one place every stager
// passes through — and discards whatever a caller passed, so a version
// computed here would be a number nothing reads.
func (a *archiveResolver) Subject(ctx context.Context, cmd ArchiveCommand) (StageInfo, error) {
	info := StageInfo{
		TargetType: cmd.RecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Archive %s %s", cmd.RecordType, cmd.ID),
	}
	rec, served, err := a.target(ctx, cmd)
	if err != nil {
		return StageInfo{}, err
	}
	if !served {
		// The id is the only name this type has here.
		return info, nil
	}
	// "Archive person 0195c3…" tells the approver nothing about who
	// disappears, and the approvals surface hands the inbox no other
	// human-readable name for the target.
	info.Summary = fmt.Sprintf("Archive %s %s", cmd.RecordType, recordLabel(rec))
	return info, nil
}

// Guards refuses, before anything is staged, the two archives that were never
// going to run: one whose target the caller cannot see (the read answers the
// row-scope miss as not-found, which is the existence-hiding answer the caller
// would get from the archive itself), and one whose authority lives in another
// system of record.
func (a *archiveResolver) Guards(ctx context.Context, cmd ArchiveCommand) error {
	rec, served, err := a.target(ctx, cmd)
	if err != nil {
		return err
	}
	if !served {
		return nil
	}
	return refuseStagingElsewhere(rec)
}

// CreateCommand is one record creation, whichever door asked for it.
type CreateCommand struct {
	RecordType string
	Fields     json.RawMessage
}

// NewCreateCall binds one create to the resolver that answers for it.
//
// Unlike archive's, this resolver holds no dependency and no memo: a create
// names no ROW — the record does not exist yet — so there is nothing for
// Guards to read and nothing for Subject to describe beyond the command's own
// fields.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewCreateCall(cmd CreateCommand) GovernedCall {
	return bind[CreateCommand](createResolver{}, cmd)
}

type createResolver struct{}

// Subject names the record TYPE the approval binds to — with no id and no
// pin, because there is no row yet for either to describe. This is the shape
// a staged create already had before this seam existed (#982); the REST door
// now stages the identical shape for the same operation.
func (createResolver) Subject(_ context.Context, cmd CreateCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: cmd.RecordType,
		Summary:    describeGenericWrite("Create", cmd.RecordType, cmd.Fields),
	}, nil
}

// Guards refuses, before anything is staged, a `fields` payload naming a key
// the record type does not accept. rejectUnknownFields answers nil for a
// record type it does not know (createShapes), which is deliberately a
// stand-down and not this resolver's business to override: a type outside
// createShapes is still a real, working create over REST — its OWN module's
// handler performs it, entirely independent of create_record's write path —
// so there is nothing here for a door-agnostic Guards to refuse it FOR.
//
// createRecord.StageInfo (tools.go) refuses such a type BEFORE it ever
// reaches here, and that is deliberate placement, not an oversight: #982's
// guarantee — no approval staged for a create that could only ever die at
// the provider — is true of create_record's OWN Handle, which writes
// exclusively through datasource.SystemOfRecordProvider.Create and cannot
// express a type outside createShapes at all. That is a fact about the TOOL
// door's executor, not about the record type, so it has to be asked at the
// tool door and cannot be asked here: the same command reaching this
// resolver from REST names an operation whose OWN handler creates the type
// fine, and a resolver that cannot tell which door asked has no way to
// answer "does the executor support this" correctly for both.
func (createResolver) Guards(_ context.Context, cmd CreateCommand) error {
	return rejectUnknownFields(createShapes, cmd.RecordType, cmd.Fields)
}

// PatchCommand is one whole-record field patch, whichever door asked for it.
//
// It carries no IfVersion: the tool door's own if_version argument has no
// reader here to give it to. The pin an approval binds to is taken
// server-side inside the staging transaction (Subject's own comment below),
// so a caller-supplied version has nothing this command's Guards/Subject
// would do with it — a field with no reader is exactly what a command
// documents ITS OWN OBLIGATIONS by carrying, and this one has none to
// document.
type PatchCommand struct {
	RecordType string
	ID         ids.UUID
	Fields     json.RawMessage
}

// NewPatchCall binds one patch to the resolver that answers for it, reading
// through the record seam the patch itself writes through.
//
// No memo here, unlike archive's: Subject below reads nothing — its summary
// names the FIELDS the patch sets, not the record, so there is only ONE
// caller of a target read (Guards) and nothing for a second reading to race
// against. A memo earns its keep with a second caller; adding one here ahead
// of that caller would be exactly the abstraction T3/T8 forbid.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewPatchCall(records datasource.SystemOfRecordProvider, cmd PatchCommand) GovernedCall {
	return bind[PatchCommand](patchResolver{records: records}, cmd)
}

type patchResolver struct {
	records datasource.SystemOfRecordProvider
}

// Subject names the record TYPE and ID the approval binds to, with no pin:
// approvals.resolveTargetVersion takes the pin server-side inside the staging
// transaction, the one place every stager passes through, so a version
// computed here would be a number nothing reads. The summary names the
// FIELDS the patch sets rather than the record, matching the shape a staged
// patch already had before this seam existed (#982) — a record's values are
// the record, and the staged row carries all of them in proposed_change,
// which the inbox shows beside this line.
func (patchResolver) Subject(_ context.Context, cmd PatchCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: cmd.RecordType,
		TargetID:   cmd.ID,
		Summary:    describeGenericWrite("Update", cmd.RecordType, cmd.Fields),
	}, nil
}

// Guards refuses, before anything is staged: a `fields` payload naming a key
// the record type does not accept, a target the caller cannot see (the read
// answers the row-scope miss as not-found), and a target whose authority
// lives in another system of record.
//
// Unlike create's, there is no standalone "verb does not serve this type"
// refusal here — update_record's pre-#982 body never had one, and adding one
// keyed on the same vocabulary the seam is keyed on would make the
// servedByTheRecordSeam stand-down below unreachable: a type outside it would
// already have been refused. The seam stands down instead, exactly as
// archive's does, for the same reason: five of the twelve patchable types
// (custom_field, offer, product, saved_view, webhook_subscription) are
// patched by their own module rather than through this seam.
func (p patchResolver) Guards(ctx context.Context, cmd PatchCommand) error {
	if err := rejectUnknownFields(updateShapes, cmd.RecordType, cmd.Fields); err != nil {
		return err
	}
	if !servedByTheRecordSeam(cmd.RecordType) {
		return nil
	}
	rec, err := p.records.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(cmd.RecordType), ID: cmd.ID})
	if err != nil {
		return err
	}
	return refuseStagingElsewhere(rec)
}

// servedByTheRecordSeam reports whether the record seam speaks this type at all.
//
// Half of the twelve types the REST door can archive — lists, offers, offer
// templates, products, tags and saved views — have no row on the seam; they are
// archived by their own module's handler. Asking the seam about one answers
// "not served here", which would refuse an ordinary archive, so the guards above
// stand down for them. It is the archive_record TOOL's schema that is narrow,
// not the operation, so the vocabulary is read from the seam that defines it
// rather than restated here.
//
// Standing down is a BOUND, not a discharge, and the bound is uneven: three of
// those six — list, offer and saved_view — carry real row scope in
// approvals.targetProbes. An agent can still stage an archive of one it cannot
// see, and the human's yes is then spent on a call the handler answers 404.
// Closing it needs a visibility question this seam cannot ask through the
// record provider, which is why it is filed rather than patched here:
// gradionhq/margince-poc-v1#1021.
func servedByTheRecordSeam(recordType string) bool {
	return slices.Contains(datasource.EntityTypes(), datasource.EntityType(recordType))
}
