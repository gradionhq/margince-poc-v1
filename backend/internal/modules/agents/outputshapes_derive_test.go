// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The deriver's refusals. Each is a type that would otherwise be advertised as
// something looser than it is, and the walk has to carry the refusal OUT of
// whatever it was nested in — a bad element type inside a slice or a map is
// still a schema this surface cannot honestly publish.
func TestTheDeriverRefusesATypeItCannotDescribe(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"a bare channel":      reflect.TypeOf(make(chan int)),
		"a slice of channels": reflect.TypeOf([]chan int(nil)),
		"a map of channels":   reflect.TypeOf(map[string]chan int(nil)),
		"a struct holding one": reflect.TypeOf(struct {
			Ch chan int `json:"ch"`
		}{}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := describeType(typ); err == nil {
				t.Error("described a type with no JSON rendering rather than refusing it")
			}
		})
	}
}

// A field with no json tag would go on the wire under its GO name, which is a
// name nothing else on this surface uses. That is a defect in the result type,
// and the deriver says so rather than publishing it.
func TestAnUntaggedExportedFieldIsRefused(t *testing.T) {
	_, err := describeType(reflect.TypeOf(struct {
		Untagged string
	}{}))
	if err == nil || !strings.Contains(err.Error(), "no json tag") {
		t.Errorf("err = %v, want a refusal naming the untagged field", err)
	}
}

// A field the wire never carries is not part of the shape: an unexported one,
// and one tagged out. Describing either would advertise a member no result has.
func TestFieldsTheWireNeverCarriesAreNotDescribed(t *testing.T) {
	schema, err := describeType(reflect.TypeOf(struct {
		Kept    string `json:"kept"`
		Skipped string `json:"-"`
		hidden  string
	}{}))
	if err != nil {
		t.Fatalf("describing the struct: %v", err)
	}
	if _, named := schema.Properties["kept"]; !named {
		t.Error("the tagged field was not described")
	}
	for _, absent := range []string{"Skipped", "-", "hidden"} {
		if _, named := schema.Properties[absent]; named {
			t.Errorf("%q was described, but it never reaches the wire", absent)
		}
	}
}

// json.RawMessage holds a document this surface did not build, so the honest
// schema for it says "an object" and stops — describing it as the []byte it is
// would advertise an array of integers.
func TestARawMessageIsDescribedAsAnObjectAndNotAsItsBytes(t *testing.T) {
	schema, err := describeType(reflect.TypeOf(json.RawMessage(nil)))
	if err != nil {
		t.Fatalf("describing a raw message: %v", err)
	}
	if schema.Type != schemaObject || schema.Items != nil {
		t.Errorf("schema = %+v, want a bare object", schema)
	}
}

// The number kinds, because a result carrying a count and one carrying an
// amount must not be advertised the same way as one carrying a rate.
func TestScalarKindsAreDescribedAsTheirWireTypes(t *testing.T) {
	for want, value := range map[string]any{
		schemaInteger: struct {
			V int64 `json:"v"`
		}{},
		schemaNumber: struct {
			V float64 `json:"v"`
		}{},
		schemaBoolean: struct {
			V bool `json:"v"`
		}{},
		schemaString: struct {
			V string `json:"v"`
		}{},
	} {
		schema, err := describeType(reflect.TypeOf(value))
		if err != nil {
			t.Fatalf("describing %s: %v", want, err)
		}
		if got := schema.Properties["v"].Type; got != want {
			t.Errorf("described as %q, want %q", got, want)
		}
	}
}

