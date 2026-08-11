// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// JobHandler runs ONE tick of a scheduled extension job, for ONE workspace.
//
// It is the BEHAVIOR half of a job, exactly as ToolHandler is the behavior half
// of a governed tool, and it is not derivable from the AST — so the manifest
// generator skips it and a job declared without one is inert: the contract still
// declares the kinds, the operator manifest still records the request, and
// nothing runs.
//
// rt arrives HERE, at invocation, for the same reason a tool's does: a
// declaration is inert data, so a Job value sitting in a slice holds no route
// into the core. The core mints rt for this one tick and releases it when the
// handler returns.
//
// The ctx a handler is given ALREADY carries the workspace this tick is for —
// the runner pins it from the child row's own args before the handler is
// entered. That matters more here than it does for a tool: a tool call arrives
// on a request whose tenant was resolved by authentication, while a job has no
// caller at all, so if the runner did not pin it nothing later would. Every
// capability on rt re-derives the tenant from this context, so a handler that
// passes down a context of its own keeps its deadline and loses nothing else.
//
// A tick returns an error to FAIL the attempt. There is no result: nobody is
// waiting for one.
type JobHandler func(ctx context.Context, rt Runtime) error

// Job is the BEHAVIOR half of a scheduled extension job: the job's name, and
// the Go function one workspace's tick runs. Nothing else — no cadence, no
// timeout, no queue, no attempt cap. Those are MECHANICS, they live in the
// unit's api/jobs.yaml fragment, and they reach the process as a
// JobDeclaration. The same split Tool and Verb already draw, for the same
// reason: the contract is the surface, and this struct is the only part of it
// a static document cannot hold.
type Job struct {
	// Name is the job name, lower snake_case, and must equal the `job:` of one
	// of the declaring unit's jobs.yaml kind pairs. It is NOT the River kind —
	// see JobDeclaration.DispatcherKind.
	Name string
	// Handle is the job's behavior. A nil Handle is the same as no entry at
	// all: the contract still declares the kinds and nothing works them, which
	// is why a unit with no Go behavior writes no Jobs entry rather than an
	// entry holding nil.
	Handle JobHandler
}

// Validate enforces the name grammar and nothing more, for the reason
// Tool.Validate does: every other rule a job is subject to is a rule about its
// DECLARATION, and the declaration is the contract's.
func (j Job) Validate() error {
	if !toolNameGrammar.MatchString(j.Name) {
		return fmt.Errorf("job name %q is not a valid job name (lower snake_case, e.g. refresh_quotes)", j.Name)
	}
	return nil
}

// JobDeclaration is ONE scheduled job as the unit's api/jobs.yaml fragment
// declares it, read back out of the MERGED contract by gen-composition and
// re-emitted into the generated composition as a LITERAL — the same path a
// Verb takes, and for the same load-bearing reason: the boot refusals below
// read Tier and RequestedScope, and they must keep refusing inside a bare role
// binary that ships no repository on disk.
//
// A scheduled job is TWO River kinds, not one, and the split is the
// contract's rather than this type's invention: gen-jobs' validateCadence
// forbids a cadence on a workspace-role kind, because a kind that both ticks
// and carries a tenant would have no honest answer for whose data the tick
// touched. So a unit declares a cadenced DISPATCHER that enumerates the fleet
// and a workspace CHILD that does the work, and this value carries both halves.
//
// There is deliberately no Route/ServedPath-style split here. A Verb needs one
// because the contract spells a path relative to a `servers` url the server has
// to put back; a kind string has no such base — what api/jobs.yaml writes is
// verbatim what River persists in river_job.kind — so a second spelling would
// be a second fact that could disagree with the first.
type JobDeclaration struct {
	// Unit is the declaring extension, derived from the kind's own ext_<ns>_
	// namespace rather than from anything the fragment says, so a fragment
	// cannot declare jobs in another unit's name.
	Unit Name
	// Job is the job name the unit's Jobs entry joins on.
	Job string
	// Queue is the River queue both kinds land on. It must name an entry in
	// the merged contract's `queues:` block, which today is the CORE queue set
	// — `queues` is not a container a fragment may extend, so an extension job
	// rides a pool the installation already declared rather than allocating
	// one. See the composer's ownership rule.
	Queue string
	// Cadence is the dispatcher's tick interval, the contract's `every:`.
	Cadence time.Duration
	// DispatcherTimeout and Timeout are the two kinds' wall clocks. Two
	// numbers because they bound two different things — enumerating the fleet
	// and running one tenant's tick — and River applies a timeout per kind.
	DispatcherTimeout time.Duration
	Timeout           time.Duration
	// MaxAttempts is the child's attempt cap. Small on purpose, like every
	// other fan-out child's: the real retry cadence is the dispatcher's tick.
	MaxAttempts int
	// Tier and RequestedScope are the governance the unit REQUESTS for the
	// work its tick does, on the same footing as a Verb's — a request an
	// operator resolves, never a fact. Both are refused at boot for a job in
	// cases a served tool merely could not stage; see the refusals in
	// compose/extjobs.go.
	Tier           Tier
	RequestedScope Scope
}

