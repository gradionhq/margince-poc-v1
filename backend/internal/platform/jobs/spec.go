// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// Role is what a declared kind DOES. A dispatcher enumerates and enqueues and
// touches no tenant data; a workspace kind carries one tenant's pass and is
// enqueued, never ticked. The zero value names neither — every Spec compiled
// from the contract sets it, so a zero Role means a Spec nobody declared.
type Role int

// The two roles the fleet has. A third would be a change to what a job IS,
// not data: both surfaces (the health report and the metrics) read this to
// decide whether a null workspace_id is correct or a defect.
const (
	Dispatcher Role = iota + 1
	Workspace
)

// FanOutUnit is what one child of a fan-out stands for. A dispatcher enqueues
// one child per unit, and the unit is what makes a child row readable: a
// gmail_watch_renew_connection row is one CONNECTION's renewal, not one
// tenant's. The zero value means the kind fans out to nothing.
type FanOutUnit int

// The three units the tree fans out over.
const (
	FanOutWorkspace FanOutUnit = iota + 1
	FanOutConnection
	FanOutBuild
)

// OptsOwner names who supplies a kind's River insert options, and therefore
// how strongly the declaration binds them. The three levels genuinely differ
// and the difference is stated rather than implied:
//
//   - OptsFanOut — SUPPLIED. The fan-out helper reads the declared queue and
//     attempt cap, so drift is impossible.
//   - OptsArgs — CHECKED. The kind's own InsertOpts() owns them and the census
//     compares the two.
//   - OptsCaller — DECLARED ONLY. The options live at scattered enqueue sites;
//     the declared queue is documentation for the readers, not a governed
//     value. A real gap, stated rather than papered over.
//
// The zero value names none — every Spec from the contract sets it.
type OptsOwner int

// The three ownership levels.
const (
	OptsFanOut OptsOwner = iota + 1
	OptsArgs
	OptsCaller
)

// TimeoutPolicy is a kind's whole-job wall clock in the one of four forms it
// actually takes. Absence is not one of them: a kind with no declared timeout
// fails generation, because River's silent one-minute default is the failure
// this contract exists to remove.
//
// DerivedFrom carries the name of the Go constant a timeout was computed from
// when it is an expression over another module's own limit
// (privacy.MaxPassDuration and friends). Fixed still carries the resolved
// duration — Govern hands River a duration, not a name — and the census is
// what keeps the two from drifting apart when the upstream constant moves.
type TimeoutPolicy struct {
	Fixed        time.Duration
	None         bool
	FromOperator bool
	DerivedFrom  string
}

// Duration is the value Govern hands River. A None policy yields -1, which
// takes the job out of River's rescuer; a FromOperator policy yields the
// value supplied at registration.
func (p TimeoutPolicy) Duration(supplied time.Duration) time.Duration {
	switch {
	case p.None:
		return -1
	case p.FromOperator:
		return supplied
	default:
		return p.Fixed
	}
}

// Cadence is a dispatcher's schedule, in exactly one of three forms: Fixed is
// this repo's own number, OperatorField names the dial the number comes from,
// and OnDemand says a human's confirm enqueues this dispatcher and no clock
// ever does.
//
// OnDemand is a declaration and not an absence, which is the whole reason it
// exists as a field rather than a zero Cadence: a dispatcher whose schedule
// simply went missing and one that deliberately has none are the same bytes
// otherwise, and every consumer reads the compiled table rather than the YAML.
//
// ScheduleWhenPositive names the JobRunnerConfig field whose non-positive
// value means "workers registered, SCHEDULE absent". It is a third posture,
// not a variant of registration: the capability stays wired and only the tick
// goes away.
type Cadence struct {
	Fixed                time.Duration
	OperatorField        string
	OnDemand             bool
	ScheduleWhenPositive string
}

// Registration is what a kind's wiring depends on, and what happens when the
// dependency is absent.
//
// When is a CONJUNCTION of JobRunnerConfig field paths — gmail_watch_renew
// needs both GmailRegistry and GmailWatch.Topic — and an empty When means the
// kind registers unconditionally.
//
// AbsentRegistersAnyway distinguishes the two postures a nil dependency takes:
// absent by omission registers nothing, so a row nothing here could work is
// never queued at all; registered anyway keeps the worker, so a picked-up row
// fails with an actionable message instead of rotting queued. The same
// dependency takes DIFFERENT postures on different kinds, which is why the
// posture is declared per kind rather than per dependency.
type Registration struct {
	When                  []string
	AbsentRegistersAnyway bool
}

