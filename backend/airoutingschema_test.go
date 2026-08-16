// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// What the EDITOR accepts, checked against what the parser accepts.
//
// config/ai-routing.schema.json is what a YAML language server reads while an
// operator types, so it is the first answer they get about whether a binding is
// legal — hours before a process ever boots. Its enums are drift-tested in
// schema_test.go, but an enum is not the interesting half: the `input:` rules
// are conditionals, and a conditional can be subtly inverted while every enum
// still matches.
//
// So this runs a real JSON Schema validator over the generated schema and
// asserts the same acceptances the parser makes. The two are allowed to differ
// in the MESSAGE they give; they are not allowed to differ in the ANSWER. A
// schema that green-lights a binding the parser refuses at boot is worse than
// no schema, because the operator was told it was fine.

import (
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func compiledRoutingSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	const path = "../config/ai-routing.schema.json"
	raw, err := os.Open(path)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer func() {
		if cerr := raw.Close(); cerr != nil {
			t.Errorf("close schema: %v", cerr)
		}
	}()
	doc, err := jsonschema.UnmarshalJSON(raw)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("ai-routing.json", doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	sch, err := c.Compile("ai-routing.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// The `input:` acceptance matrix, asserted against the editor's authority and
// the runtime's in one table so the two cannot drift apart silently.
func TestTheSchemaAndTheParserAgreeOnEveryInputDeclaration(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "k")
	sch := compiledRoutingSchema(t)

	tiered := func(binding string) string {
		return "profile: eu_hosted\ntiers:\n  premium: {" + binding + "}\nembeddings: {provider: gemini, model: e}\n"
	}
	for name, tc := range map[string]struct {
		yaml  string
		legal bool
	}{
		// The field belongs only where carriage depends on the bound model.
		"declared on openai_compatible": {tiered(`provider: openai_compatible, base_url: https://x, model: m, input: [text, image]`), true},
		"declared on vllm":              {tiered(`provider: vllm, model: m, input: [text, image]`), true},
		"declared on gemini":            {tiered(`provider: gemini, model: m, input: [text, image]`), false},
		"declared on anthropic":         {tiered(`provider: anthropic, model: m, input: [text, image]`), false},
		"declared on openai":            {tiered(`provider: openai, model: m, input: [text, image]`), false},
		"declared on ollama":            {tiered(`provider: ollama, model: m, input: [text, image]`), false},
		// Omitting it is the text-only default every existing config relies on.
		"omitted on gemini":            {tiered(`provider: gemini, model: m`), true},
		"omitted on openai_compatible": {tiered(`provider: openai_compatible, base_url: https://x, model: m`), true},
		// The value rules.
		"unknown modality":  {tiered(`provider: vllm, model: m, input: [text, pdf]`), false},
		"missing text":      {tiered(`provider: vllm, model: m, input: [image]`), false},
		"empty list":        {tiered(`provider: vllm, model: m, input: []`), false},
		"repeated modality": {tiered(`provider: vllm, model: m, input: [text, image, image]`), false},
		// The embeddings lane sends no attachments.
		"declared on the embeddings lane": {
			"profile: eu_hosted\ntiers:\n  premium: {provider: gemini, model: m}\n" +
				"embeddings: {provider: openai_compatible, base_url: https://x, model: e, input: [text, image]}\n", false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var doc any
			if err := yaml.Unmarshal([]byte(tc.yaml), &doc); err != nil {
				t.Fatalf("fixture is not yaml: %v", err)
			}
			// The validator works on JSON types; yaml.v3 already decodes into
			// map[string]any for string keys, which is what it expects.
			schemaOK := sch.Validate(doc) == nil
			_, parseErr := ai.ParseRouting([]byte(tc.yaml))
			parserOK := parseErr == nil

			if schemaOK != tc.legal {
				t.Errorf("schema accepted=%v, want %v", schemaOK, tc.legal)
			}
			if parserOK != tc.legal {
				t.Errorf("parser accepted=%v, want %v (err: %v)", parserOK, tc.legal, parseErr)
			}
			if schemaOK != parserOK {
				t.Errorf("the editor and the runtime disagree: schema accepted=%v, parser accepted=%v (err: %v)",
					schemaOK, parserOK, parseErr)
			}
		})
	}
}
