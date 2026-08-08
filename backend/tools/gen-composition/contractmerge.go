// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Applying fragments to a base contract: the merge itself, the JSONPath subset
// a target may use, and the two walls that keep the result additive — the
// ownership rule and the route namespace. contracts.go is the other half: it
// reads the fragments this file consumes.
//
// The split is where it is because these are two separable concerns with one
// seam (contractFragment): everything here is a pure function of a base
// document and a fragment list, touching no filesystem at all.

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// mergeContract applies frags to base, in the order given.
//
// The zero-fragment case returns the base slice ITSELF, deliberately, and
// this is the load-bearing line of the whole file: a vanilla installation's
// composed contract must be the committed contract byte for byte, and a
// parse-and-reserialize round trip breaks that while passing every semantic
// check — yaml.v3 rewrites comments, quoting, key style, line folding and
// indentation. The empty-tree guarantee is checked by comparison
// (TestComposedContractIsByteIdenticalWithNoFragments), so a reserialization
// slipped in here fails loudly rather than eroding the guarantee in silence.
func mergeContract(base []byte, frags []contractFragment) ([]byte, error) {
	if len(frags) == 0 {
		return base, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(base, &doc); err != nil {
		return nil, fmt.Errorf("parsing the base contract: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the base contract is not a YAML mapping")
	}
	root := doc.Content[0]
	// claimed maps a target to the fragment that already took it, refusing two
	// overlays on ONE JSONPath — which have no defined winner, so the loser's
	// operations would vanish from the client types and the docs while its
	// registrations still exist.
	//
	// Scope, precisely: this map compares target STRINGS, so it sees only
	// exact duplicates. Two units reaching the same NODE by different targets
	// ($.a.b versus $.a.b.c) is a different class and is checkOwnership's, not
	// this map's — the earlier version of this comment claimed the merge
	// refused "extension-name order deciding what is published" in general,
	// which was more than any single rule here delivered.
	//
	// Even for exact duplicates addNode's "already declares" rule would refuse
	// the second one, since a target is a map key. What this map buys is
	// ATTRIBUTION: the error names both units. Verified by mutation — with this
	// branch disabled the refusal still fires, and names only the loser.
	claimed := make(map[string]string)
	// owners records, per declared node, which unit declared it — the state
	// the ownership rule is decided against. See checkOwnership.
	owners := make(map[string]string)
	for _, f := range frags {
		for _, a := range f.Actions {
			if prev, ok := claimed[a.Target]; ok {
				return nil, fmt.Errorf("target %s is claimed by both %s and %s — two overlays on one JSONPath have no defined winner, so the merge refuses rather than letting extension order decide the contract", a.Target, prev, f.Source)
			}
			claimed[a.Target] = f.Source
			steps, err := parseTarget(a.Target)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Source, err)
			}
			if err := checkRouteNamespace(f.Unit, steps); err != nil {
				return nil, fmt.Errorf("%s: %w", f.Source, err)
			}
			if err := checkOwnership(f.Unit, steps, owners); err != nil {
				return nil, fmt.Errorf("%s: target %s: %w", f.Source, a.Target, err)
			}
			if err := addNode(root, steps, a.Update); err != nil {
				return nil, fmt.Errorf("%s: target %s: %w", f.Source, a.Target, err)
			}
			// Recorded only after the add succeeded, so a refused action
			// never confers ownership of a node that was not created.
			recordOwnership(owners, steps, f.Unit)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	// The base contracts are two-space indented; a composed contract a human
	// diffs against its base should not differ in whitespace everywhere.
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// targetStepRE matches one `.identifier` step of a target path.
var targetStepRE = regexp.MustCompile(`^\.([A-Za-z_][A-Za-z0-9_-]*)`)

// parseTarget reads the constrained JSONPath subset this composer can
// evaluate: `$` followed by one or more child steps, each either
// `.identifier` or `['literal']`. That is enough to name any node an
// extension ADDS (a path item, a schema, a job kind, a task) and nothing
// else — no wildcards, no filters, no descent, no array indices.
//
// The subset is a refusal surface, not a limitation to work around: a target
// that can select more than one node could not be checked for collisions
// against another unit's target by string equality, which is the rule
// mergeContract enforces. Widening the grammar means replacing that rule
// first.
func parseTarget(target string) ([]string, error) {
	rest, ok := strings.CutPrefix(target, "$")
	if !ok {
		return nil, fmt.Errorf("target %q must start at the document root ($)", target)
	}
	var steps []string
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "["):
			end := strings.Index(rest, "']")
			if !strings.HasPrefix(rest, "['") || end < 0 {
				return nil, fmt.Errorf("target %q: a bracket step is a single-quoted literal key, e.g. ['/v1/ext/name/thing']", target)
			}
			steps = append(steps, rest[2:end])
			rest = rest[end+2:]
		default:
			m := targetStepRE.FindStringSubmatch(rest)
			if m == nil {
				return nil, fmt.Errorf("target %q: this composer evaluates absolute child paths only (.key or ['key']); %q is not one", target, rest)
			}
			steps = append(steps, m[1])
			rest = rest[len(m[0]):]
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("target %q selects the whole document — name the node to add", target)
	}
	return steps, nil
}

