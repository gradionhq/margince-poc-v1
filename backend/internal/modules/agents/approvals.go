// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The 🟡 loop from the tool surface's side. A refused confirm-first call
// is STAGED (approval.requested) so the human sees exactly what the agent
// wanted; after the human approves, the agent re-invokes the IDENTICAL
// call plus `approval_id`, and redemption checks tool + diff_hash +
// passport + target version before consuming the staging once. The agent
// never receives a bearer secret — the approval row itself is the
// authority object, and it only fits the caller it was staged by.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Approvals is the staging/redemption dependency, implemented by the
// approvals module and injected at the composition root (this package
// depends on seams, never on sibling modules).
type Approvals interface {
	// StageCall answers the live authority object for one refused 🟡 call,
	// staging one only when the call does not already have one — and reports
	// whether a human has ALREADY approved it.
	//
	// The two facts are one answer because a caller needs both to say anything
	// true to the agent: an id alone cannot distinguish "wait for a human" from
	// "spend what you are holding", and an agent told to wait for a decision it
	// already has asks the question again — which is how one act comes to hold
	// several approvals, each answered separately and none of them spent.
	StageCall(ctx context.Context, in StageRequest) (id ids.ApprovalID, alreadyApproved bool, err error)
	// StageQuotaRelease puts a §2.4 step-up in front of the human who lent this
	// passport, and reports whether anything was staged: a question that human
	// has already REJECTED is not re-asked, and the refusal then stands alone.
	//
	// It is a second method rather than a field on StageRequest because the two
	// stage different things. Stage carries a proposed CHANGE against a target
	// record; this carries no change and no target — it is a question about a
	// credential — and one shared shape would leave every caller of the common
	// method passing empty halves that have no meaning for it.
	StageQuotaRelease(ctx context.Context, in QuotaReleaseRequest) (id ids.ApprovalID, staged bool, err error)
	// Redeem answers the version the approval was pinned to, so a transport
	// that forwards the authorized call can bind its own write to it. pinned
	// is false when the approval carried none — a create, or a target type
	// with no version column — and version is meaningless then.
	Redeem(ctx context.Context, approvalID ids.ApprovalID, tool, diffHash string) (version int64, pinned bool, err error)
}

// StageRequest carries what the inbox shows the human and what redemption
// later re-checks.
type StageRequest struct {
	Tool           string
	ProposedChange json.RawMessage
	DiffHash       string
	TargetType     string
	TargetID       ids.UUID
	TargetVersion  *int64
	Summary        string
}

// StageInfo is what a 🟡-capable tool contributes to its own staging: the
// row the effect targets (for the version re-check) and the one-liner the
// inbox displays.
//
// TargetVersion reaches no staged row in production, on either door. It travels
// as far as StageRequest and stops there: the approvals engine resolves the pin
// itself, inside the staging transaction, discarding whatever a caller offered
// (approvals.insertProposalInTx). So a resolver's answer here documents the
// version its own read saw, and is not the pin an approval is later redeemed
// against — a difference worth stating, since a field named TargetVersion on
// the staging seam reads like the thing that binds the redemption.
type StageInfo struct {
	TargetType    string
	TargetID      ids.UUID
	TargetVersion *int64
	Summary       string
}

// stageableTool is implemented by tools whose refused 🟡 calls should
// land in the inbox rather than dead-end.
type stageableTool interface {
	StageInfo(ctx context.Context, args json.RawMessage) (StageInfo, error)
}

// approvalRedeemedKey marks a context whose call already consumed a
// redeemed approval at the dispatch layer: the handler may perform the
// exact effect the human released without re-asking the precedence
// question (the diff_hash binding guarantees the call IS that effect).
type approvalRedeemedKey struct{}

// releasedPinKey carries the version the approval was granted against, so the
// write the release performs can re-check it inside its OWN transaction.
type releasedPinKey struct{}

// withApprovalRedeemed marks ctx as carrying a released approval. Set only by
// RedeemAndMark, which cannot mark without a successful Redeem.
func withApprovalRedeemed(ctx context.Context, version int64, pinned bool) context.Context {
	ctx = context.WithValue(ctx, approvalRedeemedKey{}, true)
	if pinned {
		ctx = context.WithValue(ctx, releasedPinKey{}, version)
	}
	return ctx
}

