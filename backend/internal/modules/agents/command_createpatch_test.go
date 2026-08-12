// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The create and patch resolvers are the seam both doors ask, exactly as
// command_test.go pins the archive resolver's answers. This file pins
// createResolver's and patchResolver's own: what each refuses, what each
// stages, and — for patch — what happens when the record seam has never
// heard of the type.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// A record type create_record's own write path cannot make at all still
// stages through createResolver — unlike patch, create has no target to
// read, so there is no seam to stand down on, but the same door-agnostic
// principle holds: whether this VERB can make a custom_field is a fact about
// create_record's own Handle, not about the operation, and the resolver has
// no way to tell whether a REST create (whose own module's handler performs
// it fine) or a raw tool call (which cannot) supplied this command. That
// question is asked at the door that owns the answer —
// TestCreateRecordStageInfoRefusesARecordTypeItCannotWrite below, on
// createRecord.StageInfo itself, before the command is ever built.
func TestCreateStagesARecordTypeTheResolverHasNoOpinionOn(t *testing.T) {
	call := NewCreateCall(CreateCommand{RecordType: "custom_field", Fields: json.RawMessage(`{"name":"x"}`)})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging a custom_field create through the resolver answered %v, want it staged — "+
			"createResolver.Guards has no opinion on whether create_record itself can write this type", err)
	}
	if info.TargetType != "custom_field" {
		t.Errorf("staged target_type = %q, want \"custom_field\"", info.TargetType)
	}
	if !info.TargetID.IsZero() {
		t.Errorf("staged target_id = %s, want zero", info.TargetID)
	}
}

// createRecord.StageInfo (tools.go) is the ONE door where "does this verb
// serve this record type" is a real question: create_record's own Handle
// writes exclusively through datasource.SystemOfRecordProvider.Create, which
// cannot express a type outside createShapes, so staging one here would mint
// an approval whose approved retry dies at the provider with the authority
// already spent. Checked at the door, before the command is even built —
// createResolver.Guards itself has no such refusal (proved above), because
// the identical command reaching it from REST names an operation whose own
// module's handler performs the create fine.
func TestCreateRecordStageInfoRefusesARecordTypeItCannotWrite(t *testing.T) {
	tool := createRecord{}

	_, err := tool.StageInfo(context.Background(), json.RawMessage(`{"record_type":"custom_field","fields":{}}`))
	var badArgs *BadArgsError
	if !errors.As(err, &badArgs) {
		t.Fatalf("staging a create_record call naming a type this verb cannot create answered %v, want a "+
			"BadArgsError — an approval for it could never be carried out", err)
	}
}

// A `fields` key the record type does not accept is refused by name, not
// silently dropped.
func TestCreateGuardsRefuseAnUnknownField(t *testing.T) {
	call := NewCreateCall(CreateCommand{RecordType: "person", Fields: json.RawMessage(`{"nickname":"Bob"}`)})

	err := call.Guards(context.Background())
	var badArgs *BadArgsError
	if !errors.As(err, &badArgs) {
		t.Fatalf("guarding an unknown field answered %v, want a BadArgsError naming it", err)
	}
	if !strings.Contains(err.Error(), "nickname") {
		t.Errorf("refusal %q does not name the offending field", err.Error())
	}
}

// A served create stages the record TYPE with no id and no pin: the row does
// not exist yet, so there is nothing for either to describe.
func TestCreateStagesAServedTypeWithNoTargetID(t *testing.T) {
	call := NewCreateCall(CreateCommand{RecordType: "person", Fields: json.RawMessage(`{"full_name":"Ada"}`)})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging a served create answered %v, want it staged", err)
	}
	if info.TargetType != "person" {
		t.Errorf("staged target_type = %q, want \"person\"", info.TargetType)
	}
	if !info.TargetID.IsZero() {
		t.Errorf("staged target_id = %s, want zero — a create names no existing row an approval could pin",
			info.TargetID)
	}
	if info.TargetVersion != nil {
		t.Errorf("the resolver supplied target_version %d for a record that does not exist yet", *info.TargetVersion)
	}
}

