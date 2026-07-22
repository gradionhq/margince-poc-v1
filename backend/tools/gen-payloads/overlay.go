// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// downgradeToOpenAPI30 rewrites an OpenAPI 3.1 document into the 3.0.3 subset
// oapi-codegen's kin-openapi backend consumes, using the same transform as the
// repo's contract-overlay tool so the two pipelines agree on what "3.0-safe"
// means:
//   - openapi: 3.1.x -> 3.0.3
//   - type: [T, 'null'] -> type: T + nullable: true
//   - schema-level examples: [x, …] -> example: x (3.0 has no plural form)
//
// Anything else 3.1-specific fails loudly rather than degrading silently.
func downgradeToOpenAPI30(src []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}
	if err := downgrade(&doc); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("re-marshaling downgraded doc: %w", err)
	}
	return out, nil
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

// rewriteTypeUnion turns type: [T, 'null'] into type: T + nullable: true on
// the mapping that holds it.
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