// RedeemAndMark consumes an approval and returns a context marked as released,
// binding the two together so neither can happen without the other. The
// version pin travels back for a transport that must forward it as its own
// precondition; pinned is false when the approval carried none.
//
// Outside this package this is the ONLY way to obtain a released context: the
// marker is proof that a human released exactly THIS call, so only the
// redemption path may set it. There are two dispatch layers — the MCP registry
// and the REST agent gate — and both must mark what they redeem, or the gate
// refuses the very write the approval was granted for; making the marking a
// consequence of redeeming is what keeps that true without trusting either
// caller to remember.
func RedeemAndMark(ctx context.Context, approvals Approvals, approvalID ids.ApprovalID, tool, diffHash string,
) (marked context.Context, version int64, pinned bool, err error) {
	version, pinned, err = approvals.Redeem(ctx, approvalID, tool, diffHash)
	if err != nil {
		return ctx, 0, false, err
	}
	return withApprovalRedeemed(ctx, version, pinned), version, pinned, nil
}

// ApprovalRedeemed reports whether this call already consumed a redeemed
// approval. Exported because the composition layer needs the same answer at
// the datasource seam, where a write into an external system of record is
// refused unless a human released this exact call. Read-only by design: the
// marker is set only by RedeemAndMark.
func ApprovalRedeemed(ctx context.Context) bool {
	redeemed, ok := ctx.Value(approvalRedeemedKey{}).(bool)
	return ok && redeemed
}

// pinForWrite answers the version a write must be conditioned on, from the
// three places a version is established — in the order of who established it.
//
// It answers for the tools that CALL it — the two deal moves and the project
// phase advance — and not for every write on the surface: update_record passes
// the caller's if_version straight through, so a redeemed retry there is not
// carried by the released pin the way the REST door's If-Match forward carries
// it. That asymmetry predates this function and is filed, not implied away.
//
// The CALLER's pin wins when it supplied one: it is bound into the diff_hash the
// redemption verified, so it cannot disagree with what the human approved. It
// may not disagree with what the GATE read either, and that is CHECKED rather
// than assumed. The caller controls this argument, so a version the gate never
// saw is a version nothing proved — and a caller naming the version the racing
// close will PRODUCE walks straight through the guard, because the store's
// compare then passes on precisely the record the tier decision does not
// describe. A disagreement answers skew rather than being silently overridden,
// so a caller holding a stale version still learns that it is stale.
//
// The RELEASED pin comes next. Redemption commits its OWN transaction and the
// handler then opens a fresh one, so the skew check inside the redemption proves
// the row was at the approved version when the approval was consumed — not that
// it still is when the effect lands, and the agent controls both sides of that
// window (its own 🟢 update_record can commit in between). Carrying the pin into
// the write moves the version compare inside the transaction that actually
// mutates.
//
// The ADMITTED pin closes the same window on the auto-execute path, where there
// is no approval to carry one. A dynamic tier is resolved by READING the record
// — a deal move runs unattended only when the gate can prove BOTH endpoints of
// the move open — and that read commits before the write just as a redemption
// does. Without it a close landing in the window reopens a won deal at the 🟢
// tier: the same race, one tier down.
//
// The REST gate does the same thing by forwarding the pin as If-Match
// (compose/agentgate.go); this is that fix on the MCP transport.
//
//nolint:nilnil // no pin IS an answer here: a write with nothing to condition it on is the ordinary case for a static tier and an unapproved call, and a sentinel for it would make every call site branch on a condition none of them act on
func pinForWrite(ctx context.Context, callerPin *int64) (*int64, error) {
	admitted, gateRead := auth.AutoExecutePin(ctx)
	if callerPin != nil {
		if gateRead && *callerPin != admitted {
			return nil, fmt.Errorf(
				"if_version %d is not the version this record was read at (%d) — re-read it and retry: %w",
				*callerPin, admitted, apperrors.ErrVersionSkew)
		}
		return callerPin, nil
	}
	if released, pinned := ctx.Value(releasedPinKey{}).(int64); pinned {
		return &released, nil
	}
	if gateRead {
		return &admitted, nil
	}
	return nil, nil
}

