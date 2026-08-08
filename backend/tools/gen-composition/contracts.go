// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The contract composition: build/composition/api/ holds the EFFECTIVE
// contracts for this installation — each core contract with every enabled
// unit's api/ fragment merged in. Publication, the client types, the docs
// and the operator manifest read the merged file, so an extension's
// operations are contract-declared exactly like core's rather than
// bolted on beside the contract.
//
// The governing property is stated in mergeContract: with no fragments the
// composed contract IS the base, byte for byte. A vanilla installation must
// compose to the committed file, not to a semantically equal reserialization
// of it, or the empty-tree guarantee the whole composition rests on becomes
// unobservable.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// apiLayer is the unit subdirectory holding contract fragments, spelled
// once — it is a path component, a refusal message and (by its absence from
// unbuiltCapabilityLayers) a statement that the layer has a composition now.
const apiLayer = "api"

// composedContractBases are the core contracts an extension may extend. A
// fragment file is NAMED after the contract it targets, so the mapping is
// the filename and needs no in-document `extends:` to disagree with it.
// Sorted, because it is also the order errors and outputs are produced in.
var composedContractBases = []string{"ai-tasks.yaml", "crm.yaml", "jobs.yaml", "public-events.yaml"}

// overlayVersion is the one accepted `overlay:` value. The document shape is
// the OpenAPI Overlay 1.0.0 subset this composer can evaluate TOTALLY:
// additive `update` actions on absolute, child-only JSONPath targets. The
// version is checked rather than ignored so a unit written against a future
// overlay dialect fails here instead of composing a contract that silently
// omits half of what it asked for.
const overlayVersion = "1.0.0"

// overlayDoc is extensions/<name>/api/<base>.
type overlayDoc struct {
	Overlay string          `yaml:"overlay"`
	Info    overlayInfo     `yaml:"info"`
	Actions []overlayAction `yaml:"actions"`
}

// overlayInfo is provenance for a human reading the merged contract's
// origin; nothing derives behaviour from it. It is required because an
// overlay with no stated author or version is an anonymous edit to a
// published contract.
type overlayInfo struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
}

// overlayAction adds Update at Target.
//
// The Overlay specification's `remove` is deliberately NOT a field here:
// KnownFields(true) turns it into a named refusal, because an extension
// deleting a core contract node would break every existing client while
// looking like a local capability declaration. Fail-closed, as astreader.go
// is for manifest fields it does not recognise.
type overlayAction struct {
	Target string    `yaml:"target"`
	Update yaml.Node `yaml:"update"`
}

// contractFragment is one unit's overlay document for one base contract,
// carrying the source path so a collision can name both sides.
type contractFragment struct {
	Unit    string
	Source  string
	Actions []overlayAction
}

// collectUnitFragments reads a unit's api/ layer. Absent is the common case
// (a Go-only unit) and composes nothing; present, every file in it must be a
// fragment this composer understands. Fail-closed on anything else: a file
// the layer silently ignored would look declared and publish nothing.
func collectUnitFragments(name, dir string) (map[string]contractFragment, error) {
	entries, err := os.ReadDir(filepath.Join(dir, apiLayer))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	frags := make(map[string]contractFragment, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			return nil, fmt.Errorf("extensions/%s: %s/%s is a directory — the api layer is a flat set of overlay documents, one per core contract", name, apiLayer, e.Name())
		}
		if !slices.Contains(composedContractBases, e.Name()) {
			return nil, fmt.Errorf("extensions/%s: %s/%s does not name a core contract — an overlay file is named after the contract it extends (%s)",
				name, apiLayer, e.Name(), strings.Join(composedContractBases, ", "))
		}
		raw, err := os.ReadFile(filepath.Join(dir, apiLayer, e.Name())) // #nosec G304 -- generator reading the unit tree it was pointed at
		if err != nil {
			return nil, err
		}
		actions, err := parseOverlay(raw)
		if err != nil {
			return nil, fmt.Errorf("extensions/%s: %s/%s: %w", name, apiLayer, e.Name(), err)
		}
		frags[e.Name()] = contractFragment{
			Unit:    name,
			Source:  "extensions/" + name + "/" + apiLayer + "/" + e.Name(),
			Actions: actions,
		}
	}
	return frags, nil
}

