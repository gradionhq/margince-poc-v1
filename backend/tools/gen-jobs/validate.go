// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Everything api/jobs.yaml has to be true about itself, checked here so a
// contract that cannot hold fails generation rather than compiling into a
// fleet that behaves differently from what the file says. Each message names
// the offending kind, because the reader is someone who has just added one.

import (
	"fmt"
	"regexp"
)

// The two roles, the three fan-out units, and the three options owners. Each
// is a closed set here because each is a closed set in platform/jobs: adding
// one is a change to what a job IS, and it has to land in both places at once.
const (
	roleDispatcher = "dispatcher"
	roleWorkspace  = "workspace"

	unitWorkspace  = "workspace"
	unitConnection = "connection"
	unitBuild      = "build"

	optsFanOut = "fan_out"
	optsArgs   = "args"
	optsCaller = "caller"

	// cadenceOnDemand is the explicit spelling for the one dispatcher a
	// human's confirm enqueues rather than a clock. It is a declaration and
	// not an omission for the reason every other field here is required: an
	// absent cadence reads as a schedule somebody forgot, and the whole point
	// of the file is that nothing about a job is left to be inferred.
	cadenceOnDemand = "on_demand"

	absentRegistersNothing = "registers_nothing"
	absentRegistersAnyway  = "registers_anyway"

	// argID is the shorthand for the ordinary args field: a reference to a row,
	// which is all a job is meant to carry.
	argID = "id"

	// riverDefaultQueue is river.QueueDefault's string value, spelled here
	// because this tool does not import River. It is the one queue that owes
	// no justification: every other entry exists because work was split out
	// of it, and that split is what a reason states.
	riverDefaultQueue = "default"
)

var (
	roles     = map[string]bool{roleDispatcher: true, roleWorkspace: true}
	fanUnits  = map[string]bool{unitWorkspace: true, unitConnection: true, unitBuild: true}
	optsOwner = map[string]bool{optsFanOut: true, optsArgs: true, optsCaller: true}

	// kindNameRE is the contract's kind-naming rule. River persists these
	// strings in river_job.kind, so they are lowercase snake_case forever.
	kindNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// goTypeRE is the args struct's Go identifier, which the compose half is
	// generated from — an exported name, because compose's args types are.
	goTypeRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*Args$`)
	// configFieldRE is a JobRunnerConfig field path: a field, or a field of a
	// sub-config (GmailWatch.Topic).
	configFieldRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*(\.[A-Z][A-Za-z0-9]*)*$`)
	// goConstRE is the name of the Go constant a {derived: …} timeout tracks.
	goConstRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	// argFieldRE is an args struct's Go field name — exported, because River
	// marshals args to JSON and an unexported field never reaches the wire.
	argFieldRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// validate enforces every invariant the contract can decide on its own. Each
// message names the offending kind, because the reader is someone who has just
// added one and needs to know which line to fix.
func (c contract) validate() error {
	if len(c.Queues) == 0 {
		return fmt.Errorf("contract declares no queues")
	}
	for name, q := range c.Queues {
		if q.MaxWorkers <= 0 {
			return fmt.Errorf("queue %q: max_workers must be positive, got %d", name, q.MaxWorkers)
		}
		if q.Reason == "" && name != riverDefaultQueue {
			return fmt.Errorf("queue %q: declares no reason — a queue exists because work was SPLIT OUT of the default pool, and that reason is the whole content of the decision", name)
		}
	}
	if len(c.Kinds) == 0 {
		return fmt.Errorf("contract declares no kinds")
	}
	byGoType := make(map[string]string, len(c.Kinds))
	for _, name := range c.sortedKinds() {
		def := c.Kinds[name]
		if err := c.validateKind(name, def); err != nil {
			return err
		}
		if other, dup := byGoType[def.GoType]; dup {
			return fmt.Errorf("kind %q: go_type %s is already declared by %q — one args struct is one River kind",
				name, def.GoType, other)
		}
		byGoType[def.GoType] = name
	}
	return nil
}