// archiveAt performs one archive conditioned on the version the caller's
// authority was granted against.
//
// The pin comes from pinForWrite, exactly as every other 🟡 write's does — the
// released approval's version on an approved retry, the admitted read's on an
// auto-executed call, and nothing at all on an ordinary one. Until the seam
// grew a field to carry it, the archive was the one 🟡 write that took the pin
// and dropped it: redemption verified the record was at version 4 and
// committed, and the archive then ran in a LATER transaction with no version
// clause, so a concurrent update in that window landed the archive on a
// version nobody had approved.
//
// A provider that does not answer RecordArchiverV2 and a caller who HAS a pin
// is the one case that refuses. Falling back to the unconditioned verb there
// would spend the approval on precisely the write it was granted against
// something else — quietly, and only on the installations whose adapter
// happens not to answer the newer seam.
func archiveAt(ctx context.Context, p datasource.SystemOfRecordProvider, ref datasource.EntityRef) (datasource.EntityRef, error) {
	pin, err := pinForWrite(ctx, nil)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	archiver, ok := p.(datasource.RecordArchiverV2)
	if !ok {
		if pin != nil {
			return datasource.EntityRef{}, fmt.Errorf(
				"this workspace's system of record cannot archive a %s at a named version, so an archive "+
					"approved against version %d cannot be carried out as approved: %w",
				ref.Type, *pin, apperrors.ErrUnsupportedBySoR)
		}
		return p.Archive(ctx, ref)
	}
	return archiver.ArchiveAt(ctx, datasource.ArchiveInput{Ref: ref, IfVersion: pin})
}

// refuseArchiveHere asks the ROUTED executor to answer, before anything is
// staged, every refusal the archive itself would answer with.
//
// It is the seam's half of GovernanceResolver.Guards' own promise — "refuses,
// BEFORE anything is staged, what the executor would refuse afterwards" — and
// that promise was kept for exactly two of the executor's refusals (unreadable,
// held elsewhere) and broken for the rest. Every archive store also requires
// WRITE authority over the row, which is a narrower question than reading it:
// a rep who may read a colleague's record staged an archive, a human released
// it, and the store refused the retry. The approval was spent either way.
//
// A provider that does not answer RecordArchiverV2 is refused HERE, and that is
// the whole point rather than an inconvenience.
//
// archive_record is statically TierConfirmationRequired, so every archive
// through this seam is staged and every redemption carries the released pin —
// which archiveAt refuses such a provider for, because carrying it out
// unpinned would be the approval granted against one version and spent on
// another. Standing down here and refusing there would put that refusal AFTER
// a human answered, on every archive, which is the exact defect this file
// exists to remove. The staging is the place to say it.
func refuseArchiveHere(ctx context.Context, p datasource.SystemOfRecordProvider, ref datasource.EntityRef) error {
	archiver, ok := p.(datasource.RecordArchiverV2)
	if !ok {
		return fmt.Errorf(
			"this workspace's system of record cannot archive a %s at a named version, and an approved "+
				"archive names one — so no approval of this could be carried out as approved: %w",
			ref.Type, apperrors.ErrUnsupportedBySoR)
	}
	return archiver.RefuseArchive(ctx, ref)
}

// refuseStagingElsewhere refuses to stage a change whose target's authority
// lives in another system of record.
//
// A staged approval is an authority object a human must be able to both SEE
// and RELEASE, and for such a target neither holds: the decidability probe and
// the redemption version pin both read our own tables, which the record has no
// row in. Staging anyway puts a decision in an inbox that can never take
// effect and cannot be withdrawn — the zombie authority object the REST gate's
// own decision-grant check refuses to mint. Answering the declared
// unsupported-by-SoR sentinel instead makes the 🟡 tools agree with the
// datasource seam, which refuses the same write for the same reason. That
// agreement is only true while EVERY stageable tool calls this, so the set is
// derived rather than trusted: stagingfitness_test.go walks the registry and
// fails on a stageable tool that does not refuse.
//
// rec must be the record the change targets, read through the seam.
func refuseStagingElsewhere(rec datasource.Record) error {
	if rec.Freshness.Authoritative {
		return nil
	}
	return fmt.Errorf(
		"this %s is held in an external system of record, so an approval for it could never be released: %w",
		rec.Ref.Type, apperrors.ErrUnsupportedBySoR)
}

// The `approval_id` argument is popped in reserved.go, with the surface's
// other reserved member and in the same reading of the caller's bytes. The
// diff_hash the redemption above checks is taken over what remains, which is
// why an approval binds to the CALL and not to the transport bookkeeping
// wrapped around it.
