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

// Bind pairs one command with the resolver that speaks its language.
func Bind[T any](resolver GovernanceResolver[T], cmd T) GovernedCall {
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

// NewArchiveResolver answers both governance questions for an archive, reading
// through the record seam the archive itself writes through.
func NewArchiveResolver(records datasource.SystemOfRecordProvider) GovernanceResolver[ArchiveCommand] {
	return archiveResolver{records: records}
}

type archiveResolver struct{ records datasource.SystemOfRecordProvider }

// Subject names the row the approval binds to.
//
// It supplies NO version pin. approvals.resolveTargetVersion takes the pin
// server-side inside the staging transaction — the one place every stager
// passes through — and discards whatever a caller passed, so a version
// computed here would be a number nothing reads.
func (a archiveResolver) Subject(ctx context.Context, cmd ArchiveCommand) (StageInfo, error) {
	info := StageInfo{
		TargetType: cmd.RecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Archive %s %s", cmd.RecordType, cmd.ID),
	}
	if !servedByTheRecordSeam(cmd.RecordType) {
		return info, nil
	}
	// The label is worth its own read: "Archive person 0195c3…" tells the
	// approver nothing about who disappears, and the approvals surface hands
	// the inbox no other human-readable name for the target.
	rec, err := a.records.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(cmd.RecordType), ID: cmd.ID})
	if err != nil {
		return StageInfo{}, err
	}
	info.Summary = fmt.Sprintf("Archive %s %s", cmd.RecordType, recordLabel(rec))
	return info, nil
}

// Guards refuses, before anything is staged, the two archives that were never
// going to run: one whose target the caller cannot see (the read answers the
// row-scope miss as not-found, which is the existence-hiding answer the caller
// would get from the archive itself), and one whose authority lives in another
// system of record.
func (a archiveResolver) Guards(ctx context.Context, cmd ArchiveCommand) error {
	if !servedByTheRecordSeam(cmd.RecordType) {
		return nil
	}
	rec, err := a.records.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(cmd.RecordType), ID: cmd.ID})
	if err != nil {
		return err
	}
	return refuseStagingElsewhere(rec)
}

// servedByTheRecordSeam reports whether the record seam speaks this type at all.
//
// Half of the twelve types the REST door can archive — lists, offers, offer
// templates, products, tags and saved views — have no row on the seam, and are
// archived by their own module's handler instead. The seam therefore has
// nothing to say about who may see them or where their authority lives, and
// asking it would answer "not served here" and turn an ordinary archive into a
// refusal. It is the archive_record TOOL's schema that is narrow, not the
// operation, so the vocabulary is read from the seam that defines it rather
// than restated here.
func servedByTheRecordSeam(recordType string) bool {
	return slices.Contains(datasource.EntityTypes(), datasource.EntityType(recordType))
}
