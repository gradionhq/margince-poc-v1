// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// How a Runtime is MINTED for each kind of invocation, which is one concept and
// not four: every constructor here builds the same callRuntime and they differ
// only in who the invocation answers as.
//
// Split from extruntime.go — which owns the Runtime's lifetime and its bound
// dependencies — because these are the choices a reader compares against each
// other. An attended tool call has a caller whose authority a core write is
// checked against; a job tick, a bus delivery and an outbound transmission have
// nobody, and each says WHY in its own words.

import (
	"context"
)

// runtimeFor mints the Runtime for one invocation of one unit's tool. ctx is
// the invocation's, not a handler's.
//
// It returns the concrete type rather than the published interface because
// the caller needs release, which is the core's side of the lifetime contract
// and deliberately not on the surface a handler holds.
func runtimeFor(ctx context.Context, unit, version, via string, deps extensionRuntimeBinding) *callRuntime {
	return &callRuntime{unit: unit, version: version, via: via, deps: deps, callCtx: ctx, live: true}
}

// jobRuntimeFor mints the Runtime for one JOB tick, which differs from an
// invocation in exactly one way: who it answers as.
//
// A tick's context carries a principal — deriveAuthority re-reads the
// dispatcher's seat at execution and binds it, because the tenant policies and
// the audit rows need an actor. That actor is an AGENT seat with no human
// behind it: its OnBehalfOf is zero and its UserID is the workspace's is_agent
// app_user, which bootstrap wrote and the dispatcher resolved. Mapping it
// through Caller's ordinary rules
// would hand a unit precisely the thing Caller.UserID promises never to be —
// "a synthetic id for the agent" rather than the person accountable for the
// row — and would contradict Runtime.Caller's promise that a tick answers the
// zero Caller. So the tick says so at construction rather than leaving Caller
// to guess it from a principal that looks, field by field, like a real agent
// call. Nothing else about the tick changes: the actor on callCtx is still the
// one every capability and every policy sees.
func jobRuntimeFor(ctx context.Context, unit, version, via string, deps extensionRuntimeBinding) *callRuntime {
	return unattendedRuntimeFor(ctx, unit, version, via, deps)
}

// deliveryRuntimeFor mints the Runtime for one BUS DELIVERY — a subscription
// hearing that something happened.
//
// It is unattended for a plainer reason than a tick's: a tick at least ran
// because a schedule the installation configured said so, while a delivery ran
// because a fact arrived. Neither has a person behind it, and this one's
// principal is the system actor the subscriber binds (see extsubscribe.go),
// which auth.Require does not check at all — so the unattended flag is what
// keeps the governed core port shut for a caller nothing else would refuse.
func deliveryRuntimeFor(ctx context.Context, unit, version, via string, deps extensionRuntimeBinding) *callRuntime {
	return unattendedRuntimeFor(ctx, unit, version, via, deps)
}

// sendRuntimeFor mints the Runtime for one outbound TRANSMISSION on a unit's
// channel — the unit's Send or its Live, invoked by the delivery dispatcher.
//
// Unattended, and the reason is worth stating because a human IS involved
// somewhere: the rep wrote the message and their seat is re-read at
// transmission (gateSeat). But they wrote it earlier, on a different request,
// and what runs now is a worker walking a retry ladder — so there is no live
// authority here for the governed core port to check a write against, and a
// unit that could reach it from a transmission would be writing records under
// an authority nobody is exercising.
func sendRuntimeFor(ctx context.Context, unit, version, via string, deps extensionRuntimeBinding) *callRuntime {
	return unattendedRuntimeFor(ctx, unit, version, via, deps)
}

// unattendedRuntimeFor is what each of those three is: a Runtime for an
// invocation with nobody behind it. They stay separate NAMES because the call
// sites are the distinct kinds of unattended work and a reader at any one of
// them should see which it is — but the behaviour is one fact, spelled here, so
// a later change to what "unattended" means cannot reach one kind and miss the
// others.
func unattendedRuntimeFor(ctx context.Context, unit, version, via string, deps extensionRuntimeBinding) *callRuntime {
	rt := runtimeFor(ctx, unit, version, via, deps)
	rt.unattended = true
	return rt
}