// The check the other derive tests cannot make.
//
// Every assertion above reads the deriver's OWN output, and so does the golden
// file. A reflection rule that is simply wrong — a tag misread, an embedded
// struct nested instead of flattened, a uuid described as its bytes — would
// satisfy all of them and be canonized into the golden the first time it ran.
//
// This closes that circle from the other side: it marshals a REPRESENTATIVE
// VALUE of each result type with encoding/json, which is the same encoder every
// handler ends in, and holds the bytes to the schema derived from the same type.
// A derivation that disagrees with the encoder fails here, whatever the golden
// says.
func TestEveryResultTypeSatisfiesTheSchemaDerivedFromIt(t *testing.T) {
	note := "a note"
	amount := int64(250000)
	currency := "EUR"
	id := ids.NewV7()
	record := wireRecord{RecordType: "deal", ID: id, Fields: json.RawMessage(`{"name":"Acme"}`), Version: 3}

	for name, value := range map[string]any{
		"SearchRecordsResult":  SearchRecordsResult{Records: []wireRecord{record}, NextCursor: "cursor"},
		"SearchRecordsEmpty":   SearchRecordsResult{Records: []wireRecord{}},
		"wireRecord":           record,
		"ArchiveResult":        ArchiveResult{Archived: true, RecordType: "person", ID: id},
		"PromoteLeadResult":    PromoteLeadResult{Merged: true, Person: record},
		"MergeRecordsResult":   MergeRecordsResult{Merged: true, RecordType: "person", SurvivorID: id},
		"DraftEmailResult":     DraftEmailResult{Subject: "Re: x", Body: "hi", InReplyToActivityID: id},
		"AssembledContext":     AssembledContextResult{Anchor: ContextAnchor{RecordType: "deal", RecordID: id}, Sections: []ContextSection{{Name: "recent", Items: []ContextItem{{RecordType: "activity", RecordID: id, Summary: "s", Evidence: []ContextEvidence{{Source: "a", Snippet: "b"}}}}}}},
		"PrepForMeetingResult": PrepForMeetingResult{Briefing: AssembledContextResult{Anchor: ContextAnchor{RecordType: "deal", RecordID: id}, Sections: []ContextSection{}}, MeetingFocus: []MeetingFocusItem{{RecordID: id, Summary: "s"}}},
		"QualifyLeadResult":    QualifyLeadResult{RecordID: id, Filled: map[string]QualifiedField{"company_name": {Value: "Acme", Evidence: []ContextEvidence{{Source: "lead.email", Snippet: "a@acme"}}}}, Gaps: []string{"title"}},
		"ProgressDealWithNote": ProgressDealResult{Deal: record, NoteActivityID: &id},
		"ProgressDealNoNote":   ProgressDealResult{Deal: record},
		"WhatsSlippingResult":  WhatsSlippingResult{Deals: []SlippingDealItem{{Rank: 1, DealID: id, Name: "Acme", AmountMinor: &amount, Currency: &currency, Evidence: []SlippingEvidence{{Source: "s", Snippet: "x"}}}}},
		"SlippingUnpriced":     WhatsSlippingResult{Deals: []SlippingDealItem{{Rank: 1, DealID: id, Name: "Acme", Evidence: []SlippingEvidence{{Source: "s", Snippet: "x"}}}}},
		"DraftFollowUpsResult": DraftFollowUpsResult{Segment: "slipping", Drafts: []FollowUpDraft{{DealID: id, DraftActivityID: id, Summary: note, Evidence: []SlippingEvidence{{Source: "s", Snippet: "x"}}}}},
		"UpdateSplitResult":    UpdateWithStagedApprovalResult{wireRecord: record, StagedApproval: &stagedApprovalNote{ApprovalID: ids.New[ids.ApprovalKind](), Fields: []string{"title"}, Replay: json.RawMessage(`{}`), Message: "m"}},
		"UpdatePlainResult":    UpdateWithStagedApprovalResult{wireRecord: record},
		"RunReportResult":      RunReportResult{Report: "pipeline", Columns: []string{"stage"}, Plan: json.RawMessage(`{}`), Rows: []json.RawMessage{json.RawMessage(`{"stage":"open"}`)}},
		"SendEmailResult":      SendEmailResult{ActivityID: id, Status: "accepted"},
		"SendMessageResult":    SendMessageResult{ActivityID: id, Status: "accepted"},
		"AvailabilityResult":   AvailabilityResult{Slots: []FreeSlot{{Start: time.Now().UTC(), End: time.Now().UTC().Add(time.Hour)}}, Truncated: false},
		"PassthroughEntity":    PassthroughEntityResult{ID: id},
		"listPipelinesAnswer":  listPipelinesAnswer{Pipelines: []Pipeline{{ID: id, Name: "Sales", IsDefault: true, Stages: []Stage{{ID: id, Name: "Open", Semantic: "open"}}}}},
		"WhoKnowsAnswer":       WhoKnowsAnswer{PersonID: id, Colleagues: []KnownColleague{{UserID: id, DisplayName: "Ada", StrengthBucket: "warm"}}},
		"IntroPathAnswer":      IntroPathAnswer{OrganizationID: id, Routes: []IntroRoute{}},
		"AtRiskReport":         AtRiskReport{Deals: []AtRiskDeal{}, DealsScanned: 4},
		"DealCoverageAnswer":   DealCoverageAnswer{DealID: id, Stakeholders: []CoverageSeat{}, OurSide: []KnownColleague{}, Risks: []CoverageRisk{}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshalling the value: %v", err)
			}
			schema := schemaOfValue(t, value)
			if defect := ResultDefect(schema, encoded); defect != "" {
				t.Errorf("encoding/json produced %s, which the schema derived from the same type rejects: %s",
					encoded, defect)
			}
		})
	}
}

// schemaOfValue derives the schema for a value's own type. schemaFor is generic
// over a compile-time type, and the table above is heterogeneous on purpose —
// one entry per result shape — so the derivation is reached through reflect
// here rather than by writing out twenty-odd separate calls.
//
//craft:ignore naked-any the parameter IS a heterogeneous set of result values — one per shape this surface answers with — and naming a narrower type would mean one call per shape, which is the enumeration this test exists to avoid
func schemaOfValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	schema, err := describeType(reflect.TypeOf(value))
	if err != nil {
		t.Fatalf("describing %T: %v", value, err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("encoding the schema for %T: %v", value, err)
	}
	return raw
}

// The rule the strict null check makes mechanical: this surface already held
// "an empty LIST, not a null" by hand at each boundary, because a model handed
// null reads it as unknown and hedges about something the server was certain
// of. A declared schema that REQUIRES the member turns that convention into a
// check — so the zero value of a result type, which is what a nil list produces,
// must be reported rather than served.
func TestANilListIsReportedRatherThanServedAsNull(t *testing.T) {
	for name, value := range map[string]any{
		"a report with no deals list":   AtRiskReport{},
		"a deal with no findings list":  AtRiskReport{Deals: []AtRiskDeal{{DealID: ids.NewV7(), Name: "Acme"}}},
		"a search with no records list": SearchRecordsResult{},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if defect := ResultDefect(schemaOfValue(t, value), encoded); defect == "" {
				t.Errorf("%s was served as %s — a null where a list belongs", name, encoded)
			}
		})
	}
}
