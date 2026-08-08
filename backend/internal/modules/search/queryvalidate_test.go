// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// validateJSON runs a plan document end to end — decode then validate — which
// is the path a caller actually takes, so a refusal that only the decoder or
// only the validator can produce is still exercised here.
func validateJSON(ctx context.Context, t *testing.T, doc string) (ValidatedPlan, error) {
	t.Helper()
	plan, err := DecodePlan([]byte(doc))
	if err != nil {
		return ValidatedPlan{}, err
	}
	return NewPlanValidator(NewVocabularyResolver()).Validate(ctx, plan)
}

// refusalCodes reads the codes off a refusal, failing the test when the error
// is not the typed clarification every refusal owes the caller.
func refusalCodes(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		t.Fatal("plan accepted; want a refusal")
	}
	var faults apperrors.FieldFaults
	if !errorAs(err, &faults) {
		t.Fatalf("refusal is %T, which no transport can render as a clarification", err)
	}
	codes := make([]string, 0, len(faults.FieldFaults()))
	for _, f := range faults.FieldFaults() {
		if f.Message == "" {
			t.Errorf("refusal at %q carries code %q and no message; a code alone does not say what to fix", f.Field, f.Code)
		}
		codes = append(codes, f.Code)
	}
	return codes
}

func errorAs(err error, target *apperrors.FieldFaults) bool {
	faults, ok := err.(apperrors.FieldFaults)
	if ok {
		*target = faults
	}
	return ok
}