// ownershipContainers are the contract containers an extension may add a node
// to, and they define where ownership begins: for a container C, the owned node
// is the ONE step below it, C.<name>.
//
// The boundary is per container, not a fixed depth — $.paths.<path> is two deep
// while $.components.schemas.<name> is three — so a depth constant cannot
// express it. Declared longest-prefix-first, since a shorter entry would
// otherwise shadow a longer one.
//
// The list is deliberately short, and a container that is NOT on it is not
// forbidden in principle — it is simply not composed yet. An omission is a loud
// refusal rather than a silent pass, and the refusal says which of the two it
// is, because the two call for different things from the reader: a unit needing
// components.responses is asking for a container nobody has built the
// composition for, and the answer is a reviewed line here. The same posture
// composedWork takes toward go.work directives it does not compose yet.
//
// `queues` is the one omission with an argument BEHIND it rather than merely
// waiting for one, and it belongs here because gen-jobs requires every kind's
// queue: to name a queues: entry, so a reader adding job kinds arrives at this
// list immediately. A River queue is a bound on the process's worker pool,
// shared with core work — an extension declaring one would not be adding a
// capability beside a core node, it would be allocating a share of the
// installation's concurrency from a directory. Composition's own queue set
// (compose/jobqueues.go) would have to become composed too, and the census that
// holds declared bounds equal to built ones with it. So an extension job rides
// one of the pools the installation already declared, and the job composer
// checks that it does (tools/gen-composition/extjobs.go).
var ownershipContainers = [][]string{
	{"components", "schemas"},
	{"paths"},
	{"kinds"},
	{"tasks"},
}

// ownershipContainerFor finds the container a target sits inside. It requires
// steps to be at least one longer than the container: a target naming the
// container ITSELF ($.paths) adds no node to it and is not a match.
func ownershipContainerFor(steps []string) ([]string, bool) {
	for _, c := range ownershipContainers {
		if len(steps) > len(c) && slices.Equal(steps[:len(c)], c) {
			return c, true
		}
	}
	return nil, false
}

// ownerKey identifies the owned node a target sits under. NUL-joined rather
// than dot-joined because a path item's name contains slashes and dots and
// could otherwise be spelled two ways that collide.
func ownerKey(steps, container []string) string {
	return strings.Join(steps[:len(container)+1], "\x00")
}

// recordOwnership notes that unit declared the owned node, but ONLY when the
// target created that node rather than reaching inside an existing one.
func recordOwnership(owners map[string]string, steps []string, unit string) {
	container, ok := ownershipContainerFor(steps)
	if ok && len(steps) == len(container)+1 {
		owners[ownerKey(steps, container)] = unit
	}
}

