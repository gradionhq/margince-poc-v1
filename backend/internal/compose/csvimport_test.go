// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// Every advertised target must survive BOTH paths — the create and the patch.
// A field the import offers, accepts, and then drops on one of the two is worse
// than one the screen never showed, and the only way that stays true as the
// stores change is to check the round trip rather than the list.
func TestEveryImportTargetRoundTripsThroughCreateAndUpdate(t *testing.T) {
	for object, build := range map[string]func(map[string]string) (created, patched map[string]bool){
		migration.ObjectLead: func(fields map[string]string) (map[string]bool, map[string]bool) {
			in := leadCreateFrom(fields, "import:csv", "ext-1", "src")
			up := leadUpdateFrom(fields)
			return map[string]bool{
					"full_name":    in.FullName != nil,
					"email":        in.Email != nil,
					"title":        in.Title != nil,
					"company_name": in.CompanyName != nil,
				}, map[string]bool{
					"full_name":    up.FullName != nil,
					"email":        up.Email != nil,
					"title":        up.Title != nil,
					"company_name": up.CompanyName != nil,
				}
		},
		migration.ObjectOrganization: func(fields map[string]string) (map[string]bool, map[string]bool) {
			in := organizationCreateFrom(fields, "src")
			up := organizationUpdateFrom(fields)
			return map[string]bool{
					"display_name": in.DisplayName != "",
					"legal_name":   in.LegalName != nil,
					"industry":     in.Industry != nil,
					"size_band":    in.SizeBand != nil,
					"description":  in.Description != nil,
				}, map[string]bool{
					"display_name": up.DisplayName != nil,
					"legal_name":   up.LegalName != nil,
					"industry":     up.Industry != nil,
					"size_band":    up.SizeBand != nil,
					"description":  up.Description != nil,
				}
		},
	} {
		t.Run(object, func(t *testing.T) {
			targets, err := importTargets(object)
			if err != nil {
				t.Fatalf("importTargets: %v", err)
			}
			fields := make(map[string]string, len(targets))
			for _, target := range targets {
				fields[target] = "value"
			}
			created, patched := build(fields)
			for _, target := range targets {
				if !created[target] {
					t.Errorf("%s: target %q is advertised but never reaches the create input", object, target)
				}
				if !patched[target] {
					t.Errorf("%s: target %q is advertised but never reaches the update input", object, target)
				}
			}
		})
	}
}

// A custom-field column is not offered, because the caller-opened transaction
// the import writes through refuses custom fields: offering one would accept a
// column, report the row as written, and drop the value.
func TestImportTargetsOfferNoCustomFields(t *testing.T) {
	for _, object := range []string{migration.ObjectLead, migration.ObjectOrganization} {
		targets, err := importTargets(object)
		if err != nil {
			t.Fatalf("importTargets(%s): %v", object, err)
		}
		for _, target := range targets {
			if len(target) > 3 && target[:3] == "cf_" {
				t.Errorf("%s advertises %q, which the import write path cannot carry", object, target)
			}
		}
	}
}

func TestChangedFieldsComparesEmailAsTheStoreWillHoldIt(t *testing.T) {
	stored := []byte(`{"email":"ada@lovelace.example","full_name":"Ada Lovelace"}`)

	// The store lowercases email on write. Compared raw, a file spelling it
	// differently would rewrite the row on every single re-import.
	changed, err := changedFields(stored, map[string]string{"email": "Ada@Lovelace.Example"})
	if err != nil {
		t.Fatalf("changedFields: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %v, want none: the stored value IS this value", changed)
	}

	// A real change is still a change.
	changed, err = changedFields(stored, map[string]string{"email": "ada@newplace.example"})
	if err != nil {
		t.Fatalf("changedFields: %v", err)
	}
	if changed["email"] != "ada@newplace.example" {
		t.Fatalf("changed = %v, want the new address", changed)
	}

	// Case is NOT folded on a field the store keeps verbatim.
	changed, err = changedFields(stored, map[string]string{"full_name": "ADA LOVELACE"})
	if err != nil {
		t.Fatalf("changedFields: %v", err)
	}
	if changed["full_name"] != "ADA LOVELACE" {
		t.Fatalf("changed = %v, want the renamed value: only email is canonicalized", changed)
	}
}