// SEARCH-AC-14, one case per refusal class. Every one of these is refused
// with a code that names what was not understood — none is coerced, and none
// is narrowed to the part that parsed.
func TestAPlanOutsideTheVocabularyIsRefusedByClass(t *testing.T) {
	ctx := readerFor("deal", "organization", "person")
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"a table name as the target": {
			doc:  `{"version":"v1","target":"public.deal"}`,
			want: CodeUnknownTarget,
		},
		"a record type the caller cannot read": {
			doc:  `{"version":"v1","target":"lead"}`,
			want: CodeUnknownTarget,
		},
		"a SQL fragment as a field": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"name FROM deal WHERE 1=1 --","op":"eq","value":"x"}]}`,
			want: CodeSQLFragment,
		},
		"a SQL fragment as the target": {
			doc:  `{"version":"v1","target":"deal; DROP TABLE deal"}`,
			want: CodeSQLFragment,
		},
		"a free expression as a field": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"amount_minor * 2","op":"gt","value":1}]}`,
			want: CodeFreeExpression,
		},
		"an unknown field": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"probability","op":"eq","value":"x"}]}`,
			want: CodeUnknownField,
		},
		"an unknown operator": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"name","op":"matches","value":"x"}]}`,
			want: CodeUnknownOperator,
		},
		"an operator the field's kind does not admit": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"name","op":"gt","value":"x"}]}`,
			want: CodeUnknownOperator,
		},
		"a join the vocabulary lacks": {
			doc:  `{"version":"v1","target":"deal","traverse":{"relation":"invoices"}}`,
			want: CodeUnknownRelation,
		},
		"a second hop": {
			doc:  `{"version":"v1","target":"deal","traverse":{"relation":"organization","traverse":{"relation":"deals"}}}`,
			want: CodeTraversalDepthExceeded,
		},
		"a member the grammar has no place for": {
			doc:  `{"version":"v1","target":"deal","sql":"select 1"}`,
			want: CodeUnknownPlanMember,
		},
		"a plan of another version": {
			doc:  `{"version":"v2","target":"deal"}`,
			want: CodeUnknownPlanVersion,
		},
		"a document that is not a plan": {
			doc:  `not json at all`,
			want: CodeMalformedPlan,
		},
		"a second document after the plan": {
			doc:  `{"version":"v1","target":"deal"} {"version":"v1","target":"deal"}`,
			want: CodeMalformedPlan,
		},
		"a string where a number belongs": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"amount_minor","op":"gt","value":"lots"}]}`,
			want: CodeValueTypeMismatch,
		},
		"an operand the plan forgot": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"name","op":"eq"}]}`,
			want: CodeValueMissing,
		},
		"an empty in-list": {
			doc:  `{"version":"v1","target":"deal","where":[{"field":"name","op":"in","values":[]}]}`,
			want: CodeValueMissing,
		},
		"a page bigger than the surface serves": {
			doc:  `{"version":"v1","target":"deal","limit":5000}`,
			want: CodeLimitOutOfRange,
		},
		"a page of nothing": {
			doc:  `{"version":"v1","target":"deal","limit":0}`,
			want: CodeLimitOutOfRange,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(ctx, t, tc.doc)
			if codes := refusalCodes(t, err); !slices.Contains(codes, tc.want) {
				t.Errorf("refused with %v; want %q", codes, tc.want)
			}
		})
	}
}

// A plan with several faults names all of them: a caller told about the first
// of three has to make three round trips to learn what one answer could have
// carried.
func TestARefusalNamesEveryFaultInThePlan(t *testing.T) {
	_, err := validateJSON(readerFor("deal"), t, `{"version":"v1","target":"deal","where":[
		{"field":"probability","op":"eq","value":"x"},
		{"field":"name","op":"gt","value":"x"},
		{"field":"amount_minor","op":"gt","value":"lots"}],"limit":9000}`)
	codes := refusalCodes(t, err)
	for _, want := range []string{CodeUnknownField, CodeUnknownOperator, CodeValueTypeMismatch, CodeLimitOutOfRange} {
		if !slices.Contains(codes, want) {
			t.Errorf("refusal %v omits %q", codes, want)
		}
	}
}

// SEARCH-AC-16: a real field the caller may not read and an invented one are
// refused IDENTICALLY. Anything that varied between the two would turn
// vocabulary probing into field discovery.
func TestADeniedNameAndAnInventedOneRefuseIdentically(t *testing.T) {
	// `lead` and `organization` really exist — the contract declares both,
	// and another principal can read them. `galaxies` and `nebulae` never
	// existed. This caller must not be able to tell the two apart.
	ctx := readerFor("deal")
	for _, tc := range []struct{ name, real, invented, doc string }{
		{
			name: "a record type", real: "lead", invented: "galaxies",
			doc: `{"version":"v1","target":%q}`,
		},
		{
			name: "a relationship hop", real: "organization", invented: "nebulae",
			doc: `{"version":"v1","target":"deal","traverse":{"relation":%q}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, realErr := validateJSON(ctx, t, fmt.Sprintf(tc.doc, tc.real))
			_, inventedErr := validateJSON(ctx, t, fmt.Sprintf(tc.doc, tc.invented))
			realFault, inventedFault := singleFault(t, realErr), singleFault(t, inventedErr)

			if realFault.Code != inventedFault.Code {
				t.Errorf("the real name refused as %q, the invented one as %q; the difference is a discovery channel",
					realFault.Code, inventedFault.Code)
			}
			if realFault.Field != inventedFault.Field {
				t.Errorf("the refusals point at different paths (%q vs %q)", realFault.Field, inventedFault.Field)
			}
			// Both messages quote the caller's OWN token, so they may differ
			// by exactly that substitution and by nothing else.
			realShape := strings.ReplaceAll(realFault.Message, quote(tc.real), "<token>")
			inventedShape := strings.ReplaceAll(inventedFault.Message, quote(tc.invented), "<token>")
			if realShape != inventedShape {
				t.Errorf("refusal wording differs beyond the quoted token:\n real:     %s\n invented: %s",
					realFault.Message, inventedFault.Message)
			}
		})
	}
}