// validateKind checks one entry's own shape and its edges into the rest of the
// contract.
func (c contract) validateKind(name string, def kindDef) error {
	if !kindNameRE.MatchString(name) {
		return fmt.Errorf("kind %q: name must match %s — River persists it in river_job.kind", name, kindNameRE)
	}
	if !roles[def.Role] {
		return fmt.Errorf("kind %q: role must be %q or %q, got %q", name, roleDispatcher, roleWorkspace, def.Role)
	}
	if !goTypeRE.MatchString(def.GoType) {
		return fmt.Errorf("kind %q: go_type %q must match %s — it names the compose args struct", name, def.GoType, goTypeRE)
	}
	if _, ok := c.Queues[def.Queue]; !ok {
		return fmt.Errorf("kind %q: queue %q has no queues: entry — a queue nothing declares gets no workers and the kind is never run", name, def.Queue)
	}
	if !optsOwner[def.OptsOwner] {
		return fmt.Errorf("kind %q: opts_owner must be %q, %q or %q, got %q", name, optsFanOut, optsArgs, optsCaller, def.OptsOwner)
	}
	if err := validateTimeout(name, def.Timeout); err != nil {
		return err
	}
	if def.MaxAttempts == nil && def.OptsOwner == optsFanOut {
		return fmt.Errorf("kind %q: opts_owner is fan_out but no max_attempts is declared — the fan-out helper reads this number and nothing else supplies it, so its absence is River's silent 25-rung ladder in place of the dispatcher's tick", name)
	}
	if def.MaxAttempts != nil {
		if def.OptsOwner != optsFanOut {
			return fmt.Errorf("kind %q: max_attempts is declared but opts_owner is %q — the file governs the attempt cap only for a fan-out child, and publishing one it does not enforce is the declared-vs-actual drift this contract exists to remove",
				name, def.OptsOwner)
		}
		if *def.MaxAttempts <= 0 {
			return fmt.Errorf("kind %q: max_attempts must be positive, got %d", name, *def.MaxAttempts)
		}
	}
	if err := c.validateFanOut(name, def); err != nil {
		return err
	}
	if err := c.validateCadence(name, def); err != nil {
		return err
	}
	if err := validateRegistration(name, def.Registration); err != nil {
		return err
	}
	if err := validateFault(name, def.Fault); err != nil {
		return err
	}
	return validateArgs(name, def)
}

// validateFault keeps the one departure from returning a failure honest. The
// block exists only to waive, so a waiver with nothing to say is the whole
// error: what makes a green row for a failed pass acceptable is the durable
// retry policy elsewhere, and unstated it is indistinguishable from the
// swallowed error this fleet removes.
func validateFault(name string, f *faultDef) error {
	if f == nil {
		return nil
	}
	if f.NilAfterLogging == "" {
		return fmt.Errorf("kind %q: declares a fault block with no nil_after_logging rationale — the block exists only to waive returning a failure, and an unstated waiver is a swallowed error with a heading", name)
	}
	return nil
}

// validateArgs holds each declared field to the shapes it may take.
//
// A scalar is a payload the erasure engine cannot reach through the row the job
// names, so it is admitted only with a reason. An id ordinarily needs none and
// is spelled with the bare shorthand — but it may take the mapping form to
// carry one, because a field whose NAME reads like content (Body, Subject,
// RecipientEmail) has something to argue even when it really is a reference,
// and jobargscontent_test.go is what demands the argument. What the mapping
// form may never be is empty: a shape with neither scalar: true nor a reason
// says nothing the shorthand does not.
func validateArgs(name string, def kindDef) error {
	for _, field := range def.sortedArgs() {
		if !argFieldRE.MatchString(field) {
			return fmt.Errorf("kind %q: args field %q must match %s — it names an exported field of %s", name, field, argFieldRE, def.GoType)
		}
		arg := def.Args[field]
		if !arg.Scalar {
			if arg.argued && arg.Reason == "" {
				return fmt.Errorf("kind %q: args field %q takes the mapping form and says nothing — an id with nothing to argue is spelled %q; the mapping form carries scalar: true, or the reason a field whose name reads like content really is a reference", name, field, argID)
			}
			continue
		}
		if arg.Reason == "" {
			return fmt.Errorf("kind %q: args field %q is declared a scalar with no reason — River persists args verbatim in a table with no workspace column and no RLS, so a value that is not an id has to say why it is safe there", name, field)
		}
	}
	return nil
}

// validateTimeout holds the rule this whole contract exists for: every kind
// has a CHOSEN timeout, in exactly one of the four forms.
func validateTimeout(name string, t *timeoutDef) error {
	if t == nil {
		return fmt.Errorf("kind %q: declares no timeout — an absent one is River's silent 1-minute default, which is what this contract removes", name)
	}
	forms := 0
	if t.None {
		forms++
	}
	if t.Operator != "" {
		forms++
	}
	if t.Derived != "" {
		forms++
	}
	if t.Fixed != 0 && t.Derived == "" {
		forms++
	}
	if forms != 1 {
		return fmt.Errorf("kind %q: timeout must take exactly one of the four forms (a duration, {derived: …}, {operator: …}, {none: true}), got %d", name, forms)
	}
	switch {
	case t.None:
		if t.Reason == "" {
			return fmt.Errorf("kind %q: a {none: true} timeout needs a reason — taking a job out of River's rescuer is a decision, not a default", name)
		}
	case t.Operator != "":
		if !configFieldRE.MatchString(t.Operator) {
			return fmt.Errorf("kind %q: timeout operator %q must name a JobRunnerConfig field", name, t.Operator)
		}
	case t.Derived != "":
		if !goConstRE.MatchString(t.Derived) {
			return fmt.Errorf("kind %q: timeout derived %q must name a Go constant", name, t.Derived)
		}
		if t.Fixed <= 0 {
			return fmt.Errorf("kind %q: a {derived: %s} timeout needs its resolved value — Govern hands River a duration, and the census is what proves the two still agree",
				name, t.Derived)
		}
	case t.Fixed <= 0:
		return fmt.Errorf("kind %q: timeout must be positive, got %v — a deliberate absence is spelled {none: true}", name, t.Fixed)
	}
	return nil
}

