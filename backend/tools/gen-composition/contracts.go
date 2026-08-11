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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
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
		// Checked BEFORE os.ReadFile, because the failure mode of a
		// non-regular file is not a bad read, it is no read: opening a FIFO
		// blocks until something writes to it, so composition would hang here
		// with no output rather than refuse. The tree digest would reject the
		// entry, but only after this loop has already tried to read it.
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("extensions/%s: %s/%s is not a regular file — the api layer holds overlay documents, and this composer will not read through a link, a device or a pipe", name, apiLayer, e.Name())
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
	// digested as part of the unit tree while composing nothing. Spelled with
	// errors.Is, matching gen-aitasks' rejectSecondDocument: matching on the
	// error's TEXT would break silently on any yaml.v3 rewording, and "breaks
	// silently" here means readmitting a smuggled second document.
	var second yaml.Node
	err := dec.Decode(&second)
	switch {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, fmt.Errorf("reading past the first document: %w", err)
	default:
		return nil, fmt.Errorf("the fragment carries more than one YAML document — only the first is composed; keep every action in one document")
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
		if err := rejectDuplicateKeys(&a.Update, a.Target); err != nil {
			return nil, err
		}
	}
	return doc.Actions, nil
}

// rejectDuplicateKeys walks an update payload for a mapping key declared
// twice.
//
// KnownFields(true) does NOT cover this, and neither does yaml.v3's own
// uniqueKeys default: a `yaml.Node` destination is assigned the raw node
// (decode.go's `d.unmarshal` Node arm) without going through the mapping
// decoder that enforces either setting. `update:` is exactly such a
// destination — it has to be, since it holds arbitrary contract fragments — so
// a duplicate inside it would ride verbatim into the composed contract, and
// which copy wins would then be decided by whichever downstream parser reads
// the merged file. That is the same question this whole change set exists to
// answer: can a second declaration of something reach the emitters unseen?
func rejectDuplicateKeys(node *yaml.Node, target string) error {
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if seen[key] {
				return fmt.Errorf("target %s: key %q is declared twice in the update — which copy survives would be decided by whichever parser reads the merged contract", target, key)
			}
			seen[key] = true
		}
	}
	// Sequences carry mappings too (an OpenAPI parameters list), so the walk
	// covers every child rather than mapping values alone.
	for _, child := range node.Content {
		if err := rejectDuplicateKeys(child, target); err != nil {
			return err
		}
	}
	return nil
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
		// Only when something was actually merged: with no fragments `merged`
		// IS the committed base, byte for byte, and whether the core contract
		// validates is the core contract's own lanes' business — an empty
		// extension tree must not be able to fail composition for it.
		if len(frags) > 0 {
			if err := validateMergedOpenAPI(merged); err != nil {
				return nil, fmt.Errorf("composing %s/%s (fragments from %s): %w",
					apiLayer, base, fragmentSources(frags), err)
			}
		}
		out[base] = merged
	}
	return out, nil
}

// fragmentSources names the units whose fragments produced a merged document,
// for the one message that has to point a reader at a file to edit: the merge
// is additive and order-independent, so a validation failure names the SET
// rather than one action the way the merge's own refusals can.
func fragmentSources(frags []contractFragment) string {
	sources := make([]string, 0, len(frags))
	for _, f := range frags {
		sources = append(sources, f.Source)
	}
	return strings.Join(sources, ", ")
}

// validateMergedOpenAPI proves the document a unit's fragments produced is
// still a loadable, valid OpenAPI document.
//
// The merge is STRUCTURAL by construction: it checks the target's grammar, the
// ownership wall and the route namespace, then splices the unit's YAML in
// verbatim. Nothing on that path knows what an OpenAPI node MEANS, so a
// fragment that is well-formed YAML and nonsense OpenAPI merges cleanly and is
// published — a `$ref` at a component nobody declares, a `minLength` holding a
// word, a response map spliced where an operation was expected. The composed
// contract is what the generated client types, the docs and the operator
// manifest are built from, so today that surfaces as an unresolvable client
// build or a document that lies, attributed to nothing. This is where it
// becomes a named refusal of the extension that caused it.
//
// It complements rather than duplicates the per-operation reader: extverbs.go
// validates every extension OPERATION strictly (its x-mcp-tool request, its
// inline schemas, its $refs), but a fragment may also add a
// `components.schemas` node, and nothing read that at all.
//
// Only documents that ARE OpenAPI are checked, detected by the `openapi:` key
// rather than a second hardcoded list beside composedContractBases —
// ai-tasks.yaml and jobs.yaml are custom DSLs whose own generators (gen-aitasks,
// gen-jobs) validate them, and a new base is then covered or skipped by what it
// is instead of by a list somebody has to remember to extend.
//
// EXAMPLES ARE EXEMPT, deliberately. kin-openapi otherwise validates every
// `example` against its schema, and the committed core crm.yaml already carries
// examples that fail it (ColdStartProposal's omit the required `source_kind`).
// Switching that on here would refuse every composition for a pre-existing core
// defect this gate has no standing to rule on, which is how a gate gets turned
// off. The structural claim is the one being made.
func validateMergedOpenAPI(merged []byte) error {
	// `any`, not `string`: an `openapi:` written unquoted (3.1) is a YAML float
	// and would fail a string decode here, turning a defect kin-openapi reports
	// precisely into an unrelated complaint from this probe.
	var probe struct {
		OpenAPI any `yaml:"openapi"`
	}
	if err := yaml.Unmarshal(merged, &probe); err != nil {
		return fmt.Errorf("re-reading the merged contract: %w", err)
	}
	if probe.OpenAPI == nil {
		return nil
	}
	// External references stay off (the loader's default): a composed contract
	// resolves against itself, and a fragment that could reach the network here
	// would make the published document depend on what a build host can fetch.
	doc, err := openapi3.NewLoader().LoadFromData(merged)
	if err != nil {
		return fmt.Errorf("the merged contract is not a loadable OpenAPI document: %w", err)
	}
	if err := doc.Validate(context.Background(), openapi3.DisableExamplesValidation()); err != nil {
		return fmt.Errorf("the merged contract is not a valid OpenAPI document: %w", err)
	}
	return nil
}
