// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// GET /admin/job-health: what the background system is holding for THIS
// workspace, and whose work died. Phase 1 made a failed tenant pass
// durable; until this endpoint it was reachable only by psql.
//
// Authentication, response mapping and the vetted-sentence substitution
// live here. The SQL lives in platform/jobs, which owns every statement
// over river_job.

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fleetDispatchers is every args type in this package that declares
// jobs.FleetWide — the CLOSED set of kinds whose rows carry no workspace
// and are still an admin's business to see.
//
// It is a slice of the marker interface rather than a slice of strings so
// a kind that is not actually fleet-wide cannot be added by hand, and
// TestEveryFleetWideArgsTypeIsDeclaredAsADispatcherKind derives the
// expected membership from the tree rather than restating it: a new
// dispatcher that never reaches this list would otherwise be invisible on
// the surface an admin uses to notice a dispatcher is not running.
var fleetDispatchers = []jobs.FleetWide{
	AgentSchedulerArgs{},
	CaptureAutoEnrichSweepArgs{},
	CaptureClassifyArgs{},
	CaptureDigestArgs{},
	CaptureEnrichArgs{},
	CloseDateSweepArgs{},
	CounterpartyVerdictArgs{},
	EmbedDriftSweepArgs{},
	EmbedReindexArgs{},
	FollowUpReconcileArgs{},
	GmailSyncArgs{},
	GmailWatchArgs{},
	GraphEdgeReconcileArgs{},
	IdempotencyRetentionArgs{},
	LinkedInRematchArgs{},
	OrgNamePromotionArgs{},
	OverlayReconcileArgs{},
	ParticipantBackfillArgs{},
	PrivacyRetentionArgs{},
	TelegramPollSweepArgs{},
	TimeScanArgs{},
	VoiceBuildRetryArgs{},
	WebhookRetryArgs{},
}

// dispatcherKinds answers the untenanted kinds the job-health read admits.
func dispatcherKinds() []string {
	kinds := make([]string, 0, len(fleetDispatchers))
	for _, d := range fleetDispatchers {
		kinds = append(kinds, d.Kind())
	}
	return kinds
}

// unvettedFailureReason is what an unrecognised stored error becomes.
//
// It does NOT promise the diagnosis is in the process log. River writes its
// own strings into this column too, and the rescuer's ("Stuck job rescued
// by JobRescuer") means the worker's process died mid-job — so for that
// case, one of the most common to reach here, a log pointer would be an
// instruction to go read something that was never written. It says what is
// known and where to look, and no more.
const unvettedFailureReason = "the job failed for a reason this surface cannot vet; check the worker logs and the job row directly"

// noRecordedCause is what a row with no stored error at all becomes.
//
// It is NOT the unvetted substitute. A cancelled job that never ran records
// no attempt error, and telling its operator the job "failed for a reason
// this surface cannot vet" asserts a failure that did not happen and points
// at a log line nobody wrote. Nothing recorded is a different fact from
// something unreadable, and the two must not render alike.
const noRecordedCause = "this job recorded no cause; a job cancelled before it ran records none"

// reasonFor renders a stored failure for a human.
//
// river_job.errors holds whatever the worker returned. jobs.Fault exists so
// that is a vetted sentence — but a worker that bypassed it stored its raw
// cause, which routinely names the address or record a provider refused. So
// the column is checked, never trusted, and anything unrecognised becomes
// the same fixed substitute.
func reasonFor(stored string) string {
	if stored == "" {
		return noRecordedCause
	}
	if jobs.VettedSentence(stored) {
		return stored
	}
	return unvettedFailureReason
}

// jobHealthHandlers serves the admin job-health read. The pool is the only
// state, and newServer CONSTRUCTS it rather than leaving the embed's zero
// value in place — an embedded-only handler set would answer every
// authenticated request with a nil pool.
//
// There is deliberately no nil-pool branch here. A nil pool is a wiring
// mistake, not a state this endpoint can legitimately be in, and a guard
// would have to invent a status for it — 404 says the endpoint does not
// exist, which would be a lie an operator then has to disprove.
// TestTheJobHealthHandlerIsConstructedWithAPoolNotJustEmbedded is what
// holds the wiring instead.
type jobHealthHandlers struct {
	pool *pgxpool.Pool
}

