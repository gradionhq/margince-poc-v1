// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// API-CC-8 as a fitness function: a replay is a read, so every operation
// the idempotency middleware can replay must either re-probe the row scope
// of the record its recorded body carries, or say in writing that the body
// carries no such record.
//
// The operation's 2xx response names its schema, so the route's obligation
// follows from the contract once the SHAPES are classified. Classifying them —
// rowScopedResponses below — is a human judgment this gate cannot make for
// itself; what it does is hold every route answering a given schema to the
// same answer, which is what makes a fix here a class fix rather than a
// one-route one. It catches four kinds of drift: a new replayable route
// landing in neither map, an exemption claimed for a body that does carry a
// record, a reasonless exemption, and a probe aimed at the wrong table or
// field.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rowScopedResponses maps a contract response schema to the row-scoped record
// it carries and where that record's id sits in the body. Expressed over
// SCHEMAS rather than routes on purpose: routes are added constantly, response
// shapes rarely, so a new route inherits the right answer for free.
//
// A shape belongs here whether it IS a row-scoped record or merely POINTS at
// one. The second half is the easier one to miss and the more dangerous: a
// body with no owner column of its own still hands back whatever its parent
// record contains, so "this table has no owner_id" is never on its own a
// reason to skip the probe.
var rowScopedResponses = map[string]expectedTarget{
	"Person":              {table: "person", idPath: "id"},
	"Organization":        {table: "organization", idPath: "id"},
	"Deal":                {table: "deal", idPath: "id"},
	"Lead":                {table: "lead", idPath: "id"},
	"Project":             {table: "project", idPath: "id"},
	"Activity":            {table: "activity", idPath: "id"},
	"VoiceProfile":        {table: "voice_profile", idPath: "id"},
	"List":                {table: "list", idPath: "id"},
	"SavedView":           {table: "saved_view", idPath: "id"},
	"Automation":          {table: "automation", idPath: "id"},
	"PromoteLeadResponse": {table: "person", idPath: "person.id"},
	// Not row-scoped in themselves — they carry a reference to a record that
	// is, and inherit its visibility. An offer without its deal's scope would
	// hand back that deal's pricing and buyer snapshot to someone who can no
	// longer open the deal.
	"Offer": {table: "deal", idPath: "deal_id"},
	// No owner column, scoped through its subject entity instead
	// (auth.EnsureSignalVisible) — "no owner_id" is not a reason to skip.
	"Signal": {table: "signal", idPath: "id"},
	// Row-scoped through its TARGET, by a rule that lives inside the approvals
	// module; compose borrows that rule rather than keeping a second copy.
	"Approval":    {moduleProbe: "approval", pathParam: "id"},
	"RecordGrant": {tableField: "record_type", idPath: "record_id"},
	// Projections that name their parent nowhere in the body — the route
	// parameter is the only handle on the record whose scope governs them.
	// A body with no reference of its own is the easiest kind to wave through
	// and still hands back whatever its parent contains.
	"PersonConsentState":   {table: "person", pathParam: "id"},
	"VoiceBuild":           {table: "voice_profile", pathParam: "id"},
	"VoiceLearningSummary": {table: "voice_profile", pathParam: "id"},
	"VoiceProfileVersion":  {table: "voice_profile", pathParam: "id"},
	// An inline response schema has no name to key on, so the route is.
	"inline:POST /v1/voice-profiles/{id}/sources": {table: "voice_profile", pathParam: "id"},
}

// expectedTarget mirrors compose's replayTarget for comparison.
type expectedTarget struct {
	object, objectNote, table, tableField, moduleProbe, idPath, pathParam, rowNote string
}

const replayScopeSource = "internal/compose/replayscope.go"

