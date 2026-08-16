// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package pipelinetrace assembles one message's journey through the ingress
// pipeline into something a member can read.
//
// WHY IT LIVES IN COMPOSE. The ladder crosses modules: capture owns the stored
// rungs, activities owns the label and the person link, and neither may import
// the other. A module never imports a sibling, so the assembly is a compose
// orchestration that owns no entity of its own — the same shape compose/briefs
// takes.
//
// WHAT IT REFUSES TO DO. It does not re-derive any module's rule. Whether the
// attention classifier would read a message is a predicate activities owns, and
// this package asks rather than re-implements: a second copy is correct until
// the first one moves, and a trace that explains a rule wrongly is worse than
// one that says nothing.
package pipelinetrace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// Rung is one stage as a reader sees it.
type Rung struct {
	Stage       trace.Stage
	Order       int
	SubjectKind trace.SubjectKind
	Status      trace.Status
	Reason      trace.Reason

	// At is when this rung happened, for the stages that know. A derived rung
	// usually does not: the label carries no timestamp of its own that this read
	// can see, and inventing one from the activity would date the wrong event.
	At *time.Time

	// Counterparty and Subject carry whatever the caller's OWN stored row held.
	// The deployment's payload posture is applied at the WIRE (wireRung), so a
	// row written before the posture was turned off does not leak through this
	// struct — but nothing here has consulted it yet.
	Counterparty string
	Subject      string
}

// Ladder is the whole answer.
type Ladder struct {
	ActivityID      *ids.UUID
	Connector       string
	PayloadsEnabled bool
	Rungs           []Rung
}

// Assembler builds ladders. It holds the two module stores it asks, and nothing
// else: every rule it renders belongs to one of them.
type Assembler struct {
	traces     *capture.TraceStore
	activities *activities.Store
	payloads   bool
}

// NewAssembler wires the assembly. `payloads` is the deployment's
// capture.trace_payloads posture, passed in rather than read here so one
// composition root decides it for every surface.
func NewAssembler(traces *capture.TraceStore, acts *activities.Store, payloads bool) *Assembler {
	return &Assembler{traces: traces, activities: acts, payloads: payloads}
}

// ByTraceID answers for one row of the member's own capture-activity window.
//
// Owning the trace row is NOT the same as being able to read the activity it
// points at: an activity can move out of a member's row scope after their own
// message created it. The window read strips the link in exactly that case
// (hideUnreadableLinks — "returning the id would make this surface an existence
// oracle over rows the timeline itself would refuse"), and this drawer opens
// from that same row, so it must strip it too. Without the probe below the
// drawer would hand back the id the list beside it had just withheld, plus the
// derived rungs read from that activity.
func (a *Assembler) ByTraceID(ctx context.Context, id ids.UUID) (Ladder, error) {
	stored, err := a.traces.LadderByTraceID(ctx, id, a.payloads)
	if err != nil {
		return Ladder{}, err
	}
	readable, err := a.activityReadable(ctx, stored.ActivityID)
	if err != nil {
		return Ladder{}, err
	}
	if !readable && stored.ActivityID != nil {
		// The message is theirs and the activity is not. The stored rungs still
		// answer — they describe the member's own message — but nothing derived
		// from the activity may, and the id itself does not travel.
		//
		// `hidden`, not a nil id alone: an activity that exists and cannot be
		// opened is a different fact from one that never existed, and the rung
		// for the activity write has to tell them apart. Collapsing them made
		// that rung report "dropped before the write was attempted" next to a
		// tier-ladder rung reading `captured`.
		stored.ActivityID = nil
		return a.assemble(ctx, view{stored: stored, owned: true, activityHidden: true})
	}
	return a.assemble(ctx, view{stored: stored, owned: true})
}

// view is one caller's standing against one message: which of its rungs they may
// read, and whether an activity they cannot open sits behind it.
type view struct {
	stored         capture.TraceLadder
	owned          bool
	activityHidden bool
}