func TestMappingFromRefusesTwoColumnsOntoOneField(t *testing.T) {
	_, err := mappingFrom(migration.ObjectLead, crmcontracts.CreateImportRunRequest{
		Mapping: map[string]string{"Work Email": "email", "Personal Email": "email"},
	})
	if err == nil {
		t.Fatal("two columns onto one field were accepted; the row builder would pick one at random")
	}
}

func TestMappingFromRefusesATargetTheObjectDoesNotHave(t *testing.T) {
	_, err := mappingFrom(migration.ObjectLead, crmcontracts.CreateImportRunRequest{
		Mapping: map[string]string{"Revenue": "annual_revenue"},
	})
	if err == nil {
		t.Fatal("an unknown target was accepted; the run would fail at the first row instead")
	}
}

func TestMappingFromNeedsAColumnThatIdentifiesARow(t *testing.T) {
	// Nothing maps to email, and no explicit source key is given, so no row
	// could be recognized on a re-import or found by an undo.
	_, err := mappingFrom(migration.ObjectLead, crmcontracts.CreateImportRunRequest{
		Mapping: map[string]string{"Name": "full_name"},
	})
	if err == nil {
		t.Fatal("a mapping with no identifying column was accepted")
	}
}

func TestMappingFromRefusesASourceKeyThatIsNotMapped(t *testing.T) {
	key := "Some Other Column"
	_, err := mappingFrom(migration.ObjectLead, crmcontracts.CreateImportRunRequest{
		Mapping:   map[string]string{"Email": "email"},
		SourceKey: &key,
	})
	if err == nil {
		t.Fatal("a source key naming an unmapped column was accepted")
	}
}

func TestMappingFromDefaultsTheSourceKeyToTheIdentifyingColumn(t *testing.T) {
	mapping, err := mappingFrom(migration.ObjectLead, crmcontracts.CreateImportRunRequest{
		Mapping: map[string]string{"E-mail": "email", "Name": "full_name"},
	})
	if err != nil {
		t.Fatalf("mappingFrom: %v", err)
	}
	if mapping.SourceKey != "E-mail" {
		t.Fatalf("source key = %q, want the column mapped onto email", mapping.SourceKey)
	}
}

// A vanished upload is the caller's to fix by re-uploading, so it is named as
// missing rather than blamed on the server.
func TestImportProblemNamesAVanishedUploadAsNotFound(t *testing.T) {
	err := importProblem(errors.New("import source \"ws/import/x\": " + apperrors.ErrNotFound.Error()))
	if err == nil {
		t.Fatal("importProblem dropped the error")
	}
}

func TestToContractReportNeverSumsAPredictionWithAnOutcome(t *testing.T) {
	report := migration.Report{Objects: []migration.ObjectReport{{
		Object: migration.ObjectLead, MirrorCount: 3,
		WillCreate: 3, Created: 3,
	}}}

	awaiting := toContractImportReport(migration.Run{
		Status: migration.StatusAwaitingApproval, Report: &report,
		Mapping: &migration.RunMapping{Object: migration.ObjectLead},
	})
	if awaiting.Disposition.Created != 3 {
		t.Fatalf("awaiting created = %d, want the 3 it predicts", awaiting.Disposition.Created)
	}

	// The stored report carries both legs after a completed run merges them;
	// adding them would tell a human 6 rows landed out of a 3-row file.
	done := toContractImportReport(migration.Run{
		Status: migration.StatusComplete, Report: &report,
		Mapping: &migration.RunMapping{Object: migration.ObjectLead},
	})
	if done.Disposition.Created != 3 {
		t.Fatalf("completed created = %d, want the 3 that actually landed", done.Disposition.Created)
	}
}

