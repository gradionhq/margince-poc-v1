// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The parsed shape of api/jobs.yaml: one Go type per block of the file, the
// decoders that accept each field's shorthand spelling, and parseContract,
// which is the only way in. Every rule the document has to satisfy lives in
// validate.go beside it.

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// queueDef is one queues.<name> entry: the pool bound, and why it is that
// number. The reason is prose for the reader — nothing derives behaviour from
// it — but a bound with no stated reason is a tuning knob rather than the
// posture the queue set is meant to be, so it is required.
type queueDef struct {
	MaxWorkers int    `yaml:"max_workers"`
	Reason     string `yaml:"reason"`
}

// timeoutDef is a kind's whole-job wall clock in the four forms the tree
// actually takes. Exactly one form per entry.
type timeoutDef struct {
	// Fixed is the resolved duration Govern hands River. It is set for a
	// literal AND for a {derived: …} entry: River takes a duration, not the
	// name of a Go constant.
	Fixed time.Duration
	// Derived names the Go constant a derived duration was computed from, so
	// the census can prove the two still agree when that constant moves. A
	// bare literal would silently stop tracking it.
	Derived string
	// Operator names the JobRunnerConfig field the value is computed from at
	// registration; the duration is then not knowable here at all.
	Operator string
	// None declares a deliberate absence: the pass is bounded by a backlog
	// rather than a wall clock, and River's rescuer must leave it alone.
	None   bool
	Reason string
}

// UnmarshalYAML accepts a bare duration alongside the three mapping forms, so
// the common case — a kind whose timeout is just a number — stays one token.
func (t *timeoutDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		d, err := time.ParseDuration(node.Value)
		if err != nil {
			return fmt.Errorf("timeout %q: %w", node.Value, err)
		}
		t.Fixed = d
		return nil
	}
	var raw struct {
		Derived  string `yaml:"derived"`
		Value    string `yaml:"value"`
		Operator string `yaml:"operator"`
		None     bool   `yaml:"none"`
		Reason   string `yaml:"reason"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	t.Derived, t.Operator, t.None, t.Reason = raw.Derived, raw.Operator, raw.None, raw.Reason
	if raw.Value != "" {
		d, err := time.ParseDuration(raw.Value)
		if err != nil {
			return fmt.Errorf("timeout value %q: %w", raw.Value, err)
		}
		t.Fixed = d
	}
	return nil
}

// cadenceDef is a dispatcher's schedule: a literal interval, the named
// operator dial it is taken from, or the explicit on_demand.
type cadenceDef struct {
	Fixed                time.Duration
	Operator             string
	OnDemand             bool
	ScheduleWhenPositive string
}

// UnmarshalYAML accepts `24h`, `on_demand`, or the mapping form.
func (c *cadenceDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value == cadenceOnDemand {
			c.OnDemand = true
			return nil
		}
		d, err := time.ParseDuration(node.Value)
		if err != nil {
			return fmt.Errorf("cadence %q: want a duration or %q: %w", node.Value, cadenceOnDemand, err)
		}
		c.Fixed = d
		return nil
	}
	var raw struct {
		Operator             string `yaml:"operator"`
		ScheduleWhenPositive string `yaml:"schedule_when_positive"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("cadence: %w", err)
	}
	c.Operator, c.ScheduleWhenPositive = raw.Operator, raw.ScheduleWhenPositive
	return nil
}

// registrationDef is what a kind's wiring depends on and what happens when the
// dependency is absent.
type registrationDef struct {
	When   []string
	Absent string
}

