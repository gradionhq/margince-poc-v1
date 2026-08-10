// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A field the contract PROMISES and nobody WRITES is invisible.
//
// `make drift` proves the generated Go matches api/crm.yaml. Nothing proves a
// handler fills what the contract advertises, so a schema property with no
// producer ships as permanently absent — and absent, on these composites,
// reads as a fact about the account. `last_meeting_at` sat on
// Organization360Health for exactly that reason: promised, never set, and
// rendering as "no meeting" on every company in the product.
//
// This is the shape of bug that adding fields to a composite creates, so it is
// derived rather than listed: the struct's own field set is the obligation, and
// a new property joins the check by existing.
//
// SCOPE is deliberately the page composites rather than every schema. Most
// contract types are request bodies or are filled by a mapper the compiler
// already forces to be total; these three are assembled field by field from
// separate readers, which is where a field quietly goes unwritten (ADR-0095).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The composites the company record page reads. Each is assembled by hand from
// several sources, which is the shape this gate guards.
var producedSchemas = []string{
	"Organization360Health",
	"OrganizationGrowthFit",
	"Organization360Suggestion",
}

// A Go field declaration inside a generated struct: `\tName Type `json:...“.
var genField = regexp.MustCompile("^\t([A-Z][A-Za-z0-9]*)\\s+\\S")

func TestEveryContractPropertyOnAPageCompositeHasAProducer(t *testing.T) {
	fields := map[string][]string{}
	for _, schema := range producedSchemas {
		found := generatedFieldsOf(t, schema)
		if len(found) == 0 {
			t.Fatalf("%s: no fields found in the generated contract — the struct was renamed or removed, and this gate is now watching nothing", schema)
		}
		fields[schema] = found
	}

	// Every hand-written Go file under internal/, as one corpus. A producer is
	// any non-generated file that names the field — assignment, struct literal
	// or mapper alike, because all three are ways of filling it.
	corpus := handWrittenGoUnderInternal(t)

	for _, schema := range producedSchemas {
		for _, field := range fields[schema] {
			if strings.Contains(corpus, field) {
				continue
			}
			t.Errorf(
				"%s.%s is in the contract and no hand-written file under internal/ mentions it.\n"+
					"A property nobody writes renders as absent forever, and on this composite absent is\n"+
					"a claim about the account. Produce it where the section is assembled, or take it out\n"+
					"of api/crm.yaml.",
				schema, field,
			)
		}
	}
}

// generatedFieldsOf reads one struct out of the generated contract and returns
// its exported field names. Textual, like its sibling gates: the root fitness
// package stays free of parser dependencies (the arch-lint boundary).
func generatedFieldsOf(t *testing.T, schema string) []string {
	t.Helper()
	const path = "internal/contracts/api_gen.go"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the generated contract: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	open := "type " + schema + " struct {"
	var fields []string
	inside := false
	for _, line := range lines {
		if !inside {
			inside = line == open
			continue
		}
		if line == "}" {
			break
		}
		if match := genField.FindStringSubmatch(line); match != nil {
			fields = append(fields, match[1])
		}
	}
	return fields
}

// handWrittenGoUnderInternal is every .go file under internal/ that is not
// generated and not a test, concatenated. Tests are excluded on purpose: a
// field only a test writes has no producer in the product.
func handWrittenGoUnderInternal(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir("internal", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_gen.go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// The generated contract package is the source of the obligation, never
		// evidence of it being met.
		if strings.HasPrefix(path, filepath.Join("internal", "contracts")) {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out.Write(raw)
		out.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	return out.String()
}
