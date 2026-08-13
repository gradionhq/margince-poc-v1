// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