// The same equivalence on a FIELD the caller cannot read: the custom field
// exists in the workspace, but not for a caller who cannot read its record
// type — and the refusal must not say otherwise.
func TestADeniedRecordTypesFieldIsRefusedAsAnUnknownTarget(t *testing.T) {
	catalog := stubCatalog{columns: map[string][]fieldcatalog.Column{
		"deal": {{Name: "cf_margin", Type: fieldcatalog.TypeNumber}},
	}}
	validator := NewPlanValidator(NewVocabularyResolver().WithFieldCatalog(catalog))

	plan, err := DecodePlan([]byte(`{"version":"v1","target":"deal","where":[{"field":"cf_margin","op":"gt","value":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate(readerFor("deal"), plan); err != nil {
		t.Fatalf("a caller who reads deals cannot ask about the workspace's own deal column: %v", err)
	}
	_, err = validator.Validate(readerFor("person"), plan)
	fault := singleFault(t, err)
	if fault.Code != CodeUnknownTarget {
		t.Errorf("a caller who cannot read deals was refused with %q; want %q, which says nothing about what exists",
			fault.Code, CodeUnknownTarget)
	}
}

func singleFault(t *testing.T, err error) apperrors.FieldRefusal {
	t.Helper()
	if err == nil {
		t.Fatal("plan accepted; want a refusal")
	}
	faults, ok := err.(apperrors.FieldFaults)
	if !ok {
		t.Fatalf("refusal is %T, not the typed clarification", err)
	}
	if len(faults.FieldFaults()) != 1 {
		t.Fatalf("refusal carries %d clarifications; want exactly one", len(faults.FieldFaults()))
	}
	return faults.FieldFaults()[0]
}

// The whole of what v1 admits, in one plan.
func TestTheThreeThingsV1AdmitsValidate(t *testing.T) {
	plan, err := validateJSON(readerFor("deal", "organization"), t, `{
		"version":"v1",
		"target":"deal",
		"where":[{"field":"status","op":"eq","value":"open"},
		         {"field":"amount_minor","op":"gte","value":100000},
		         {"field":"forecast_category","op":"in","values":["commit","best_case"]}],
		"similar_to":"manufacturers who churned after a pilot",
		"traverse":{"relation":"organization","where":[{"field":"address.city","op":"eq","value":"Stuttgart"}]},
		"limit":25}`)
	if err != nil {
		t.Fatalf("the v1 grammar refused a v1 plan: %v", err)
	}
	if plan.Limit != 25 {
		t.Errorf("limit resolved to %d, want 25", plan.Limit)
	}
	if plan.Hop == nil || plan.Hop.Target != "organization" || plan.Hop.Via != "organization_id" {
		t.Errorf("hop resolved to %+v; want the derived organization edge", plan.Hop)
	}
	if len(plan.Unavailable) != 0 {
		t.Errorf("a plan of answerable predicates reports %v unavailable", plan.Unavailable)
	}
}

// An absent limit takes the contract default rather than an unbounded scan.
func TestAPlanWithNoLimitTakesTheContractDefault(t *testing.T) {
	plan, err := validateJSON(readerFor("deal"), t, `{"version":"v1","target":"deal"}`)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Limit != 50 {
		t.Errorf("default page is %d; want the contract's 50", plan.Limit)
	}
}

// SEARCH-AC-17: the radius operator is declared, so it is not an unknown
// operator — and it answers with its unavailability rather than with a
// ranking that has nothing behind it. City stays an ordinary exact match.
func TestWithinRadiusValidatesAndAnswersItsUnavailability(t *testing.T) {
	ctx := readerFor("organization")
	plan, err := validateJSON(ctx, t, `{"version":"v1","target":"organization",
		"where":[{"field":"address","op":"within_radius","value":{"center":"Stuttgart","radius_km":50}}]}`)
	if err != nil {
		t.Fatalf("a declared operator was refused: %v", err)
	}
	if len(plan.Unavailable) != 1 || plan.Unavailable[0].Code != CodeDistanceRankingUnavailable {
		t.Fatalf("plan reports %v; want one %s note", plan.Unavailable, CodeDistanceRankingUnavailable)
	}
	if plan.Unavailable[0].Path != "where[0]" {
		t.Errorf("the note points at %q rather than the predicate it belongs to", plan.Unavailable[0].Path)
	}

	exact, err := validateJSON(ctx, t, `{"version":"v1","target":"organization",
		"where":[{"field":"address.city","op":"eq","value":"Stuttgart"}]}`)
	if err != nil {
		t.Fatalf("a city predicate was refused: %v", err)
	}
	if len(exact.Unavailable) != 0 {
		t.Errorf("a city predicate reports %v unavailable; city and region work today", exact.Unavailable)
	}
}

// A malformed radius operand is the CALLER's fault and says so, rather than
// being reported as this deployment's missing capability.
func TestAMalformedRadiusOperandIsRefusedRatherThanDeclaredUnavailable(t *testing.T) {
	for name, doc := range map[string]string{
		"no radius":     `{"center":"Stuttgart"}`,
		"no center":     `{"radius_km":50}`,
		"empty center":  `{"center":"","radius_km":50}`,
		"zero radius":   `{"center":"Stuttgart","radius_km":0}`,
		"a bare string": `"Stuttgart"`,
		"an extra knob": `{"center":"Stuttgart","radius_km":50,"unit":"miles"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(readerFor("organization"), t,
				`{"version":"v1","target":"organization","where":[{"field":"address","op":"within_radius","value":`+doc+`}]}`)
			if codes := refusalCodes(t, err); !slices.Contains(codes, CodeValueTypeMismatch) {
				t.Errorf("refused with %v; want %q", codes, CodeValueTypeMismatch)
			}
		})
	}
}

