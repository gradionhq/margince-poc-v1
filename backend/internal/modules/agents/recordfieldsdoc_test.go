// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The published write vocabulary, held to what the two tools used to carry in
// their own descriptions: the same shapes, the same per-write advice, and the
// same refusal to give one tool the other's.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// readRecordFields fetches the document the way a client does — through
// ReadResource and back out of JSON — rather than calling the renderer.
// Everything a caller ever sees passes through that round trip, and a document
// that renders and does not marshal is a resource that fails at read time only.
func readRecordFields(t *testing.T) recordFieldsDoc {
	t.Helper()
	contents, err := RecordFieldsResource{}.ReadResource(context.Background(), RecordFieldsURI)
	if err != nil {
		t.Fatalf("reading %s: %v", RecordFieldsURI, err)
	}
	if contents.URI != RecordFieldsURI || contents.MIMEType != "application/json" {
		t.Fatalf("the document came back as %+v", contents)
	}
	var doc recordFieldsDoc
	if err := json.Unmarshal([]byte(contents.Text), &doc); err != nil {
		t.Fatalf("the published document is not JSON: %v\n%s", err, contents.Text)
	}
	return doc
}

// The document answers with a section per write, and both are populated.
//
// One section is the failure this shape exists to make impossible: the two
// writes disagree about enough that a merged answer would have to hedge, and a
// section that arrived empty would leave a tool pointing at a document that
// says nothing about it.
func TestThePublishedDocumentCarriesASectionPerWrite(t *testing.T) {
	doc := readRecordFields(t)
	if doc.Version != recordFieldsVersion {
		t.Errorf("the document publishes version %q, want %q", doc.Version, recordFieldsVersion)
	}
	// The notation is what makes `?` mean anything; without it a reader infers
	// which keys are required from punctuation, which is the guess the whole
	// document exists to remove.
	if !strings.Contains(doc.Notation, "REQUIRED") {
		t.Errorf("the document does not say what its notation marks as required: %q", doc.Notation)
	}
	for name, section := range map[string]recordFieldsWrite{"create_record": doc.Create, "update_record": doc.Update} {
		if len(section.Fields) == 0 {
			t.Errorf("%s's section describes no record type, so the tool points at a document silent about it", name)
		}
		if len(section.Notes) == 0 {
			t.Errorf("%s's section carries no notes, so everything a field list cannot show is gone", name)
		}
	}
}

// Every other URI is not found — the same answer a URI the caller cannot see
// gets, so this resource hides existence exactly as the rest of the surface
// does.
func TestAnUnknownRecordFieldsURIIsNotFound(t *testing.T) {
	for _, uri := range []string{"margince://schema/query", "margince://schema/record-field", ""} {
		_, err := RecordFieldsResource{}.ReadResource(context.Background(), uri)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Errorf("reading %q answered %v, want the declared not-found", uri, err)
		}
	}
}

