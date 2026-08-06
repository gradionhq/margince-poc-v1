// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The reset-data 200 body as a fitness function.
//
// The contract declares that response INLINE, so oapi-codegen synthesizes no Go
// type for it and the drift gate has nothing to compare: the hand-written
// resetDataResponse struct is the only spelling on the wire, and nothing else
// checks it against api/crm.yaml. A field added to the contract and forgotten in
// the struct ships a 200 body missing a key every client is told is required —
// which is how an operator reads "0 objects deleted" as "nothing to delete"
// rather than "this build cannot tell you".
//
// Both sets are DERIVED — the contract's `required` list and the struct's json
// tags — so adding a surface to the reset means changing both or failing here.
// Resolution is textual on the YAML side (contractrefs_test.go's reason: the
// root fitness-test package stays free of parser deps) and go/ast on the Go
// side, reusing jsonFieldName from requiredbodyids_test.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	contractPath        = "api/crm.yaml"
	resetResponseSource = "internal/compose/datareset.go"
	resetResponseType   = "resetDataResponse"
)

// requiredList captures the members of a flow-style `required: [a, b, c]`.
var requiredList = regexp.MustCompile(`^\s*required:\s*\[([^\]]*)\]`)

func TestResetDataResponseMatchesTheContract(t *testing.T) {
	contract := resetDataRequiredFields(t)
	if len(contract) == 0 {
		t.Fatalf("no required field list found for the resetData 200 response in %s — "+
			"the contract scan is reading the wrong shape", contractPath)
	}
	implemented := jsonTagsOfStruct(t, resetResponseSource, resetResponseType)
	if len(implemented) == 0 {
		t.Fatalf("no json-tagged fields found on %s in %s — the struct scan is reading the wrong shape",
			resetResponseType, resetResponseSource)
	}
	if strings.Join(contract, ",") != strings.Join(implemented, ",") {
		t.Errorf("the reset-data 200 body disagrees with the contract:\n  %s requires %v\n  %s declares  %v\n"+
			"Every required key must be on the wire — a missing one reads to the caller as a zero, not as absent.",
			contractPath, contract, resetResponseType, implemented)
	}
}

// resetDataRequiredFields returns the sorted `required` list of the resetData
// operation's 200 response schema.
//
// The scan is anchored twice inside the operation's OWN block — the '200'
// response, then the first required list under it — because the same operation
// declares a required request-body field, and nearly every other operation in
// the file declares a required list of its own.
func resetDataRequiredFields(t *testing.T) []string {
	t.Helper()
	block := operationBlock(t, "resetData")
	response := indexOfLine(block, "'200':")
	if response < 0 {
		t.Fatalf("%s: the resetData operation declares no 200 response", contractPath)
	}
	for _, line := range block[response:] {
		m := requiredList.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var fields []string
		for _, name := range strings.Split(m[1], ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				fields = append(fields, trimmed)
			}
		}
		sort.Strings(fields)
		return fields
	}
	return nil
}

// operationBlock returns the contract lines from the named operation's
// operationId up to the next one — the bound that keeps a missing declaration
// from silently matching a neighbour's.
func operationBlock(t *testing.T, operationID string) []string {
	t.Helper()
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := indexOfLine(lines, "operationId: "+operationID)
	if start < 0 {
		t.Fatalf("%s declares no %s operation", contractPath, operationID)
	}
	for i, line := range lines[start+1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "operationId:") {
			return lines[start : start+1+i]
		}
	}
	return lines[start:]
}

// indexOfLine reports the first line whose trimmed text equals want, or -1.
func indexOfLine(lines []string, want string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) == want {
			return i
		}
	}
	return -1
}

// jsonTagsOfStruct returns the sorted wire names the named struct's fields bind.
func jsonTagsOfStruct(t *testing.T, path, typeName string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var tags []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != typeName {
			return true
		}
		structType, isStruct := spec.Type.(*ast.StructType)
		if !isStruct {
			return true
		}
		for _, field := range structType.Fields.List {
			if name := jsonFieldName(field); name != "" {
				tags = append(tags, name)
			}
		}
		return false
	})
	sort.Strings(tags)
	return tags
}
