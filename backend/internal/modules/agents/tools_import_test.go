// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A commit starts only from awaiting_approval, which is how the mandatory dry
// run is enforced: a run reaches that state ONLY by producing a report, so
// requiring the state is requiring the report. There is no verb that imports
// without one.
func TestACommitRefusesARunThatHasNotBeenDryRun(t *testing.T) {
	for _, status := range []string{"validating", "running", "complete", "failed", "undoing"} {
		t.Run(status, func(t *testing.T) {
			err := refuseUncommittableRun(crmcontracts.ImportRun{
				Id:     openapi_types.UUID(ids.NewV7()),
				Status: crmcontracts.ImportRunStatus(status),
			})
			if err == nil {
				t.Fatalf("a %s run was accepted for commit; only awaiting_approval may commit", status)
			}
			if !strings.Contains(err.Error(), status) {
				t.Errorf("the refusal does not say which state the run is in: %v", err)
			}
		})
	}
	if err := refuseUncommittableRun(crmcontracts.ImportRun{
		Id: openapi_types.UUID(ids.NewV7()), Status: awaitingApproval,
	}); err != nil {
		t.Errorf("a dry-run run was refused: %v", err)
	}
}

// A column nothing reads is reported, not dropped in silence.
//
// A caller who mistyped a field name would otherwise get a clean report and a
// column quietly missing from every imported row — which looks exactly like a
// successful import until somebody goes looking for the data.
func TestUnmappedColumnsAreNamedRatherThanDroppedSilently(t *testing.T) {
	profile := crmcontracts.ImportSourceProfile{Columns: []crmcontracts.ImportColumn{
		{Header: "Company"}, {Header: "Website"}, {Header: "Internal Ref"},
	}}
	got := unmappedColumns(profile, map[string]string{
		"Company": "display_name",
		"Website": "", // mapped to nothing, which is the same as unmapped
	})
	want := map[string]bool{"Website": true, "Internal Ref": true}
	if len(got) != len(want) {
		t.Fatalf("unmapped = %v, want the two columns nothing reads", got)
	}
	for _, column := range got {
		if !want[column] {
			t.Errorf("%q was reported unmapped and it is mapped", column)
		}
	}
}

// `object` takes lead or organization. Not person, and not deal.
//
// This is ADR-0008 in the schema: a bulk file of contacts lands as leads, which
// a human promotes, so machine-sourced rows cannot enter as contacts by the
// choice of an enum value. It also has to match the REST contract's own enum,
// which is what TestEveryToolEnumMatchesTheContractItMirrors holds it to.
func TestAFileOfPeopleCannotBeImportedAsContacts(t *testing.T) {
	for _, object := range []string{"person", "deal", "activity", ""} {
		if err := refuseUnimportableObject(object); err == nil {
			t.Errorf("`object` accepted %q; a file imports as lead or organization", object)
		}
	}
	for _, object := range []string{importObjectLead, importObjectOrganization} {
		if err := refuseUnimportableObject(object); err != nil {
			t.Errorf("`object` refused %q: %v", object, err)
		}
	}
}

// An empty file is refused before anything is stored.
func TestAnEmptyFileIsRefusedRatherThanStored(t *testing.T) {
	var stored bool
	_, err := previewImport{imports: recordingImports{stored: &stored}}.Handle(
		context.Background(), json.RawMessage(`{"object":"lead","csv":"   \n  "}`))
	if err == nil {
		t.Fatal("an empty file was accepted")
	}
	if stored {
		t.Error("an empty file reached the object store")
	}
}

// The caller's mapping wins over the proposal, column by column, and the
// proposal fills the rest — so a caller correcting one guess does not have to
// restate the ones that were right.
func TestACallersMappingOverridesTheProposalColumnByColumn(t *testing.T) {
	out, err := previewImport{imports: recordingImports{
		suggested: map[string]string{"Company": "legal_name", "Website": "domains"},
	}}.Handle(context.Background(), json.RawMessage(
		`{"object":"organization","csv":"Company,Website\nAcme,acme.test\n",`+
			`"mapping":{"Company":"display_name"}}`))
	if err != nil {
		t.Fatalf("previewing: %v", err)
	}
	var got ImportPreviewResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Mapping["Company"] != "display_name" {
		t.Errorf("the caller's mapping lost to the proposal: Company = %q", got.Mapping["Company"])
	}
	if got.Mapping["Website"] != "domains" {
		t.Errorf("the proposal did not fill an unstated column: Website = %q", got.Mapping["Website"])
	}
}

type recordingImports struct {
	stubImports
	stored    *bool
	suggested map[string]string
}

func (r recordingImports) ProfileSource(
	_ context.Context, object, _ string,
) (crmcontracts.ImportSourceProfile, error) {
	if r.stored != nil {
		*r.stored = true
	}
	return crmcontracts.ImportSourceProfile{
		Object:           crmcontracts.ImportObject(object),
		SuggestedMapping: r.suggested,
	}, nil
}
