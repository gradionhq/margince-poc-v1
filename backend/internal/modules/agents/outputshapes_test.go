// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden rewrites the committed schemas instead of comparing against
// them. It exists so a deliberate shape change is one command, and it is a flag
// rather than an env var so a reader of the failure message can see what to run.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/outputshapes.json from the current result types")

// goldenPath is the committed rendering of every declared result schema.
const goldenPath = "testdata/outputshapes.json"

// The schemas are derived from Go types, so they cannot DRIFT from those types
// — but that also means they never appear in a diff, and a result shape is
// exactly the kind of thing that should not change without someone looking at
// it. This renders them to a committed file so a change to a struct tag, an
// omitempty, or a field's type shows up as a reviewable diff.
func TestDeclaredOutputSchemasMatchTheCommittedRendering(t *testing.T) {
	rendered := renderDeclaredSchemas(t)
	if *updateGolden {
		if err := os.WriteFile(goldenPath, rendered, 0o600); err != nil {
			t.Fatalf("writing %s: %v", goldenPath, err)
		}
		return
	}
	committed, err := os.ReadFile(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPath, err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(rendered)) {
		t.Errorf("the declared result schemas differ from %s.\n"+
			"A result shape moved. If that was the intention, re-render with:\n"+
			"  go test ./internal/modules/agents/ -run TestDeclaredOutputSchemas -update-golden\n"+
			"and read the diff — it is what a client is now promised.", goldenPath)
	}
}

// renderDeclaredSchemas is every registered tool's advertised output schema,
// keyed by tool and indented, so the committed file reads as documentation
// rather than as one line of JSON.
func renderDeclaredSchemas(t *testing.T) []byte {
	t.Helper()
	shapes := map[string]json.RawMessage{}
	for _, spec := range fullRegistry(t).Specs() {
		shapes[spec.Name] = spec.OutputSchema
	}
	out, err := json.MarshalIndent(shapes, "", "  ")
	if err != nil {
		t.Fatalf("rendering the declared schemas: %v", err)
	}
	return append(out, '\n')
}

// Every tool declares a schema, and the two that declare only an object say why
// in their own source. The list is here rather than derived because it is a
// statement about intent: a THIRD tool falling back to an object is a decision
// someone has to make deliberately, and this is where they are asked to.
func TestOnlyTheDeclaredExceptionsAdvertiseABareObject(t *testing.T) {
	allowed := map[string]bool{"enrich": true}
	for _, spec := range fullRegistry(t).Specs() {
		if spec.OutputSchema == nil {
			t.Errorf("%s declares no output schema at all", spec.Name)
			continue
		}
		var declared struct {
			Type string `json:"type"`
			// Decoded as a MAP rather than raw bytes: `"properties": {}` is a
			// present member describing nothing, and a length check on the raw
			// bytes would read it as a shape.
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(spec.OutputSchema, &declared); err != nil {
			t.Errorf("%s: output schema is not valid JSON: %v", spec.Name, err)
			continue
		}
		if len(declared.Properties) == 0 && !allowed[spec.Name] {
			t.Errorf("%s advertises a bare object, which tells a caller nothing it could plan on. "+
				"Give it the type its handler marshals, or add it here with the reason in its own spec",
				spec.Name)
		}
	}
}