// A flat file carries no edges, so an edge reaching the writer came from
// somewhere it does not understand. It is disclosed as not applied, never
// swallowed as done.
func TestCSVWritersDiscloseAnEdgeItCannotApply(t *testing.T) {
	w := &csvWriters{object: migration.ObjectLead}

	res, err := w.Associate(t.Context(), migration.Assoc{FromType: "lead", ToType: "organization"})
	if err != nil {
		t.Fatalf("Associate: %v", err)
	}
	if res.Applied {
		t.Fatal("an edge a delimited import cannot carry was reported as applied")
	}
	if res.Reason == "" {
		t.Fatal("the non-applied edge carries no reason, so the report cannot disclose it")
	}
}

// Nothing to repair: every landing commits the record and its identity row in
// one transaction, which is the answer the seam documents for such a writer.
func TestCSVWritersHaveNoIdentitiesToReconcile(t *testing.T) {
	w := &csvWriters{object: migration.ObjectLead}
	if err := w.ReconcileIdentities(t.Context()); err != nil {
		t.Fatalf("ReconcileIdentities: %v", err)
	}
}

// The run carries one object. A row for another one is an error rather than a
// quiet no-op: it would mean the source and the run disagree about what is
// being imported.
func TestCSVWritersRefuseARowForAnotherObject(t *testing.T) {
	w := &csvWriters{object: migration.ObjectLead, nativeIDs: map[string]ids.UUID{}}

	if _, err := w.Ensure(t.Context(), migration.ObjectOrganization, migration.Row{ExternalID: "x"}); err == nil {
		t.Fatal("a row for an object this run does not carry was accepted")
	}
}

// The stamp an imported row carries is the reserved import namespace, which the
// wire mappers refuse — so a client cannot pre-plant a row under a guessed
// import id and have the store hand it back as already imported.
func TestImportedRowsCarryTheReservedProvenance(t *testing.T) {
	w := &csvWriters{object: migration.ObjectLead}

	stamp := w.provenanceOf("ada@lovelace.example")
	if !strings.HasPrefix(stamp, provenance.ReservedSourceSystemPrefix) {
		t.Fatalf("provenance %q does not sit in the reserved import namespace", stamp)
	}
	if !strings.Contains(stamp, migration.ObjectLead) || !strings.Contains(stamp, "ada@lovelace.example") {
		t.Fatalf("provenance %q does not name the object and the source row", stamp)
	}
	if !strings.HasPrefix(csvSourceSystem(), provenance.ReservedSourceSystemPrefix) {
		t.Fatalf("source system %q is not reserved", csvSourceSystem())
	}
}

// A skip the SOURCE recorded never reached the writer, so nothing else in the
// report would mention it — it is folded into the object's own skips, with the
// line a human opens the file to.
func TestSkippedLinesReachTheObjectsReport(t *testing.T) {
	report := migration.Report{Objects: []migration.ObjectReport{{Object: migration.ObjectLead}}}

	out := withSkippedLines(report, migration.ObjectLead, []migration.SkippedLine{{Line: 7, Reason: "no key"}})

	if len(out.Objects[0].Skipped) != 1 {
		t.Fatalf("skips = %+v, want the source's own disclosure", out.Objects[0].Skipped)
	}
	if got := lineOf(out.Objects[0].Skipped[0].ExternalID); got != 7 {
		t.Fatalf("line = %d, want 7 — the report must send a human to the right line", got)
	}
	// An object the report does not carry cannot silently swallow them either.
	untouched := withSkippedLines(report, migration.ObjectOrganization, []migration.SkippedLine{{Line: 2}})
	if len(untouched.Objects[0].Skipped) != 1 {
		t.Fatalf("skips = %+v, want the lead's own single skip", untouched.Objects[0].Skipped)
	}
}

func TestColumnProfileReachesTheWireWithSamplesAndRate(t *testing.T) {
	out := toContractColumns(migration.Profile{Columns: []migration.Column{
		{Header: "Email", FillRate: 0.5, Samples: []string{"a@x.test"}},
		{Header: "Empty"},
	}})

	if len(out) != 2 {
		t.Fatalf("columns = %d, want 2", len(out))
	}
	if out[0].FillRate != 0.5 || len(out[0].Samples) != 1 {
		t.Fatalf("column = %+v, want the rate and the sample the profile carried", out[0])
	}
	// An empty column answers [], never null: the contract promises an array.
	if out[1].Samples == nil {
		t.Fatal("a column with no samples serialized as null")
	}
}