// Each section describes exactly what ITS tool decodes.
//
// Derived from the contract structs rather than compared against a list, for
// the reason the shapes themselves are: a hand-kept expectation drifts the
// moment crm.yaml changes. What it catches that the shape tables cannot is a
// section wired to the wrong map — create shapes published under update_record
// would tell a caller to patch fields the decoder refuses, and every assertion
// about the shapes themselves would still pass.
func TestEachSectionPublishesTheFieldsItsOwnWriteDecodes(t *testing.T) {
	doc := readRecordFields(t)
	checked := 0
	for _, table := range []struct {
		tool    string
		section recordFieldsWrite
		decoded map[datasource.EntityType]reflect.Type
	}{
		{"create_record", doc.Create, createShapes},
		{"update_record", doc.Update, updateShapes},
	} {
		if len(table.section.Fields) != len(table.decoded) {
			t.Errorf("%s publishes %d record types and decodes %d",
				table.tool, len(table.section.Fields), len(table.decoded))
		}
		for recordType, shape := range table.decoded {
			published, ok := table.section.Fields[string(recordType)]
			if !ok {
				t.Errorf("%s decodes %s but the document publishes no shape for it", table.tool, recordType)
				continue
			}
			checked++
			if got, want := renderedKeys(published), contractFieldNames(shape); !slices.Equal(got, want) {
				t.Errorf("%s %s publishes keys %v, the decoder accepts %v", table.tool, recordType, got, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no published shape was compared — this walk passed vacuously")
	}
}

// noteText joins one section's notes so an assertion can ask whether the claim
// is stated anywhere in it, rather than in a particular note.
func noteText(section recordFieldsWrite) string {
	return strings.Join(section.Notes, "\n")
}

// The two sections' notes must not converge.
//
// Each note is advice about a field, and the two writes disagree about several
// of them: an edge's endpoints can be NAMED on create and cannot be patched at
// all, an activity's links likewise, `source` is accepted-and-overwritten on
// create and REFUSED on update. Every one of those branches was keyed on the
// record type at first, and both shape maps carry those types — so each tool
// shipped the other's advice, which is worse than none: a caller acts on it.
func TestThePerWriteNotesGiveEachToolOnlyAdviceItCanAct(t *testing.T) {
	doc := readRecordFields(t)
	create, patch := noteText(doc.Create), noteText(doc.Update)

	// Keyed on the stable CLAIM, not on the sentence — a reworded note is not a
	// regression, a missing or misplaced one is.
	for _, tc := range []struct {
		claim      string
		createOnly bool
		why        string
	}{
		{"endpoint pair", true, "naming an endpoint pair is a thing only a create can do"},
		{"stamps its own provenance", true, "no update request type carries `source` at all, so it is refused as " +
			"unknown rather than overwritten — the opposite advice"},
		{"come from list_pipelines", true, "a patch is not required to supply either id, so this is advice about a " +
			"problem update_record does not have"},
		{"links are NOT patchable", false, "create_record and log_activity both ACCEPT links, so on create this is " +
			"advice against a field the tool takes"},
		{"employer is NOT a field here", false, "the pairing rule already says it on create; here it is the whole " +
			"answer, because an endpoint cannot be patched"},
	} {
		t.Run(tc.claim, func(t *testing.T) {
			stated, withheld, tool := patch, create, "update_record"
			if tc.createOnly {
				stated, withheld, tool = create, patch, "create_record"
			}
			if !strings.Contains(stated, tc.claim) {
				t.Errorf("%s's notes never say %q — %s:\n%s", tool, tc.claim, tc.why, stated)
			}
			if strings.Contains(withheld, tc.claim) {
				t.Errorf("the other write's notes say %q, which is advice its caller cannot act on — %s", tc.claim, tc.why)
			}
		})
	}
}

// The custom-field note must not promise carriage the surface withholds.
//
// Both sections tell a caller that an extra cf_<slug> key is read as a
// custom-field value. That is false for activity and relationship: neither
// contract shape carries the additionalProperties bag a cf_ value travels in,
// so an agent following the note writes a key the strict decoder refuses.
func TestTheCustomFieldNoteNamesTheTypesThatTakeNone(t *testing.T) {
	doc := readRecordFields(t)
	for name, section := range map[string]recordFieldsWrite{"create_record": doc.Create, "update_record": doc.Update} {
		notes := noteText(section)
		if !strings.Contains(notes, "cf_<slug>") {
			t.Fatalf("%s's notes no longer mention the custom-field key shape at all:\n%s", name, notes)
		}
		// The exclusion clause, isolated with Cut so the assertion reads the
		// SENTENCE rather than the whole section — "activity" appears in the
		// notes about reach too, so a substring test over the section would pass
		// without any exclusion being stated.
		_, exclusion, found := strings.Cut(notes, "No custom fields on")
		if !found {
			t.Errorf("%s promises cf_ carriage and excludes nothing, though activity and relationship carry no "+
				"additionalProperties bag — a cf_ key on either is refused, not stored:\n%s", name, notes)
			continue
		}
		// Both, and by name: an agent reads prose, so "some types are excluded"
		// would leave it guessing which.
		for _, excluded := range []string{"activity", "relationship"} {
			if !strings.Contains(exclusion, excluded) {
				t.Errorf("%s's cf_ exclusion clause %q does not name %q", name, exclusion, excluded)
			}
		}
	}
}

// The document is advertised with everything a client needs to decide whether
// to fetch it, and behind the WRITE scope.
//
// The scope is the assertion that matters. This document is the write
// vocabulary — every field a create or a patch may name, per record type — and
// a passport granted only `read` learning it would make the resource surface
// the discovery channel the scope-filtered tool list is careful not to be. The
// caller admitted to create_record is admitted to this by the same scope, and
// nobody else is.
func TestTheWriteVocabularyIsAdvertisedBehindTheWriteScope(t *testing.T) {
	published := RecordFieldsResource{}.Resources(context.Background())
	if len(published) != 1 {
		t.Fatalf("the resource publishes %d documents, want 1", len(published))
	}
	r := published[0]
	if r.RequiredScope != principal.ScopeWrite {
		t.Errorf("the write vocabulary requires %q, want %q — a read-only passport must not learn what a write "+
			"may name", r.RequiredScope, principal.ScopeWrite)
	}
	if r.URI != RecordFieldsURI || r.Name == "" || r.Title == "" || r.Description == "" || r.MIMEType == "" {
		t.Errorf("the document is advertised incompletely: %+v", r)
	}
	// The two tools that point here, named in the advertisement: a client
	// choosing what to fetch reads this line and nothing else.
	for _, tool := range []string{"create_record", "update_record"} {
		if !strings.Contains(r.Description, tool) {
			t.Errorf("the advertisement does not say it is the vocabulary for %s: %q", tool, r.Description)
		}
	}
}