// UnmarshalYAML accepts the scalar `always` alongside the mapping, because
// most kinds depend on nothing and spelling that as an empty mapping would
// read as a condition somebody deleted.
func (r *registrationDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value != "always" {
			return fmt.Errorf("registration: want a mapping or %q, got %q", "always", node.Value)
		}
		return nil
	}
	var raw struct {
		When   []string `yaml:"when"`
		Absent string   `yaml:"absent"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("registration: %w", err)
	}
	r.When, r.Absent = raw.When, raw.Absent
	return nil
}

// faultDef is a kind's ratified departure from returning its failure. It has
// one form and no shorthand, because the strict posture is the ABSENCE of this
// block: a kind that declares nothing must return what went wrong.
type faultDef struct {
	NilAfterLogging string `yaml:"nil_after_logging"`
}

// argFieldDef is one args field: an id, or a scalar somebody argued for.
type argFieldDef struct {
	Scalar bool
	Reason string
	// argued records that the entry took the mapping form. It is what lets
	// validate tell the shorthand `id` apart from a mapping that forgot
	// scalar: true — both leave Scalar false, and only the second is a
	// mistake — while keeping the message where it can name the kind.
	argued bool
}

// UnmarshalYAML accepts the bare `id` alongside the mapping, because an id is
// what almost every field is and spelling it as a mapping would bury the four
// that are not.
func (a *argFieldDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value != argID {
			return fmt.Errorf("args field: want %q or a {scalar: true, reason: …} mapping, got %q", argID, node.Value)
		}
		return nil
	}
	var raw struct {
		Scalar bool   `yaml:"scalar"`
		Reason string `yaml:"reason"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("args field: %w", err)
	}
	a.Scalar, a.Reason, a.argued = raw.Scalar, raw.Reason, true
	return nil
}

// kindDef is one kinds.<name> entry.
type kindDef struct {
	Role         string                 `yaml:"role"`
	GoType       string                 `yaml:"go_type"`
	Queue        string                 `yaml:"queue"`
	Timeout      *timeoutDef            `yaml:"timeout"`
	MaxAttempts  *int                   `yaml:"max_attempts"`
	FansOutTo    string                 `yaml:"fans_out_to"`
	FanOutUnit   string                 `yaml:"fan_out_unit"`
	OptsOwner    string                 `yaml:"opts_owner"`
	Cadence      *cadenceDef            `yaml:"cadence"`
	Registration *registrationDef       `yaml:"registration"`
	Fault        *faultDef              `yaml:"fault"`
	Args         map[string]argFieldDef `yaml:"args"`
	Reason       string                 `yaml:"reason"`
	DerivesFrom  string                 `yaml:"derives-from"`
}

// sortedArgs is the deterministic order the emitted args list walks; Go map
// iteration is not stable and an unsorted emitter drifts the drift gate.
func (k kindDef) sortedArgs() []string {
	names := make([]string, 0, len(k.Args))
	for name := range k.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// contract is the parsed jobs.yaml. Both members are YAML mappings, and Go map
// iteration order is not stable, so every emitter below sorts its keys rather
// than ranging over these directly — an unsorted emitter passes locally and
// drifts the drift gate in CI.
type contract struct {
	Queues map[string]queueDef `yaml:"queues"`
	Kinds  map[string]kindDef  `yaml:"kinds"`
}

// sortedKinds is the one deterministic order every emitted table walks.
func (c contract) sortedKinds() []string {
	names := make([]string, 0, len(c.Kinds))
	for name := range c.Kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseContract decodes and validates jobs.yaml. Unknown keys are errors: a
// typo'd field would otherwise silently drop a declaration and leave the kind
// running on whatever River defaults to.
func parseContract(raw []byte) (contract, error) {
	if err := rejectDuplicateKinds(raw); err != nil {
		return contract{}, err
	}
	var c contract
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return contract{}, fmt.Errorf("parsing contract: %w", err)
	}
	if err := c.validate(); err != nil {
		return contract{}, err
	}
	return c, nil
}

// rejectDuplicateKinds walks the raw document for a kind declared twice.
// Decoding into a map would silently keep the last one, and two entries for
// one kind string mean two different declarations of one persisted River
// kind — whichever survived would be the one nobody reviewed.
func rejectDuplicateKinds(raw []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parsing contract: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "kinds" {
			continue
		}
		kinds := root.Content[i+1]
		seen := make(map[string]bool, len(kinds.Content)/2)
		for j := 0; j+1 < len(kinds.Content); j += 2 {
			name := kinds.Content[j].Value
			if seen[name] {
				return fmt.Errorf("kind %q is declared twice", name)
			}
			seen[name] = true
		}
	}
	return nil
}