// JobKindSuffix is what a workspace child's kind adds to its dispatcher's,
// spelled once. Two kinds derived from one name is the whole reason the suffix
// exists, and both sides — the generator that emits the pair and the runner
// that registers it — have to name the same one.
const JobKindSuffix = "_ws"

// DispatcherKind is the cadenced kind, `ext_<namespace>_<job>`. It answers the
// empty string for a declaration whose unit name is not valid, because a kind
// derived from an invalid namespace is not a kind — Validate is what reports
// that, and a caller reaching a kind before validating has a bug this would
// only hide behind a plausible-looking string.
func (d JobDeclaration) DispatcherKind() string {
	ns, err := d.Unit.Namespace()
	if err != nil {
		return ""
	}
	return ns + "_" + d.Job
}

// ChildKind is the workspace-role kind one tenant's tick runs under,
// `ext_<namespace>_<job>_ws`.
func (d JobDeclaration) ChildKind() string {
	dispatcher := d.DispatcherKind()
	if dispatcher == "" {
		return ""
	}
	return dispatcher + JobKindSuffix
}

// jobKindGrammar is gen-jobs' own kindNameRE, restated here because the two
// sides validate the same strings and this package cannot import a build tool.
// River persists these in river_job.kind, so they are lower snake_case forever.
var jobKindGrammar = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate enforces everything about a declared job that must hold wherever it
// is read — the generator that derives it from the merged contract, and the
// boot that registers it. Both run this, so gen-time acceptance cannot drift
// from boot-time validation.
//
// What it does NOT decide is whether a job may be REGISTERED: the two refusals
// that turn a valid declaration away (a confirm-first tier, an outbound scope)
// are the serving side's, exactly as a served tool's are, because a
// handler-less declaration carrying either is a legitimate manifest request.
func (d JobDeclaration) Validate() error {
	if err := d.Unit.Validate(); err != nil {
		return err
	}
	if !toolNameGrammar.MatchString(d.Job) {
		return fmt.Errorf("extension %q declares job name %q, which is not a valid job name (lower snake_case)", d.Unit, d.Job)
	}
	// Checked rather than assumed: the pair is derived here, but the KINDS are
	// written by hand in the fragment and the generator compares the two, so
	// a derived string that River could not persist has to fail at its own
	// declaration rather than at the first insert.
	for _, kind := range []string{d.DispatcherKind(), d.ChildKind()} {
		if !jobKindGrammar.MatchString(kind) {
			return fmt.Errorf("extension %q, job %q derives kind %q, which River cannot persist (lower snake_case)", d.Unit, d.Job, kind)
		}
	}
	if strings.TrimSpace(d.Queue) == "" {
		return fmt.Errorf("extension %q, job %q declares no queue — a kind on no queue gets no workers and never runs", d.Unit, d.Job)
	}
	if d.Cadence <= 0 {
		return fmt.Errorf("extension %q, job %q declares cadence %s — a dispatcher with no positive tick either never fires or fires as fast as Postgres accepts an insert", d.Unit, d.Job, d.Cadence)
	}
	for _, clock := range []struct {
		what string
		d    time.Duration
	}{{"dispatcher", d.DispatcherTimeout}, {"workspace", d.Timeout}} {
		if clock.d <= 0 {
			return fmt.Errorf("extension %q, job %q declares no %s timeout — River's silent one-minute default is the failure a declared wall clock exists to remove", d.Unit, d.Job, clock.what)
		}
	}
	if d.MaxAttempts <= 0 {
		return fmt.Errorf("extension %q, job %q declares max_attempts %d — unset, River applies its own 25-rung ladder in place of the dispatcher's tick", d.Unit, d.Job, d.MaxAttempts)
	}
	if err := d.Tier.Validate(); err != nil {
		return fmt.Errorf("extension %q, job %q: %w", d.Unit, d.Job, err)
	}
	if err := d.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("extension %q, job %q: %w", d.Unit, d.Job, err)
	}
	return nil
}