// The classifier explains; it never admits. Whatever it thinks of a token,
// the token is already out of the vocabulary by the time it is consulted.
func TestTheClassifierOnlyExplainsARefusalItDidNotCause(t *testing.T) {
	// `converted_from_lead_id` is a real contract field AND one the classifier
	// would call SQL if it were ever asked — it carries `from` as a whole
	// word. It validates anyway, because membership is settled first and the
	// classifier is only consulted about tokens already refused.
	if !looksLikeSQL("converted_from_lead_id") {
		t.Fatal("the fixture no longer exercises the classifier; pick a field it would call SQL")
	}
	plan, err := validateJSON(readerFor("person"), t,
		`{"version":"v1","target":"person","where":[{"field":"converted_from_lead_id","op":"eq",`+
			`"value":"00000000-0000-7000-8000-000000000001"}]}`)
	if err != nil {
		t.Fatalf("a legitimate contract field the classifier would call SQL was refused: %v", err)
	}
	if len(plan.Plan.Where) != 1 {
		t.Fatalf("validated plan carries %d predicates", len(plan.Plan.Where))
	}
	// And a token the classifier has no opinion about is still refused,
	// because membership — not shape — is what decides.
	_, err = validateJSON(readerFor("person"), t,
		`{"version":"v1","target":"person","where":[{"field":"innocent_looking_name","op":"eq","value":"x"}]}`)
	if codes := refusalCodes(t, err); !slices.Contains(codes, CodeUnknownField) {
		t.Errorf("an unrecognised but harmless-looking token was refused with %v; want %q", codes, CodeUnknownField)
	}
}

func TestClassifyNamesTheShapeOfARefusedToken(t *testing.T) {
	for token, want := range map[string]string{
		"amount_minor":          CodeUnknownField,
		"address.city":          CodeUnknownField,
		"name; DROP TABLE deal": CodeSQLFragment,
		"1 UNION SELECT 1":      CodeSQLFragment,
		"name -- comment":       CodeSQLFragment,
		"/* hi */ name":         CodeSQLFragment,
		"amount * 2":            CodeFreeExpression,
		"UPPER(name)":           CodeFreeExpression,
		"Name":                  CodeFreeExpression,
		"a.b.c":                 CodeFreeExpression,
	} {
		if got := classify(token, CodeUnknownField); got != want {
			t.Errorf("classify(%q) = %q, want %q", token, got, want)
		}
	}
}