// A `fields` key the record type does not accept is refused before the
// target is ever read — the same order updateRecord.StageInfo always used.
func TestPatchGuardsRefuseAnUnknownField(t *testing.T) {
	call := NewPatchCall(unreadableProvider{}, PatchCommand{
		RecordType: "person", ID: ids.NewV7(), Fields: json.RawMessage(`{"nickname":"Bob"}`),
	})

	err := call.Guards(context.Background())
	var badArgs *BadArgsError
	if !errors.As(err, &badArgs) {
		t.Fatalf("guarding an unknown field answered %v, want a BadArgsError naming it", err)
	}
	if !strings.Contains(err.Error(), "nickname") {
		t.Errorf("refusal %q does not name the offending field", err.Error())
	}
}

// A target the caller cannot see is refused BEFORE anything is staged, the
// same row-scope answer archive's own Guards gives.
func TestPatchGuardsRefuseATargetTheCallerCannotSee(t *testing.T) {
	call := NewPatchCall(unreadableProvider{}, PatchCommand{
		RecordType: "person", ID: ids.NewV7(), Fields: json.RawMessage(`{"full_name":"X"}`),
	})

	if err := call.Guards(context.Background()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("guarding an unreadable person answered %v, want the row-scope miss", err)
	}
}

// A target whose authority lives in another system of record can never have
// an approval released for it.
func TestPatchGuardsRefuseATargetHeldElsewhere(t *testing.T) {
	call := NewPatchCall(elsewhereProvider{}, PatchCommand{
		RecordType: "person", ID: ids.NewV7(), Fields: json.RawMessage(`{"full_name":"X"}`),
	})

	if err := call.Guards(context.Background()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("guarding a mirrored person answered %v, want the unsupported-by-SoR refusal", err)
	}
}

// A served patch stages the record TYPE and ID, and no version pin — the pin
// is taken server-side inside the staging transaction.
func TestPatchStagesAServedRecordAndID(t *testing.T) {
	id := ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityPerson, id, true)}
	call := NewPatchCall(provider, PatchCommand{
		RecordType: "person", ID: id, Fields: json.RawMessage(`{"full_name":"Ada"}`),
	})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging a readable patch answered %v, want it staged", err)
	}
	if info.TargetType != "person" || info.TargetID != id {
		t.Errorf("staged target = (%s,%s), want (person,%s)", info.TargetType, info.TargetID, id)
	}
	if info.TargetVersion != nil {
		t.Errorf("the resolver supplied target_version %d — the pin comes from inside the staging transaction",
			*info.TargetVersion)
	}
}

// A patch record type the record seam has never heard of still stages, with
// its own type and id — patchResolver.Guards has no standalone "verb does not
// serve" refusal (unlike create's: update_record's pre-#982 body never had
// one), so the servedByTheRecordSeam short-circuit stands down rather than
// hard-refusing. Staged against a provider that fails EVERY read, so a
// resolver that consulted the seam anyway is caught here rather than passing
// on a lenient stub.
func TestPatchStagesATypeTheRecordSeamDoesNotServe(t *testing.T) {
	id := ids.NewV7()
	call := NewPatchCall(unreadableProvider{}, PatchCommand{
		RecordType: "webhook_subscription", ID: id, Fields: json.RawMessage(`{"state":"paused"}`),
	})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging a webhook_subscription patch answered %v, want it staged — PATCH "+
			"/v1/webhook-subscriptions/{id} is a governed operation whose target the seam simply does not serve", err)
	}
	if info.TargetType != "webhook_subscription" || info.TargetID != id {
		t.Errorf("staged target = (%s,%s), want (webhook_subscription,%s)",
			info.TargetType, info.TargetID, id)
	}
}

// Guards and Subject must describe the SAME reading of the row: Subject
// itself reads nothing for a patch (its summary names the fields, not the
// record — describeGenericWrite, not recordLabel), so the only read a staged
// patch costs is Guards' own.
func TestPatchReadsItsTargetOnceAcrossGuardsAndSubject(t *testing.T) {
	provider := &countingProvider{}
	call := NewPatchCall(provider, PatchCommand{
		RecordType: "person", ID: ids.NewV7(), Fields: json.RawMessage(`{"full_name":"X"}`),
	})

	if _, err := StageSubject(context.Background(), call); err != nil {
		t.Fatalf("staging a readable patch answered %v, want it staged", err)
	}
	if provider.reads != 1 {
		t.Errorf("the resolver read its target %d times, want 1", provider.reads)
	}
}