// validateFanOut keeps the edge and its unit together, and keeps the edge
// pointing at a kind that can actually receive it.
func (c contract) validateFanOut(name string, def kindDef) error {
	if (def.FansOutTo == "") != (def.FanOutUnit == "") {
		return fmt.Errorf("kind %q: fans_out_to and fan_out_unit are one declaration — a child with no unit is a row nobody can read, and a unit with no child names nothing", name)
	}
	if def.FansOutTo == "" {
		if def.Role == roleDispatcher {
			return fmt.Errorf("kind %q: is a dispatcher that fans out to nothing — a dispatcher enumerates and enqueues, so one with no child does no work at all", name)
		}
		return nil
	}
	if def.Role != roleDispatcher {
		return fmt.Errorf("kind %q: declares a fan-out but its role is %q — only a dispatcher enqueues on the fleet's behalf", name, def.Role)
	}
	if !fanUnits[def.FanOutUnit] {
		return fmt.Errorf("kind %q: fan_out_unit must be %q, %q or %q, got %q", name, unitWorkspace, unitConnection, unitBuild, def.FanOutUnit)
	}
	child, ok := c.Kinds[def.FansOutTo]
	if !ok {
		return fmt.Errorf("kind %q: fans_out_to %q, which no kinds: entry declares", name, def.FansOutTo)
	}
	if child.Role != roleWorkspace {
		return fmt.Errorf("kind %q: fans_out_to %q, whose role is %q — a fan-out child carries one tenant's pass", name, def.FansOutTo, child.Role)
	}
	return nil
}

// validateCadence holds the schedule to the role: a dispatcher is ticked and a
// workspace kind is enqueued, and neither may claim the other's posture.
func (c contract) validateCadence(name string, def kindDef) error {
	if def.Role != roleDispatcher {
		if def.Cadence != nil {
			return fmt.Errorf("kind %q: declares a cadence but its role is %q — a workspace kind is enqueued by its dispatcher, never ticked", name, def.Role)
		}
		return nil
	}
	cad := def.Cadence
	if cad == nil {
		return fmt.Errorf("kind %q: declares no cadence — a dispatcher's schedule is never absent by accident; the one a human's confirm enqueues says %q",
			name, cadenceOnDemand)
	}
	forms := 0
	if cad.OnDemand {
		forms++
	}
	if cad.Fixed != 0 {
		forms++
	}
	if cad.Operator != "" {
		forms++
	}
	if forms != 1 {
		return fmt.Errorf("kind %q: cadence must take exactly one of a duration, {operator: …} or %q, got %d", name, cadenceOnDemand, forms)
	}
	if cad.Operator != "" && !configFieldRE.MatchString(cad.Operator) {
		return fmt.Errorf("kind %q: cadence operator %q must name a JobRunnerConfig field", name, cad.Operator)
	}
	if cad.ScheduleWhenPositive == "" {
		return nil
	}
	if !configFieldRE.MatchString(cad.ScheduleWhenPositive) {
		return fmt.Errorf("kind %q: schedule_when_positive %q must name a JobRunnerConfig field", name, cad.ScheduleWhenPositive)
	}
	if cad.OnDemand {
		return fmt.Errorf("kind %q: schedule_when_positive names the dial a non-positive value silences, and an %q kind has no dial", name, cadenceOnDemand)
	}
	return nil
}

// validateRegistration keeps a declared dependency and its absence-posture
// together: a condition with no posture leaves the reader guessing which of
// the two a nil dependency takes, and they are opposites.
func validateRegistration(name string, reg *registrationDef) error {
	if reg == nil || len(reg.When) == 0 {
		if reg != nil && reg.Absent != "" {
			return fmt.Errorf("kind %q: registration declares an absent posture but no condition to be absent from", name)
		}
		return nil
	}
	for _, field := range reg.When {
		if !configFieldRE.MatchString(field) {
			return fmt.Errorf("kind %q: registration when %q must name a JobRunnerConfig field", name, field)
		}
	}
	switch reg.Absent {
	case absentRegistersNothing, absentRegistersAnyway:
		return nil
	default:
		return fmt.Errorf("kind %q: registration absent must be %q or %q, got %q", name, absentRegistersNothing, absentRegistersAnyway, reg.Absent)
	}
}