// A traversal's predicates are checked against the vocabulary of the record
// the hop LANDS on, not the one it started from.
func TestAHopsPredicatesAreCheckedAgainstTheHopsTarget(t *testing.T) {
	ctx := readerFor("deal", "organization")
	if _, err := validateJSON(ctx, t, `{"version":"v1","target":"deal",
		"traverse":{"relation":"organization","where":[{"field":"industry","op":"eq","value":"manufacturing"}]}}`); err != nil {
		t.Fatalf("an organization field was refused inside an organization hop: %v", err)
	}
	_, err := validateJSON(ctx, t, `{"version":"v1","target":"deal",
		"traverse":{"relation":"organization","where":[{"field":"amount_minor","op":"gt","value":1}]}}`)
	if codes := refusalCodes(t, err); !slices.Contains(codes, CodeUnknownField) {
		t.Errorf("a deal field inside an organization hop was refused with %v; want %q", codes, CodeUnknownField)
	}
}

// The decoder never drops what it does not recognise: a plan carrying an
// unknown member comes back refused, not silently reduced to the members that
// happened to fit.
func TestTheDecoderRefusesRatherThanDropsAnUnknownMember(t *testing.T) {
	_, err := DecodePlan([]byte(`{"version":"v1","target":"deal","where":[{"field":"name","op":"eq","value":"x","collate":"C"}]}`))
	fault := singleFault(t, err)
	if fault.Code != CodeUnknownPlanMember {
		t.Errorf("an unknown predicate member was refused as %q; want %q", fault.Code, CodeUnknownPlanMember)
	}
	if !strings.Contains(fault.Message, "collate") {
		t.Errorf("the refusal does not name the member the caller has to remove: %q", fault.Message)
	}
}

// A refusal reaching an untrusted agent must not carry internals. The
// messages quote the caller's own tokens and name the published vocabulary;
// nothing else.
func TestARefusalCarriesNoServerInternals(t *testing.T) {
	_, err := validateJSON(readerFor("deal"), t,
		`{"version":"v1","target":"deal","where":[{"field":"probability","op":"eq","value":"x"}]}`)
	fault := singleFault(t, err)
	for _, leak := range []string{"pgx", "SELECT", "workspace_id", "internal/", "sql:"} {
		if strings.Contains(fault.Message, leak) {
			t.Errorf("refusal message leaks %q: %s", leak, fault.Message)
		}
	}
	if !strings.Contains(fault.Message, QuerySchemaURI) {
		t.Errorf("refusal does not point at the published vocabulary: %s", fault.Message)
	}
}