// containerList renders the allowed containers for an error message.
func containerList() string {
	names := make([]string, 0, len(ownershipContainers))
	for _, c := range ownershipContainers {
		names = append(names, "$."+strings.Join(c, "."))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// checkOwnership is the additive-only rule, stated at the right depth.
//
// The rule: a fragment may add a node at $.<container>.<name>, and may then
// reach INSIDE that node — but only into a node THIS unit declared in this
// same merge. It may never reach inside a node it did not declare.
//
// The earlier version of this check was leaf-only: addNode required just that
// the final key be absent and every parent exist. That left two holes, both
// reproduced against the real contracts before this rule was written:
//
//   - $.components.schemas.Deal.properties.hijacked composed successfully into
//     the REAL crm.yaml, adding a field to the core Deal schema. Nothing about
//     it looks like an extension node, and gen-recordfields and gen-agentpolicy
//     would compile it into core types the moment they read the composed lane.
//     $.kinds.<core_kind>.<new_key> and $.tasks.<core_task>.<new_key> are the
//     same hole in the other two contracts.
//   - Ordering decided validity: alpha adds $.components.schemas.Shared, then
//     beta targets $.components.schemas.Shared.properties, whose parent exists
//     only because alpha ran first. Deterministic, so never a flake — but
//     alpha→beta composed while beta→alpha errored, and the claimed map cannot
//     see this class at all because the two target STRINGS differ.
//
// Both are one root cause: "does the final key exist" is a question about a
// leaf, and ownership is a question about the node the leaf lives in.
//
// A target outside every known container is refused outright, which covers both
// $.webhooks (a top-level block of contract STRUCTURE, not a capability) and
// $.paths (the container itself). Those targets also have no owned node for the
// rule to be judged against, so admitting them would leave it undefined.
func checkOwnership(unit string, steps []string, owners map[string]string) error {
	container, ok := ownershipContainerFor(steps)
	if !ok {
		return fmt.Errorf("names a node inside a container this composer does not compose yet — an extension may extend %s. That is a statement about what has been built, not a verdict on the request: if this container should be composable, the composition for it (and the gates that read it) is the change to make. Contract STRUCTURE is the one class that stays out — a fragment adds capabilities, never the shape of the document", containerList())
	}
	if len(steps) == len(container)+1 {
		// The node is being created here; addNode refuses it if it already
		// exists, which is what keeps "core node" and "declared by a unit"
		// disjoint.
		return nil
	}
	owned := strings.Join(steps[:len(container)+1], ".")
	switch owner, ok := owners[ownerKey(steps, container)]; {
	case !ok:
		return fmt.Errorf("reaches inside %s, which this installation's contract already owns — a fragment adds a node beside a core node, never a field inside one", owned)
	case owner != unit:
		return fmt.Errorf("reaches inside %s, which extension %s declared — a fragment may only extend a node it declares itself, or the merge order would decide whether it composes at all", owned, owner)
	}
	return nil
}

// checkRouteNamespace holds the namespace wall this composer can state for
// itself: a path item a unit adds lives under /ext/<name>, the route namespace
// the global constraints fix. Without it a fragment could declare
// /deals/anything and publish it into the core surface — contract-level
// namespace squatting that no later gate is positioned to see as such.
//
// The spelling is the CONTRACT's, not the server's. Every path in these
// documents is relative to the contract's own `servers` url, which already ends
// in /v1 — core writes /me and /auth/login — so an extension writes
// /ext/<name>/…. A fragment writing the full /v1/ext/… is refused here, and
// that refusal is the point rather than a side effect: the merged document
// would otherwise resolve those operations to https://host/v1/v1/ext/…, wrong
// for every consumer of the published contract at once. The base path is put
// back exactly once, at mount time, by extension.Verb.ServedPath.
//
// checkOwnership covers the deeper case (a fragment reaching inside an
// existing path item); this covers the shallow one, where the path item is
// new but its NAME belongs to core's namespace.
//
// It is a prefix rule with an explicit boundary: /ext/undercover must not pass
// for unit `u`.
//
// Nothing analogous is enforced for job kinds, task names or schema names.
// Those namespaces are real (ext_<name>_*) but their gates belong to the
// generators that compile them, which read the merged file — this composer
// would only be guessing at four different contracts' shapes.
func checkRouteNamespace(unit string, steps []string) error {
	if len(steps) < 2 || steps[0] != "paths" {
		return nil
	}
	prefix := extension.RoutePrefix + unit
	if steps[1] == prefix || strings.HasPrefix(steps[1], prefix+"/") {
		return nil
	}
	return fmt.Errorf("path %s is outside the unit's route namespace — an extension declares routes under %s", steps[1], prefix)
}

// addNode walks steps through the base document and adds the final key.
//
// Every parent must already exist: a fragment that invented `webhooks:`
// because it misspelled `paths:` would otherwise publish a whole block the
// contract's readers ignore. And the final key must NOT exist, so no single
// key is ever overwritten.
//
// That is a statement about the LEAF only, and on its own it does not make the
// merge additive — $.components.schemas.Deal.properties.hijacked satisfies
// every rule here while adding a field to a core schema. checkOwnership is
// what makes "the merged contract is the base plus, never the base altered"
// true; this function is only its final step. Do not restate the strong
// property here.
func addNode(root *yaml.Node, steps []string, update yaml.Node) error {
	parent := root
	for i, key := range steps[:len(steps)-1] {
		next := mappingValue(parent, key)
		if next == nil {
			return fmt.Errorf("the contract has no %s to extend", strings.Join(steps[:i+1], "."))
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("%s is not a mapping, so nothing can be added under it", strings.Join(steps[:i+1], "."))
		}
		parent = next
	}
	last := steps[len(steps)-1]
	if mappingValue(parent, last) != nil {
		return fmt.Errorf("the contract already declares %s — a fragment adds nodes, it never redefines one", last)
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: last},
		&update,
	)
	return nil
}

// mappingValue returns the value node for key, or nil when the mapping does
// not carry it.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
