// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command contract-subset cuts a small components-only document out of the
// authoritative contract, so a named handful of schemas can be generated into
// the PUBLISHED extension surface without publishing the rest of the contract
// with them.
//
// Why a subset rather than the whole contract. Generating models over crm.yaml
// would publish thousands of types — every AI, comms and finance schema —
// frozen the moment the first release tag lands. It also cannot compile where
// it has to: backend/pkg is a strict-mode depguard zone (`pkg-purity`) allowing
// only the standard library and backend/pkg itself, and whole-contract models
// reference openapi_types and the oapi-codegen runtime for union helpers. The
// subset avoids both by construction: it carries only what the extension port's
// surface takes and returns, with the two formats that reach for a non-stdlib
// type rewritten to plain strings.
//
// Two failure modes this refuses rather than degrades into, because both
// produce a plausible-looking package that is wrong:
//
//   - A name that is not in the contract. Left to codegen it is simply absent
//     from the output, and the missing type surfaces as a compile error in a
//     package nobody was looking at.
//   - A $ref the subset does not carry. The generated file would reference a
//     type that is not there. Refs are followed transitively instead, so naming
//     one schema brings everything it needs.
//
// Usage:
//
//	contract-subset -in api/crm.yaml -out .build/subset.yaml -schemas Activity,CreateActivityRequest
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	in := flag.String("in", "", "authoritative contract to cut from")
	out := flag.String("out", "", "components-only subset (build artifact)")
	names := flag.String("schemas", "", "comma-separated component schema names to keep, with their refs")
	flag.Parse()
	if *in == "" || *out == "" || *names == "" {
		log.Fatal("contract-subset: -in, -out and -schemas are required")
	}

	src, err := os.ReadFile(*in) // #nosec G304 -- the contract path is a build argument, not user input
	if err != nil {
		log.Fatalf("contract-subset: %v", err)
	}
	subset, err := cut(src, strings.Split(*names, ","))
	if err != nil {
		log.Fatalf("contract-subset: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		log.Fatalf("contract-subset: %v", err)
	}
	if err := os.WriteFile(*out, subset, 0o600); err != nil {
		log.Fatalf("contract-subset: %v", err)
	}
}

// cut returns a standalone document holding the named schemas and everything
// they reference.
func cut(src []byte, names []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}
	schemas := mapping(mapping(root(&doc), "components"), "schemas")
	if schemas == nil {
		return nil, fmt.Errorf("the source declares no components.schemas")
	}

	kept := map[string]*yaml.Node{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := keep(schemas, name, kept); err != nil {
			return nil, err
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("no schema was named, so the subset would generate an empty package")
	}
	return marshalSubset(kept)
}

// keep copies one schema into the subset along with every schema it references,
// following refs depth-first. A name already kept ends the walk, so a cycle
// terminates.
func keep(schemas *yaml.Node, name string, kept map[string]*yaml.Node) error {
	if _, done := kept[name]; done {
		return nil
	}
	schema := mapping(schemas, name)
	if schema == nil {
		return fmt.Errorf("the contract declares no schema %q, and generating without it would leave the published package missing a type", name)
	}
	rewriteFormats(schema)
	kept[name] = schema
	for _, ref := range refsIn(schema) {
		target, ok := strings.CutPrefix(ref, "#/components/schemas/")
		if !ok {
			return fmt.Errorf("schema %q references %q, which is not a component schema this subset can carry", name, ref)
		}
		if err := keep(schemas, target, kept); err != nil {
			return fmt.Errorf("through %q: %w", name, err)
		}
	}
	return nil
}

// nonStdlibFormats are the string formats whose generated Go type is not in the
// standard library. They are rewritten to plain strings because the published
// package may import nothing outside it, and because a unit handles both as
// strings anyway — the precedent is extension.Caller.UserID, which is a string
// for the same reason. date-time is deliberately absent: it generates
// time.Time, which is stdlib.
var nonStdlibFormats = []string{"uuid", "email"}

// rewriteFormats stamps x-go-type: string on every node whose format would
// otherwise generate a type the published package cannot import.
func rewriteFormats(node *yaml.Node) {
	if node.Kind == yaml.MappingNode {
		format := scalar(node, "format")
		_, isString := lookup(node, "x-go-type")
		if format != "" && !isString {
			for _, f := range nonStdlibFormats {
				if format == f {
					setScalar(node, "x-go-type", "string")
					break
				}
			}
		}
	}
	for _, child := range node.Content {
		rewriteFormats(child)
	}
}

// refsIn collects every $ref value under a node.
func refsIn(node *yaml.Node) []string {
	var refs []string
	if node.Kind == yaml.MappingNode {
		if ref := scalar(node, "$ref"); ref != "" {
			refs = append(refs, ref)
		}
	}
	for _, child := range node.Content {
		refs = append(refs, refsIn(child)...)
	}
	return refs
}

// marshalSubset writes the kept schemas as a minimal 3.1 document. It carries
// an empty paths object because a document without one is not a contract, and
// oapi-codegen is run over it with pruning OFF — with no operations there are no
// incoming refs, and a pruning pass would delete every schema in it.
func marshalSubset(kept map[string]*yaml.Node) ([]byte, error) {
	names := make([]string, 0, len(kept))
	for name := range kept {
		names = append(names, name)
	}
	sort.Strings(names)

	schemas := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range names {
		schemas.Content = append(schemas.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: name}, kept[name])
	}
	doc := &yaml.Node{Kind: yaml.MappingNode}
	setScalar(doc, "openapi", "3.1.0")
	info := &yaml.Node{Kind: yaml.MappingNode}
	setScalar(info, "title", "Margince extension surface (subset)")
	setScalar(info, "version", "1.0.0")
	setNode(doc, "info", info)
	setNode(doc, "paths", &yaml.Node{Kind: yaml.MappingNode})
	components := &yaml.Node{Kind: yaml.MappingNode}
	setNode(components, "schemas", schemas)
	setNode(doc, "components", components)

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-marshaling the subset: %w", err)
	}
	return out, nil
}

// root unwraps a parsed document to its top-level mapping.
func root(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	return doc
}

// mapping returns the mapping value stored under key, or nil.
func mapping(node *yaml.Node, key string) *yaml.Node {
	value, ok := lookup(node, key)
	if !ok {
		return nil
	}
	return value
}

// scalar returns the scalar value stored under key, or "".
func scalar(node *yaml.Node, key string) string {
	value, ok := lookup(node, key)
	if !ok || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

// lookup finds a mapping's value node by key.
func lookup(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

func setScalar(node *yaml.Node, key, value string) {
	setNode(node, key, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

func setNode(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}
