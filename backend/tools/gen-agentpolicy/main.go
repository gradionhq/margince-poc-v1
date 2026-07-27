// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-agentpolicy derives the ADR-0055 REST admission table from
// the contract: for every crm.yaml operation that declares an agent policy
// it emits one entry mapping the chi route (method + path pattern) to the
// backing MCP tool verb + declared tier (`x-mcp-tool`) or the
// `x-agent-access` class (`human-only` | `auth-bootstrap`).
//
// It is also the contract drift-lint (interfaces.md §2 fail-closed rule):
// a MUTATING operation carrying NEITHER annotation fails generation, so an
// un-tiered endpoint cannot ship — the gate would default-deny it at
// runtime anyway, but the lint keeps the contract honest at build time.
//
// Reads are emitted too, and the asymmetry is deliberate on both sides. A
// read carrying no annotation is ordinary agent-readable data, so it is not
// a defect and gets no row: the contract annotates the EXCEPTIONS to agent
// readability, not the rule. But an annotated read is exactly as binding as
// an annotated write, and emitting only mutating rows is what previously
// made `x-agent-access: human-only` on a `get:` a comment — dropped at
// build time, with the runtime gate structurally unable to see it.
package main

import (
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods maps every operation key the contract may carry onto the
// method the chi router registers. mutating names the subset the
// fail-closed drift-lint applies to.
var httpMethods = map[string]string{
	"get": "GET", "head": "HEAD", "options": "OPTIONS",
	"post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE",
}

var mutating = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true}

type policy struct {
	Route      string // "METHOD /v1/path/{id}" — the chi route pattern the gate sees
	Op         string // crm.yaml operationId
	Access     string // "tool" | "human-only" | "auth-bootstrap"
	Tool       string // x-mcp-tool verb (Access == "tool")
	RecordType string // x-mcp-tool record_type ("" when the tool is not record-typed)
	Tier       string // x-mcp-tool tier: auto_execute | confirmation_required | dynamic
}

func main() {
	in := flag.String("in", "", "authoritative crm.yaml")
	out := flag.String("out", "", "generated Go table")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("gen-agentpolicy: -in and -out are required")
	}

	src, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("gen-agentpolicy: %v", err)
	}
	// Path items mix operations with non-operation keys (parameters,
	// summary), so each method node is decoded individually.
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		log.Fatalf("gen-agentpolicy: parsing %s: %v", *in, err)
	}

	policies, defects := derivePolicies(doc.Paths)
	if len(defects) > 0 {
		sort.Strings(defects)
		log.Fatalf("gen-agentpolicy: the contract violates the ADR-0055 fail-closed invariant:\n  %s", strings.Join(defects, "\n  "))
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Route < policies[j].Route })

	// The emitted table must be gofmt-clean like any committed Go file
	// (map-literal alignment shifts as route keys change length).
	formatted, err := format.Source([]byte(renderTable(policies)))
	if err != nil {
		log.Fatalf("gen-agentpolicy: formatting the generated table: %v", err)
	}
	if err := os.WriteFile(*out, formatted, 0o600); err != nil {
		log.Fatalf("gen-agentpolicy: %v", err)
	}
	fmt.Printf("%d agent policies generated\n", len(policies))
}

// derivePolicies classifies every mutating operation and collects the
// contract defects the fail-closed rule refuses to ship.
func derivePolicies(paths map[string]map[string]yaml.Node) ([]policy, []string) {
	var policies []policy
	var defects []string
	for path, item := range paths {
		for method, node := range item {
			httpMethod, isOperation := httpMethods[method]
			if !isOperation {
				continue // parameters, summary, and the other path-item keys
			}
			var op struct {
				OperationID string `yaml:"operationId"`
				MCPTool     *struct {
					Verb       string `yaml:"verb"`
					RecordType string `yaml:"record_type"`
					Tier       string `yaml:"tier"`
				} `yaml:"x-mcp-tool"`
				AgentAccess string `yaml:"x-agent-access"`
			}
			if err := node.Decode(&op); err != nil {
				log.Fatalf("gen-agentpolicy: %s %s: %v", httpMethod, path, err)
			}
			p := policy{Route: httpMethod + " /v1" + path, Op: op.OperationID}
			switch {
			case op.AgentAccess == "human-only" || op.AgentAccess == "auth-bootstrap":
				p.Access = op.AgentAccess
			case op.AgentAccess != "":
				defects = append(defects, fmt.Sprintf("%s %s (%s): unknown x-agent-access %q", httpMethod, path, op.OperationID, op.AgentAccess))
				continue
			case op.MCPTool != nil:
				p.Access = "tool"
				p.Tool = op.MCPTool.Verb
				p.RecordType = op.MCPTool.RecordType
				p.Tier = op.MCPTool.Tier
				if p.Tool == "" || (p.Tier != "auto_execute" && p.Tier != "confirmation_required" && p.Tier != "dynamic") {
					defects = append(defects, fmt.Sprintf("%s %s (%s): x-mcp-tool needs a verb and an auto_execute|confirmation_required|dynamic tier", httpMethod, path, op.OperationID))
					continue
				}
			case mutating[httpMethod]:
				defects = append(defects, fmt.Sprintf("%s %s (%s): mutating operation carries neither x-mcp-tool nor x-agent-access", httpMethod, path, op.OperationID))
				continue
			default:
				// An unannotated read: ordinary agent-readable data. No row,
				// and no defect — the gate admits a read it has no policy for,
				// which is the opposite of its mutating default and is why an
				// unannotated GET does not need to say so.
				continue
			}
			policies = append(policies, p)
		}
	}
	return policies, defects
}

// renderTable emits the generated Go source for the admission table.
func renderTable(policies []policy) string {
	var b strings.Builder
	b.WriteString(`// Code generated by tools/gen-agentpolicy from api/crm.yaml. DO NOT EDIT.

package compose

// agentPolicy is one mutating contract operation's admission class for
// AGENT (Passport) principals (ADR-0055): either the MCP tool verb whose
// tier governs it on every transport, or an x-agent-access marker. The
// gate default-denies any mutating route absent from this table.
type agentPolicy struct {
	Op         string // crm.yaml operationId
	Access     string // "tool" | "human-only" | "auth-bootstrap"
	Tool       string // backing MCP tool verb (Access == "tool")
	RecordType string // the record type the operation targets
	Tier       string // contract-declared tier: auto_execute | confirmation_required | dynamic
}

// agentPolicies is keyed by "METHOD <chi route pattern>" as the generated
// router registers it (BaseURL /v1 included).
var agentPolicies = map[string]agentPolicy{
`)
	for _, p := range policies {
		fmt.Fprintf(&b, "\t%q: {Op: %q, Access: %q, Tool: %q, RecordType: %q, Tier: %q},\n",
			p.Route, p.Op, p.Access, p.Tool, p.RecordType, p.Tier)
	}
	b.WriteString("}\n")
	return b.String()
}
