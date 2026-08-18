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
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dispatcherKinds answers the untenanted kinds the job-health read admits:
// every kind api/jobs.yaml declares as a dispatcher.
//
// river_job has no workspace column and so no RLS, which is why this arm
// exists at all — a dispatcher's rows carry no workspace, and the scope has
// to name them to admit them. Naming them by DERIVING the set is what keeps
// the arm honest: the contract's `role: dispatcher` also generates the
// `jobs.FleetWide` assertion each args type must satisfy, so a kind cannot
// be a dispatcher in the tree and something else here without failing to
// compile.
//
// It admits the kinds only. Which ROWS of those kinds are returned is the
// read's own untenanted test, not this list's.
func dispatcherKinds() []string {
	var kinds []string
	for kind, spec := range jobs.Declared() {
		if spec.Role == jobs.Dispatcher {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// The two substitute sentences this surface renders are jobs.UnvettedFailureReason
// and jobs.NoRecordedCause, and they live in that package rather than here.
//
// They were constants of this file, which put them out of reach of the check that
// refuses a composed failure class for claiming one of them — a class declaring
// this file's exact text would have rendered as the substitute WITH a class
// attached, indistinguishable from the real thing except for the class nobody
// should have been able to attach. The sentences a failure may not claim are one
// set, and the set belongs where the refusal can see it.

// renderedFailure is everything this surface is willing to SAY about one
// stored failure: always a sentence, and a class plus a remedy only when the
// stored text vetted.
//
// Class and Remedy are pointers and move together, because a class with no
// remedy is a label an operator cannot act on and a remedy with no class is
// advice about nothing. They are nil for the same failures — the ones this
// surface could not recognise — and the contract says so.
type renderedFailure struct {
	Reason string
	Class  *string
	Remedy *string
}

// renderFailure renders a stored failure for a human.
//
// river_job.errors holds whatever the worker returned. jobs.Fault exists so
// that is a vetted sentence — but a worker that bypassed it stored its raw
// cause, which routinely names the address or record a provider refused. So
// the column is checked, never trusted, and anything unrecognised becomes
// the same fixed substitute with NO class: a class invented for text nobody
// could vet would key an operator's alert on a guess, which is worse than
// telling them plainly that the failure is unclassified.
//
// The KIND is what makes the class resolvable at all. A composed vocabulary
// belongs to one unit's kinds, and two units may name a failure with the same
// token — so the sentence alone is ambiguous and the kind is what disambiguates
// it. Reading one kind's vocabulary for another's row would report a failure as
// something it is not.
func renderFailure(f jobs.Failure) renderedFailure {
	// Nothing recorded is a different fact from something unreadable, and the
	// two must not render alike — see jobs.NoRecordedCause. It is checked here
	// rather than left to the vetting, which cannot tell them apart either.
	if f.StoredReason == "" {
		return renderedFailure{Reason: jobs.NoRecordedCause}
	}
	if detail, ok := jobs.VettedFailure(f.Kind, f.StoredReason); ok {
		return renderedFailure{Reason: detail.Sentence, Class: &detail.Class, Remedy: &detail.Remedy}
	}
	// The fault seam's OWN unclassified sentence is vetted text that carries no
	// class, and it survives on its own terms. Fault wrote it, and it wrote the
	// log line it points at — so substituting it for the text below would trade
	// a true pointer for a vaguer one. There is still no class to assert: an
	// unclassified failure is precisely the one nobody has named yet.
	if jobs.VettedSentence(f.StoredReason) {
		return renderedFailure{Reason: f.StoredReason}
	}
	return renderedFailure{Reason: jobs.UnvettedFailureReason}
}

// jobHealthReadTimeout bounds the scoped job read. river_job has no index
// over args->>'workspace_id', so both statements scan; a read that cannot
// finish inside this is a signal, not something to wait out.
const jobHealthReadTimeout = 5 * time.Second

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

	// Bounded, for the same reason the exposition endpoint bounds its own
	// job read: this is two sequential scans of a table no index covers, and
	// an unbounded one holds a request thread and a pool connection for as
	// long as the scan takes. The budget is larger than the scrape's 2s
	// because an operator waiting on a page tolerates more latency than a
	// scrape interval does — but it is a budget, not the absence of one.
	ctx, cancel := context.WithTimeout(ctx, jobHealthReadTimeout)
	defer cancel()

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

	httperr.WriteJSON(w, http.StatusOK, jobHealthResponse(health))
}

// jobHealthResponse maps the scoped read onto the contract.
func jobHealthResponse(health jobs.Health) crmcontracts.JobHealth {
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
		rendered := renderFailure(f)
		failures = append(failures, crmcontracts.JobFailure{
			Kind:        f.Kind,
			State:       crmcontracts.JobFailureState(f.State),
			Attempt:     f.Attempt,
			MaxAttempts: f.MaxAttempts,
			FailedAt:    f.FailedAt,
			// The row's own id, so the process log this surface points at has a
			// line an operator can actually find: River logs job_id.
			JobId: &f.ID,
			// Absent stays absent. A row with no recorded attempt error has no
			// first failure, and a zero time would read as 1970.
			FirstFailedAt: f.FirstFailedAt,
			// The stored text is vetted, never forwarded.
			Reason:       rendered.Reason,
			FailureClass: rendered.Class,
			Remedy:       rendered.Remedy,
		})
	}

	return crmcontracts.JobHealth{
		GeneratedAt:    time.Now().UTC(),
		Kinds:          kinds,
		RecentFailures: failures,
	}
}

// secondsOrAbsent TRUNCATES a measured age to whole seconds, and keeps an
// absent one absent: null means nothing of this kind is runnable, which is
// a different claim from "something became runnable a moment ago".
//
// Truncation rather than rounding is deliberate and harmless here: the
// value answers "how long has this been waiting", where reporting 41s for
// 41.7s understates by less than a second and never overstates. A gauge
// that rounded up could report a job as waiting a second it has not.
func secondsOrAbsent(age *float64) *int {
	if age == nil {
		return nil
	}
	rounded := int(*age)
	return &rounded
}