// activityReadable asks whether the caller may open the activity this trace
// points at. A trace with no activity is not a refusal: an internal-only drop
// never produced one.
func (a *Assembler) activityReadable(ctx context.Context, activityID *ids.UUID) (bool, error) {
	if activityID == nil {
		return false, nil
	}
	_, err := a.activities.GetActivity(ctx,
		ids.From[ids.ActivityKind](*activityID), storekit.IncludeArchived)
	if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ByActivityID answers for a message on a record page.
//
// The activity read is the gate and it is taken FIRST: GetActivity applies the
// row scope, so a caller who may not open the message never reaches the trace at
// all and gets the same not-found a missing message would give.
//
// Owning the activity is not the same as owning the capture rows. A colleague
// reading a shared record may see what the pipeline did to the RECORD; what one
// member's own connection recorded about their own mailbox stays theirs, and
// `owned` says which of the two this caller is.
func (a *Assembler) ByActivityID(ctx context.Context, id ids.UUID) (Ladder, error) {
	// IncludeArchived, not LiveOnly: an archived message is one of the honest
	// answers this surface exists to give — "the classifier skipped it because
	// it was archived" is unreadable if archiving also hides the explanation.
	if _, err := a.activities.GetActivity(ctx, ids.From[ids.ActivityKind](id), storekit.IncludeArchived); err != nil {
		return Ladder{}, err
	}
	stored, owned, err := a.storedForActivity(ctx, id)
	if err != nil {
		return Ladder{}, err
	}
	stored.ActivityID = ptr(id)
	return a.assemble(ctx, view{stored: stored, owned: owned})
}

// storedForActivity fetches the caller's own stored rungs for this message, and
// reports whether they own them.
//
// Not-found is not an error here. A colleague reading a shared record owns no
// capture rows for it, and neither does a member looking at a message older than
// the 24-hour window. Both get an empty ladder and `owned=false`, and the
// assembler reports that it cannot tell — the one answer true of both, and the
// only one that does not disclose which of the two the reader is.
func (a *Assembler) storedForActivity(ctx context.Context, id ids.UUID) (capture.TraceLadder, bool, error) {
	stored, err := a.traces.LadderByActivityID(ctx, id, a.payloads)
	if errors.Is(err, apperrors.ErrNotFound) {
		return capture.TraceLadder{PayloadsEnabled: a.payloads}, false, nil
	}
	if err != nil {
		return capture.TraceLadder{}, false, err
	}
	return stored, true, nil
}

// assemble walks the registry in order and asks each stage for its answer.
//
// The REGISTRY drives the loop, not the rows. A stage with nothing to say still
// gets a rung, because a member reading a five-rung ladder where the pipeline has
// twelve steps cannot tell which of the missing seven mattered — and the whole
// point of this surface is that a silent step is the defect.
func (a *Assembler) assemble(ctx context.Context, v view) (Ladder, error) {
	stored := v.stored
	facts, known, err := a.factsFor(ctx, stored.ActivityID)
	if err != nil {
		return Ladder{}, err
	}
	var derived *activities.PipelineFacts
	if known {
		derived = &facts
	}
	out := Ladder{
		ActivityID:      stored.ActivityID,
		Connector:       stored.Connector,
		PayloadsEnabled: stored.PayloadsEnabled,
	}
	for _, reg := range trace.Registrations() {
		out.Rungs = append(out.Rungs, a.rung(reg, v, derived))
	}
	return out, nil
}

// factsFor asks activities for the derived half, and only when there is an
// activity to ask about. An internal-only drop never produced one, so the
// derived rungs for it are not "unknown" — they are not applicable, because the
// message never reached the steps that would have run.
//
// `known` rather than a nil-with-nil-error return: "there is no activity" is an
// ordinary state on this surface, not a failure, and a caller distinguishing it
// by a nil pointer beside a nil error has to know that convention to read the
// signature correctly.
func (a *Assembler) factsFor(ctx context.Context, activityID *ids.UUID) (facts activities.PipelineFacts, known bool, err error) {
	if activityID == nil {
		return activities.PipelineFacts{}, false, nil
	}
	facts, err = a.activities.ReadPipelineFacts(ctx, *activityID)
	if err != nil {
		return activities.PipelineFacts{}, false, fmt.Errorf("pipelinetrace: reading the derived rungs: %w", err)
	}
	return facts, true, nil
}

func ptr[T any](v T) *T { return &v }