func TestReplayScopeCoversEveryIdempotentOperation(t *testing.T) {
	governance := mappedReplayScope(t)
	responses := successResponseSchemas(t)

	// Vacuous-pass guard: all three sides carry dozens of entries, so
	// finding almost none means an extractor stopped seeing its source.
	if len(governance) < 20 {
		t.Fatalf("only %d replayable operations found — the extractor lost its source", len(governance))
	}

	for route, got := range governance {
		// Every gate is either run or accounted for. A blank entry is the
		// shape this whole table exists to prevent: a body replayed with
		// nobody having said what governs it.
		if got.object == "" && got.objectNote == "" {
			t.Errorf("%s re-checks no object grant and gives no reason — say which object governs the body, or why none does", route)
		}
		if got.object != "" && got.objectNote != "" {
			t.Errorf("%s both names an object and explains why it has none", route)
		}

		schema := responses[route]
		if schema == "" {
			// Unclassifiable reads as "carries no record", which would wave
			// through every exemption unexamined — the way this gate would
			// pass for the wrong reason.
			t.Errorf("%s: no 2xx response schema resolved from the contract, so its governance cannot be checked", route)
			continue
		}
		want, carriesRecord := rowScopedResponses[schema]
		probesRow := got.table != "" || got.tableField != "" || got.moduleProbe != ""

		switch {
		case carriesRecord && !probesRow:
			t.Errorf("%s answers %s, a row-scoped record, but re-probes no row scope (reason given: %q) — the reason is wrong, not the contract", route, schema, got.rowNote)
		case !carriesRecord && probesRow:
			t.Errorf("%s answers %s, which is not a row-scoped record shape, yet a row scope is probed — either the schema belongs in rowScopedResponses or the entry is wrong", route, schema)
		case carriesRecord && probesRow:
			if got.table != want.table || got.tableField != want.tableField ||
				got.moduleProbe != want.moduleProbe ||
				got.idPath != want.idPath || got.pathParam != want.pathParam {
				t.Errorf("%s answers %s: probes %+v, contract says %+v", route, schema, got, want)
			}
			// Exactly one handle on the record: none and the probe has
			// nothing to look up, both and it is ambiguous which one runs.
			sources := 0
			if got.idPath != "" {
				sources++
			}
			if got.pathParam != "" {
				sources++
			}
			if sources != 1 {
				t.Errorf("%s names %d id sources (idPath=%q pathParam=%q) — a probe needs exactly one", route, sources, got.idPath, got.pathParam)
			}
			// A route parameter that is not IN the route resolves to "" at
			// runtime, so the probe would refuse every replay — retry safety
			// silently inverted into a permanent 404.
			if got.pathParam != "" && !strings.Contains(route, "{"+got.pathParam+"}") {
				t.Errorf("%s probes route parameter {%s}, which the route does not declare — at runtime it resolves to empty and refuses every replay", route, got.pathParam)
			}
		case !probesRow && strings.TrimSpace(got.rowNote) == "":
			t.Errorf("%s re-probes no row scope and gives no reason — a reasonless exemption is itself the finding", route)
		}
	}
}

// successResponseSchemas reads each operation's 2xx response schema name
// out of the contract, keyed like the runtime maps.
func successResponseSchemas(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading api/crm.yaml: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parsing api/crm.yaml: %v", err)
	}
	type responseBody struct {
		Content map[string]struct {
			Schema struct {
				Ref string `yaml:"$ref"`
			} `yaml:"schema"`
		} `yaml:"content"`
	}
	schemas := map[string]string{}
	for path, item := range doc.Paths {
		for key, node := range item {
			if !httpMethods[key] {
				continue
			}
			var op struct {
				Responses map[string]responseBody `yaml:"responses"`
			}
			if err := node.Decode(&op); err != nil {
				continue // not an operation shape this gate reads
			}
			for code, resp := range op.Responses {
				if !strings.HasPrefix(code, "2") {
					continue
				}
				route := strings.ToUpper(key) + " /v1" + path
				if ref := resp.Content["application/json"].Schema.Ref; ref != "" {
					schemas[route] = ref[strings.LastIndex(ref, "/")+1:]
					continue
				}
				if _, hasBody := resp.Content["application/json"]; hasBody {
					// An inline schema carries no name to classify by, so the
					// route stands in for one rather than silently reading as
					// "no response record at all".
					schemas[route] = "inline:" + route
				}
			}
		}
	}
	return schemas
}

