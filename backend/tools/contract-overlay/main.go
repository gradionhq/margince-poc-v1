// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command contract-overlay downgrades the authoritative OpenAPI 3.1
// contract to 3.0.3 at generate time so oapi-codegen (kin-openapi, 3.0)
// can consume it (B-EP01.9a). The 3.1 crm.yaml stays the single source of
// truth; the overlay output lives in a gitignored build dir and is never
// committed back.
//
// Transforms applied:
//   - openapi: 3.1.x → 3.0.3
//   - type: [T, 'null'] → type: T + nullable: true
//   - schema-level examples: [x, …] → example: x (3.0 has no plural form)
//
// Anything else 3.1-specific fails loudly rather than degrading silently.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func main() {
	in := flag.String("in", "", "authoritative 3.1 contract")
	out := flag.String("out", "", "3.0.3 overlay output (build artifact)")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("contract-overlay: -in and -out are required")
	}

	src, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		log.Fatalf("contract-overlay: parsing %s: %v", *in, err)
	}

	if err := downgrade(&doc); err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}

	converted, err := yaml.Marshal(&doc)
	if err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}
	if err := os.WriteFile(*out, converted, 0o600); err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}
}

func downgrade(n *yaml.Node) error {
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			if err := downgrade(c); err != nil {
				return err
			}
		}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			if err := downgrade(c); err != nil {
				return err
			}
		}
		return nil
	}

	// Mapping content alternates key, value.
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		switch key.Value {
		case "openapi":
			if val.Kind == yaml.ScalarNode {
				val.Value = "3.0.3"
			}
		case "type":
			if val.Kind == yaml.SequenceNode {
				if err := rewriteTypeUnion(n, val); err != nil {
					return err
				}
			}
		case "examples":
			// Only the schema-level plural form (a sequence) is 3.1-only;
			// media-type examples (a mapping) exist in 3.0 and stay.
			if val.Kind == yaml.SequenceNode && len(val.Content) > 0 {
				key.Value = "example"
				*val = *val.Content[0]
			}
		}
	}
	for i := 1; i < len(n.Content); i += 2 {
		if err := downgrade(n.Content[i]); err != nil {
			return err
		}
	}
	return nil
}

// rewriteTypeUnion turns type: [T, 'null'] into type: T + nullable: true
// on the mapping that holds it.
func rewriteTypeUnion(mapping, union *yaml.Node) error {
	var concrete string
	sawNull := false
	for _, t := range union.Content {
		if t.Value == "null" {
			sawNull = true
			continue
		}
		if concrete != "" {
			return fmt.Errorf("type union %v has two concrete types; 3.0 cannot express it", nodeValues(union))
		}
		concrete = t.Value
	}
	if concrete == "" || !sawNull {
		return fmt.Errorf("type union %v is not the [T, null] shape", nodeValues(union))
	}

	*union = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: concrete}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "nullable"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
	)
	return nil
}

func nodeValues(n *yaml.Node) []string {
	vals := make([]string, len(n.Content))
	for i, c := range n.Content {
		vals[i] = c.Value
	}
	return vals
}