// FaultPolicy is what a kind's worker does with a failure it meets. River
// records whatever a Work method returns, so a worker that logs the failure and
// returns nil turns a tenant's failed pass into a green row.
//
// NilAfterLogging is the ratified exception and carries the durable retry
// policy that makes such a success honest — the sidecar backoff, the run row,
// the deferred-retry sweep. The empty string is the strict posture: the worker
// returns its failure. Omission is therefore never a licence, which is why the
// field says what is WAIVED rather than what is required.
type FaultPolicy struct {
	NilAfterLogging string
}

// ArgField is one field of a kind's args struct, and what it is allowed to
// carry. River persists args verbatim in a table with no workspace column and
// no RLS, so a field holding a message body or an address would be a second
// store of subject data that Art. 17 erasure never reaches: a job names a row
// and the worker reads it.
//
// Scalar is the ratified exception — a value that is not an id and could not
// be one — and Reason is what a reader is owed for it. Generation refuses a
// scalar with no reason, and the census refuses an args struct whose fields
// and declaration disagree, so between them every compiled field is an id or
// an argued-for scalar.
//
// Reason also stands on an ID, and there it answers a different question: a
// field whose NAME reads like content (Body, Subject, RecipientEmail) has
// something to argue even when it really is a reference, and the content gate
// is what demands the argument. So Reason is not a synonym for Scalar — it is
// "somebody wrote down why this is safe", which both cases can need.
type ArgField struct {
	Name   string
	Scalar bool
	Reason string
}

// Spec is one declared job kind: everything api/jobs.yaml says about it that
// the running system reads. It is data — the wiring stays composition's, and
// this package never learns what a kind's worker actually does.
type Spec struct {
	Kind string
	// GoType is the name of the args struct in the composition layer that
	// returns this Kind. It is one of the three species of compose identifier
	// this table carries — beside the JobRunnerConfig field paths in
	// Registration and Cadence, and the Go constant names in
	// Timeout.DerivedFrom — and like them it is carried as data rather than as
	// an import: it is what lets a gate assert that the kind↔type pairing still
	// holds without re-parsing the contract, which is the pairing a renamed
	// struct silently breaks.
	GoType       string
	Role         Role
	Queue        string
	Timeout      TimeoutPolicy
	MaxAttempts  int
	FanOutUnit   FanOutUnit
	FanOutTo     string
	OptsOwner    OptsOwner
	Cadence      Cadence
	Registration Registration
	Fault        FaultPolicy
	// Args is the kind's declared args fields in field-name order, and is
	// empty for the fieldless dispatchers. An omitted declaration is not a
	// waiver: the census compares this list against the compiled struct, so a
	// field nobody declared fails there rather than passing unnoticed.
	Args []ArgField
}

// SpecFor returns the declaration for one kind. The bool is the whole point:
// a caller that ignored it would get a zero Spec, whose zero timeout is
// River's one-minute default under another name.
func SpecFor(kind string) (Spec, bool) {
	s, ok := specs[kind]
	return s, ok
}

// Declared iterates every declared kind in kind order, so a caller building a
// report or a metric family walks the same sequence on every process.
func Declared() iter.Seq2[string, Spec] {
	return func(yield func(string, Spec) bool) {
		for _, kind := range slices.Sorted(maps.Keys(specs)) {
			if !yield(kind, specs[kind]) {
				return
			}
		}
	}
}

// DeclaredQueues is every declared queue and the number of workers the
// contract states for it, copied so a reader cannot edit the table underneath
// the next one.
//
// A declared bound is not a bound River applies: composition builds the queue
// set, and this is the number the file publishes for the same pool. The two
// are held equal by the census, because a bound that moved in one place only
// makes the file operators read a lie, and a queue declared but never built is
// one a dispatcher inserts children into that no client works.
func DeclaredQueues() map[string]int {
	return maps.Clone(queues)
}

// MustBeTotal reports the kinds a caller intends to work that the contract
// does not declare. A missing spec would otherwise fall through to River's
// 1-minute default, which is the failure this contract exists to remove.
func MustBeTotal(kinds []string) error {
	var missing []string
	for _, k := range kinds {
		if _, ok := specs[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("jobs: %d kind(s) not declared in api/jobs.yaml: %s — add them there and run `make gen`",
		len(missing), strings.Join(missing, ", "))
}