// A validated plan is the only thing an executor may run, so it carries the
// resolved vocabulary rather than leaving the executor to re-derive it from
// the plan's text.
func TestAValidatedPlanCarriesWhatTheExecutorNeeds(t *testing.T) {
	plan, err := validateJSON(readerFor("deal", "organization"), t,
		`{"version":"v1","target":"deal","traverse":{"relation":"organization"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target.Target != "deal" {
		t.Errorf("validated plan carries target %q", plan.Target.Target)
	}
	if len(plan.Target.Fields) == 0 {
		t.Error("validated plan carries no resolved field set")
	}
	if plan.Hop == nil || plan.Hop.Via == "" {
		t.Error("validated plan carries a hop with no derived join")
	}
}

// The refusal renders through the shared fault interface, which is what makes
// it legible on REST and on the tool surface without either hand-writing a
// mapping.
func TestARefusalIsTheSharedPluralFaultForm(t *testing.T) {
	var refusal error = &PlanRefusal{Refusals: []apperrors.FieldRefusal{
		{Field: "where[0].field", Code: CodeUnknownField, Message: "no"},
	}}
	if _, ok := refusal.(apperrors.FieldFaults); !ok {
		t.Fatal("PlanRefusal is not the plural fault form")
	}
	if !strings.Contains(refusal.Error(), CodeUnknownField) {
		t.Errorf("the log summary does not name the code: %s", refusal.Error())
	}
}

// json.RawMessage operands round-trip: an operand this validator accepted is
// the operand the executor will bind, byte for byte.
func TestAnAcceptedOperandSurvivesValidationUnchanged(t *testing.T) {
	plan, err := validateJSON(readerFor("deal"), t,
		`{"version":"v1","target":"deal","where":[{"field":"amount_minor","op":"gte","value":100000}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := json.Unmarshal(plan.Plan.Where[0].Value, &got); err != nil {
		t.Fatal(err)
	}
	if got != 100000 {
		t.Errorf("operand survived validation as %d", got)
	}
}

// An operand member the operator does not read is refused rather than
// ignored: a caller who filled `values` under `eq` meant the list, and
// answering on `value` instead answers a question they did not ask.
func TestTheOperandMemberAnOperatorDoesNotReadIsRefused(t *testing.T) {
	for name, doc := range map[string]string{
		"a list under eq":  `{"field":"name","op":"eq","value":"a","values":["a","b"]}`,
		"a value under in": `{"field":"name","op":"in","value":"a","values":["a"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(readerFor("deal"), t,
				`{"version":"v1","target":"deal","where":[`+doc+`]}`)
			if codes := refusalCodes(t, err); !slices.Contains(codes, CodeValueNotApplicable) {
				t.Errorf("refused with %v; want %q", codes, CodeValueNotApplicable)
			}
		})
	}
}

// A plan that names nothing at all is refused as a missing name, not as an
// expression — calling it one would send the caller looking for an expression
// they never wrote.
func TestAnOmittedNameIsRefusedAsMissingRatherThanAsAnExpression(t *testing.T) {
	fault := singleFault(t, mustRefuse(readerFor("deal"), t, `{"version":"v1"}`))
	if fault.Code != CodeUnknownTarget {
		t.Errorf("a plan with no target was refused as %q; want %q", fault.Code, CodeUnknownTarget)
	}
}

func mustRefuse(ctx context.Context, t *testing.T, doc string) error {
	t.Helper()
	_, err := validateJSON(ctx, t, doc)
	return err
}

// A repeated member is refused rather than resolved. encoding/json takes the
// LAST value in silence, so a plan carrying two `where` lists would validate
// on the second and drop the caller's actual question — an answer that looks
// exactly like every other answer, which is what SEARCH-AC-14 forbids.
func TestARepeatedMemberIsRefusedRatherThanResolvedLastWins(t *testing.T) {
	for name, doc := range map[string]string{
		"two where lists": `{"version":"v1","target":"deal",
			"where":[{"field":"status","op":"eq","value":"open"}],"where":[]}`,
		"two targets": `{"version":"v1","target":"deal","target":"person"}`,
		"a repeat inside a predicate": `{"version":"v1","target":"deal",
			"where":[{"field":"status","field":"name","op":"eq","value":"x"}]}`,
		"a repeat inside a traversal": `{"version":"v1","target":"deal",
			"traverse":{"relation":"organization","relation":"project"}}`,
		"a repeat inside a nested predicate": `{"version":"v1","target":"deal",
			"traverse":{"relation":"organization","where":[{"field":"industry","op":"eq","op":"neq","value":"x"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(readerFor("deal", "organization", "person"), t, doc)
			if codes := refusalCodes(t, err); !slices.Contains(codes, CodeDuplicateMember) {
				t.Errorf("refused with %v; want %q", codes, CodeDuplicateMember)
			}
		})
	}
}

// The same member name in DIFFERENT objects is ordinary, not a duplicate —
// every predicate names a `field`, and a scan that refused that would refuse
// every plan with two clauses.
func TestTheSameMemberInDifferentObjectsIsNotADuplicate(t *testing.T) {
	plan, err := validateJSON(readerFor("deal", "organization"), t, `{"version":"v1","target":"deal",
		"where":[{"field":"status","op":"eq","value":"open"},{"field":"name","op":"eq","value":"x"}],
		"traverse":{"relation":"organization","where":[{"field":"industry","op":"eq","value":"y"}]}}`)
	if err != nil {
		t.Fatalf("a plan with repeated member names across separate objects was refused: %v", err)
	}
	if len(plan.Plan.Where) != 2 {
		t.Errorf("validated plan carries %d predicates, want 2", len(plan.Plan.Where))
	}
}

// A malformed document is reported ONCE, by the decode that follows — the
// duplicate scan must not turn a syntax error into a second spelling of the
// same refusal.
func TestTheDuplicateScanLeavesMalformedDocumentsToTheDecoder(t *testing.T) {
	fault := singleFault(t, mustRefuse(readerFor("deal"), t, `{"version":"v1","target":`))
	if fault.Code != CodeMalformedPlan {
		t.Errorf("a truncated document was refused as %q; want %q", fault.Code, CodeMalformedPlan)
	}
}

// encoding/json matches member names CASE-INSENSITIVELY, and so does
// DisallowUnknownFields — so `TARGET` is neither unknown nor, to a scan
// comparing exact strings, a duplicate, and the decoder resolves it
// last-wins. The caller's target is silently replaced by one they did not
// write. Only the canonical spelling is admitted.
func TestACaseVariantMemberCannotOverwriteTheCanonicalOne(t *testing.T) {
	for name, doc := range map[string]string{
		"an upper-cased target":    `{"version":"v1","target":"deal","TARGET":"person"}`,
		"a title-cased where":      `{"version":"v1","target":"deal","Where":[]}`,
		"a variant inside a hop":   `{"version":"v1","target":"deal","traverse":{"Relation":"organization"}}`,
		"a variant operand member": `{"version":"v1","target":"deal","where":[{"field":"name","op":"eq","VALUE":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(readerFor("deal", "organization", "person"), t, doc)
			if codes := refusalCodes(t, err); !slices.Contains(codes, CodeUnknownPlanMember) {
				t.Errorf("refused with %v; want %q", codes, CodeUnknownPlanMember)
			}
		})
	}
}

// A caller's own operand payload is not grammar: `within_radius` takes an
// object whose members the plan grammar has never heard of, and the
// canonical-spelling rule must not reach into it.
func TestAnOperandPayloadIsNotJudgedAgainstTheGrammarsMemberNames(t *testing.T) {
	plan, err := validateJSON(readerFor("organization"), t, `{"version":"v1","target":"organization",
		"where":[{"field":"address","op":"within_radius","value":{"center":"Stuttgart","radius_km":50}}]}`)
	if err != nil {
		t.Fatalf("a legitimate operand payload was refused as a plan member: %v", err)
	}
	if len(plan.Unavailable) != 1 {
		t.Errorf("the radius predicate reports %v", plan.Unavailable)
	}
}

// A repeated member inside an operand payload is still refused: last-wins is
// exactly as silent there as it is in the grammar.
func TestARepeatedMemberInsideAnOperandPayloadIsStillRefused(t *testing.T) {
	_, err := validateJSON(readerFor("organization"), t, `{"version":"v1","target":"organization",
		"where":[{"field":"address","op":"within_radius","value":{"center":"a","center":"b","radius_km":50}}]}`)
	if codes := refusalCodes(t, err); !slices.Contains(codes, CodeDuplicateMember) {
		t.Errorf("refused with %v; want %q", codes, CodeDuplicateMember)
	}
}

// Bytes after the end of the plan are refused. dec.More() is not the test:
// it reports whether another VALUE follows, so a stray delimiter leaves it
// false and the garbage travels with an otherwise valid plan.
func TestBytesAfterThePlanAreRefused(t *testing.T) {
	for name, doc := range map[string]string{
		"a trailing delimiter": `{"version":"v1","target":"deal"}]`,
		"a second document":    `{"version":"v1","target":"deal"} {"version":"v1","target":"deal"}`,
		"a trailing scalar":    `{"version":"v1","target":"deal"} 7`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(readerFor("deal"), t, doc)
			if codes := refusalCodes(t, err); !slices.Contains(codes, CodeMalformedPlan) {
				t.Errorf("refused with %v; want %q", codes, CodeMalformedPlan)
			}
		})
	}
}

// json.Unmarshal accepts null into EVERY Go type without error and leaves the
// zero value behind, so a null operand would otherwise pass as a valid
// number, string or boolean and reach an executor as a zero nobody wrote.
func TestANullOperandIsRefusedRatherThanReadAsAZero(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"a null value":          {`{"field":"amount_minor","op":"gt","value":null}`, CodeValueTypeMismatch},
		"a null text value":     {`{"field":"name","op":"eq","value":null}`, CodeValueTypeMismatch},
		"a null boolean value":  {`{"field":"stalled","op":"eq","value":null}`, CodeValueTypeMismatch},
		"a null inside a list":  {`{"field":"name","op":"in","values":["a",null]}`, CodeValueTypeMismatch},
		"a null list":           {`{"field":"name","op":"in","values":null}`, CodeValueMissing},
		"a null unused operand": {`{"field":"name","op":"eq","value":"a","values":null}`, CodeValueNotApplicable},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateJSON(readerFor("deal", "organization", "person"), t,
				`{"version":"v1","target":"deal","where":[`+tc.doc+`]}`)
			if codes := refusalCodes(t, err); !slices.Contains(codes, tc.want) {
				t.Errorf("refused with %v; want %q", codes, tc.want)
			}
		})
	}
}

// The geo operand has its own null case, on a target that actually carries a
// place.
func TestANullRadiusOperandIsRefused(t *testing.T) {
	_, err := validateJSON(readerFor("organization"), t,
		`{"version":"v1","target":"organization","where":[{"field":"address","op":"within_radius","value":null}]}`)
	if codes := refusalCodes(t, err); !slices.Contains(codes, CodeValueTypeMismatch) {
		t.Errorf("refused with %v; want %q", codes, CodeValueTypeMismatch)
	}
}

// A null limit is not an omitted limit. Reading it as absent would serve a
// page size the caller never asked for.
func TestANullLimitIsRefusedRatherThanReadAsOmitted(t *testing.T) {
	fault := singleFault(t, mustRefuse(readerFor("deal"), t, `{"version":"v1","target":"deal","limit":null}`))
	if fault.Code != CodeValueTypeMismatch {
		t.Errorf("a null limit was refused as %q; want %q", fault.Code, CodeValueTypeMismatch)
	}
	// And a limit of the wrong SHAPE is the same refusal, not a range error.
	fault = singleFault(t, mustRefuse(readerFor("deal"), t, `{"version":"v1","target":"deal","limit":"25"}`))
	if fault.Code != CodeValueTypeMismatch {
		t.Errorf("a string limit was refused as %q; want %q", fault.Code, CodeValueTypeMismatch)
	}
}

// A refusal message carries the caller's own token, so a token containing a
// quote or a newline must not be able to end the quoted run early or split
// the message across lines.
func TestARefusalMessageSurvivesAHostileToken(t *testing.T) {
	fault := singleFault(t, mustRefuse(readerFor("deal"), t,
		`{"version":"v1","target":"deal","where":[{"field":"a\"b\nc","op":"eq","value":"x"}]}`))
	if strings.Contains(fault.Message, "\n") {
		t.Errorf("the refusal message spans lines: %q", fault.Message)
	}
	if !strings.Contains(fault.Message, `\"`) || !strings.Contains(fault.Message, `\n`) {
		t.Errorf("the token was not escaped into the message: %q", fault.Message)
	}
}