// mappedReplayScope reads the replayScope map literal out of the compose
// source, the same AST technique the idempotency mirror uses — the root
// component walks the contract, it never imports compose.
func mappedReplayScope(t *testing.T) map[string]expectedTarget {
	t.Helper()
	out := map[string]expectedTarget{}
	forEachMapEntry(t, "replayableOperations", func(route string, value ast.Expr) {
		lit, ok := value.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("%s: replayableOperations[%q] is not a replayTarget literal — teach this extractor the new shape", replayScopeSource, route)
		}
		entry := expectedTarget{}
		for _, field := range lit.Elts {
			kv, ok := field.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("%s: replayableOperations[%q] has a positional field — teach this extractor the new shape", replayScopeSource, route)
			}
			name, _ := kv.Key.(*ast.Ident)
			text := stringLiteral(t, kv.Value)
			switch {
			case name == nil:
				t.Fatalf("%s: replayableOperations[%q] has an unnamed field", replayScopeSource, route)
			case name.Name == "table":
				entry.table = text
			case name.Name == "tableField":
				entry.tableField = text
			case name.Name == "idPath":
				entry.idPath = text
			case name.Name == "pathParam":
				entry.pathParam = text
			case name.Name == "moduleProbe":
				entry.moduleProbe = text
			case name.Name == "objectNote":
				entry.objectNote = text
			case name.Name == "rowNote":
				entry.rowNote = text
			case name.Name == "object":
				entry.object = text
			}
		}
		out[route] = entry
	})
	return out
}

// replayScopeConsts holds the file's string consts, so a reason written once
// and referenced by name reads the same to this gate as an inline literal.
var replayScopeConsts = map[string]string{}

// stringLiteral flattens a string literal, a concatenation of them (the
// reasons wrap across lines), or a reference to a string const in the file.
func stringLiteral(t *testing.T, expr ast.Expr) string {
	t.Helper()
	switch node := expr.(type) {
	case *ast.Ident:
		text, ok := replayScopeConsts[node.Name]
		if !ok {
			t.Fatalf("%s: %s is not a string const in this file — teach this extractor where it comes from", replayScopeSource, node.Name)
		}
		return text
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			t.Fatalf("%s: expected a string literal, found %s", replayScopeSource, node.Kind)
		}
		text, err := strconv.Unquote(node.Value)
		if err != nil {
			t.Fatalf("%s: unquoting %s: %v", replayScopeSource, node.Value, err)
		}
		return text
	case *ast.BinaryExpr:
		return stringLiteral(t, node.X) + stringLiteral(t, node.Y)
	default:
		t.Fatalf("%s: expected a string literal, found %T", replayScopeSource, expr)
		return ""
	}
}

// forEachMapEntry walks one named package-level map literal, failing loudly
// on any element that is not a plain "key": value pair rather than dropping
// it silently.
func forEachMapEntry(t *testing.T, name string, visit func(key string, value ast.Expr)) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), replayScopeSource, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", replayScopeSource, err)
	}
	// Collect the file's string consts first: an entry may reference one by
	// name, and a name resolves to the same claim as the literal would.
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			if lit, ok := value.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unquoting const %s: %v", replayScopeSource, value.Names[0].Name, err)
				}
				replayScopeConsts[value.Names[0].Name] = text
			}
		}
	}

	found := false
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != name || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s: %s is no longer a composite map literal — teach this extractor the new shape", replayScopeSource, name)
			}
			found = true
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					t.Fatalf("%s: %s holds a non key/value element — teach this extractor the new shape", replayScopeSource, name)
				}
				visit(stringLiteral(t, kv.Key), kv.Value)
			}
		}
	}
	if !found {
		t.Fatalf("%s: no package-level map named %s — teach this extractor where it moved", replayScopeSource, name)
	}
}