// parseOverlay decodes one fragment strictly. Unknown keys are errors for
// the reason they are in gen-jobs and gen-aitasks: a typo here is not a
// missing declaration but a different one, and this document edits a
// published contract.
func parseOverlay(raw []byte) ([]overlayAction, error) {
	var doc overlayDoc
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	// Only the first document is read, so anything after a `---` would be
	// digested as part of the unit tree while composing nothing.
	var second yaml.Node
	if err := dec.Decode(&second); err == nil {
		return nil, fmt.Errorf("the fragment carries more than one YAML document — only the first is composed; keep every action in one document")
	} else if !strings.Contains(err.Error(), "EOF") {
		return nil, fmt.Errorf("reading past the first document: %w", err)
	}
	if doc.Overlay != overlayVersion {
		return nil, fmt.Errorf("overlay %s is not a dialect this composer evaluates — write overlay: %s", doc.Overlay, overlayVersion)
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		return nil, fmt.Errorf("info.title and info.version are required — an overlay edits a published contract and may not be anonymous")
	}
	if len(doc.Actions) == 0 {
		return nil, fmt.Errorf("the fragment declares no actions — delete the file rather than shipping a no-op edit to the contract")
	}
	for i, a := range doc.Actions {
		if a.Target == "" {
			return nil, fmt.Errorf("action %d declares no target", i)
		}
		if a.Update.IsZero() {
			return nil, fmt.Errorf("action %d (%s) declares no update — this composer only adds nodes", i, a.Target)
		}
	}
	return doc.Actions, nil
}

// composedContracts merges every enabled unit's fragments into each core
// contract. units arrives sorted by name (scanExtensions guarantees it), and
// that is the application order: reproducible, and independent of the order
// the filesystem hands directories over.
func composedContracts(root string, units []extensionUnit) (map[string][]byte, error) {
	out := make(map[string][]byte, len(composedContractBases))
	for _, base := range composedContractBases {
		raw, err := os.ReadFile(filepath.Join(root, "backend", apiLayer, base))
		if err != nil {
			return nil, err
		}
		var frags []contractFragment
		for _, u := range units {
			if f, ok := u.Fragments[base]; ok {
				frags = append(frags, f)
			}
		}
		merged, err := mergeContract(raw, frags)
		if err != nil {
			return nil, fmt.Errorf("composing %s/%s: %w", apiLayer, base, err)
		}
		out[base] = merged
	}
	return out, nil
}

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
	// claimed maps a target to the fragment that already took it. Two
	// overlays on one JSONPath have no defined winner: extension-name order
	// would silently decide what the installation publishes, and the loser's
	// operations would vanish from the client types and the docs while its
	// registrations still exist.
	//
	// addNode's "already declares" rule would also refuse the second one — a
	// target is a map key, so the first application makes it exist. What this
	// map buys is ATTRIBUTION: the error names both units, which is the
	// difference between an operator knowing who to talk to and being told
	// their own contract disagrees with itself. Verified by mutation: with
	// this branch disabled the refusal still fires, and names only the loser.
	claimed := make(map[string]string)
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
			if err := addNode(root, steps, a.Update); err != nil {
				return nil, fmt.Errorf("%s: target %s: %w", f.Source, a.Target, err)
			}
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

// checkRouteNamespace holds the ONE namespace wall this composer can state
// for itself: a path item a unit adds lives under /v1/ext/<name>, the route
// namespace the global constraints fix. Without it a fragment could declare
// /v1/deals/anything and publish it into the core surface — contract-level
// namespace squatting that no later gate is positioned to see as such.
//
// It is a prefix rule with an explicit boundary: /v1/ext/undercover must not
// pass for unit `u`.
//
// Nothing analogous is enforced for job kinds, task names or schema names.
// Those namespaces are real (ext_<name>_*) but their gates belong to the
// generators that compile them, which read the merged file — this composer
// would only be guessing at four different contracts' shapes.
func checkRouteNamespace(unit string, steps []string) error {
	if len(steps) < 2 || steps[0] != "paths" {
		return nil
	}
	prefix := "/v1/ext/" + unit
	if steps[1] == prefix || strings.HasPrefix(steps[1], prefix+"/") {
		return nil
	}
	return fmt.Errorf("path %s is outside the unit's route namespace — an extension declares routes under %s", steps[1], prefix)
}

// addNode walks steps through the base document and adds the final key.
//
// Every parent must already exist: a fragment that invented `webhooks:`
// because it misspelled `paths:` would otherwise publish a whole block the
// contract's readers ignore. And the final key must NOT exist: this composer
// only ADDS, so an extension can never redefine a core operation, schema or
// kind — the merged contract is the base plus, never the base altered.
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