// GetJobHealth reports this workspace's background-job health.
//
// Gate order, fail-closed: human-only first, then admin. The payload
// carries operational failure text and a fleet-wide view of the
// dispatchers, and an admin-minted read-scoped passport satisfies every
// object grant — so human-only is asserted here rather than inferred from
// RBAC. The generated agent policy refuses a passport at the middleware
// too; this check is the layer that does not depend on the wiring being
// right.
func (h jobHealthHandlers) GetJobHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// No principal at all is a refusal, not a server fault. The session
	// middleware answers 401 before this handler is reached on the real
	// wire — proved in the integration lane — but auth.RequireHuman reports
	// an unbound actor with an unmapped error, which httperr renders as a
	// 500. A security surface should not have a 500 as its answer to
	// "nobody asked".
	if _, ok := principal.Actor(ctx); !ok {
		httperr.Write(w, r, apperrors.ErrPermissionDenied)
		return
	}
	if err := auth.RequireHuman(ctx); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if err := auth.RequireAdmin(ctx); err != nil {
		httperr.Write(w, r, err)
		return
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		httperr.Write(w, r, apperrors.ErrPermissionDenied)
		return
	}

	// wsID.String(), not the uuid: ->> yields text, and a uuid-typed bind
	// gives pgx the uuid OID and Postgres "operator does not exist: text =
	// uuid".
	health, err := jobs.WorkspaceHealth(ctx, h.pool, wsID.String(), dispatcherKinds())
	if err != nil {
		// Never a partial 200. A page that renders half the fleet as if it
		// were the whole one is the failure this endpoint exists to end.
		slog.ErrorContext(ctx, "job health read failed", "err", err)
		httperr.Write(w, r, err)
		return
	}

	httperr.WriteJSON(w, http.StatusOK, jobHealthResponse(wsID, health))
}

// jobHealthResponse maps the scoped read onto the contract.
func jobHealthResponse(workspaceID ids.UUID, health jobs.Health) crmcontracts.JobHealth {
	kinds := make([]crmcontracts.JobKindHealth, 0, len(health.Kinds))
	for _, k := range health.Kinds {
		kinds = append(kinds, crmcontracts.JobKindHealth{
			Kind:                    k.Kind,
			Queue:                   k.Queue,
			FleetWide:               k.FleetWide,
			Waiting:                 int(k.Waiting),
			Running:                 int(k.Running),
			Retrying:                int(k.Retrying),
			Dead:                    int(k.Dead),
			OldestWaitingAgeSeconds: secondsOrAbsent(k.OldestWaitingAgeSeconds),
		})
	}

	failures := make([]crmcontracts.JobFailure, 0, len(health.Failures))
	for _, f := range health.Failures {
		failures = append(failures, crmcontracts.JobFailure{
			Kind: f.Kind,
			// Either null (a dispatcher) or the caller's own workspace: the
			// scope admits no third possibility, so the id is taken from the
			// authenticated principal rather than re-parsed out of a jsonb
			// value whose format nothing constrains.
			WorkspaceId: callerOrDispatcher(workspaceID, f.WorkspaceID),
			State:       crmcontracts.JobFailureState(f.State),
			Attempt:     f.Attempt,
			MaxAttempts: f.MaxAttempts,
			FailedAt:    f.FailedAt,
			// The stored text is vetted, never forwarded.
			Reason: reasonFor(f.StoredReason),
		})
	}

	return crmcontracts.JobHealth{
		WorkspaceId:    openapi_types.UUID(workspaceID),
		GeneratedAt:    time.Now().UTC(),
		Kinds:          kinds,
		RecentFailures: failures,
	}
}

// callerOrDispatcher answers the workspace a failure belongs to.
//
// The scoped read admits a row only when its workspace key is the caller's
// own or is null, so a present key is the caller's workspace by
// construction — which is why the id comes from the authenticated
// principal rather than from the jsonb value. That value is app-written
// with no database constraint behind it, and re-parsing it here would let
// a malformed row decide what this endpoint reports.
func callerOrDispatcher(caller ids.UUID, stored *string) *openapi_types.UUID {
	if stored == nil {
		return nil
	}
	id := openapi_types.UUID(caller)
	return &id
}

// secondsOrAbsent rounds a measured age to whole seconds, and keeps an
// absent one absent: null means nothing of this kind is runnable, which is
// a different claim from "something became runnable a moment ago".
func secondsOrAbsent(age *float64) *int {
	if age == nil {
		return nil
	}
	rounded := int(*age)
	return &rounded
}
