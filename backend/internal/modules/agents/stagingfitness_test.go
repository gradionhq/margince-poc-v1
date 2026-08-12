// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Derived coverage for the staging refusal (review-loop rule 2). Pinning the
// predicate alone cannot hold the invariant: refuseStagingElsewhere has to be
// CALLED by every tool that stages, and a per-site spec only ever covers the
// sites someone remembered. So this enumerates the core registry's
// StageInfo-shaped tools and requires each to refuse a target whose authority
// lives elsewhere — one with no args entry fails here, and one that forgets the
// guard fails here too.
//
// Two tools answer a DIFFERENT question here, and both are walked rather than
// excused:
//   - update_record stages twice over. Its per-field residue goes through
//     stageConflicts, which this walk cannot see —
//     TestUpdateRecordRefusesStagingForATargetHeldElsewhere is that path's pin.
//     Its StageInfo is the whole-call staging the contract's per-record-type
//     tier floor produces (#982), and that one IS walked here, because it reads
//     the record it patches exactly as its 🟡 siblings do.
//   - create_record stages a CREATE, which names no existing record, so there is
//     no target whose system of record could be elsewhere and nothing for
//     refuseStagingElsewhere to be called about. It is held to the invariant it
//     does have — stagesACreate below — rather than dropped from the count, so a
//     create that began inventing a target still fails something.
//
// One boundary this walk does NOT cover, stated so it does not read as covered:
//   - the walk builds only RegisterCoreTools and RegisterCommsTools. Every
//     stageable tool lives in one of the two today; one registered by a third
//     family would escape, which is what the count assertion below is for — it
//     changes the moment either set does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// elsewhereProvider serves every read as a record whose system of record is
// external — the shape overlay.Provider returns (Authoritative false).
type elsewhereProvider struct {
	datasource.SystemOfRecordProvider
}

func (elsewhereProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{Ref: ref, Fields: json.RawMessage(`{"full_name":"Mirrored"}`)}, nil
}

func TestEveryStageableToolRefusesATargetHeldElsewhere(t *testing.T) {
	person, lead, deal, stage, activity := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()
	args := map[string]string{
		"archive_record": fmt.Sprintf(`{"record_type":"person","id":%q}`, person),
		"promote_lead":   fmt.Sprintf(`{"lead_id":%q,"trigger":"meeting_booked"}`, lead),
		"merge_records":  fmt.Sprintf(`{"record_type":"person","source_id":%q,"target_id":%q}`, ids.NewV7(), person),
		"advance_deal":   fmt.Sprintf(`{"deal_id":%q,"to_stage_id":%q}`, deal, stage),
		"progress_deal":  fmt.Sprintf(`{"deal_id":%q,"to_stage_id":%q,"note":"n"}`, deal, stage),
		"send_message":   fmt.Sprintf(`{"activity_id":%q,"body":"b","consent_purpose":"support"}`, activity),
		"send_email":     fmt.Sprintf(`{"activity_id":%q,"to":["a@example.test"],"subject":"s","body":"b","consent_purpose":"support"}`, activity),
		// A booking anchors on no row, so its refusal has to come through a
		// LINK. Arguments with no links would read nothing and pass this walk
		// while proving nothing.
		"book_meeting": fmt.Sprintf(
			`{"start":"2026-08-03T09:00:00Z","end":"2026-08-03T09:30:00Z","subject":"s","links":[{"entity_type":"deal","entity_id":%q}]}`, deal),
		// The account-started send has no anchor either, and for the same
		// reason: it starts the conversation instead of answering one. Its
		// links are what carry the refusal here.
		"send_account_email": fmt.Sprintf(
			`{"to":["a@example.test"],"subject":"s","body":"b","consent_purpose":"support",`+
				`"links":[{"entity_type":"organization","entity_id":%q}]}`, ids.NewV7()),
		// The whole-call staging the tier floor produces (#982). It patches an
		// existing row, so it carries the same obligation as its siblings.
		"update_record": fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"full_name":"X"}}`, person),
	}
	// A create names no existing record, so it has no target to probe. What it
	// owes instead is to stage the shape it claims: the record TYPE, and no id —
	// an id here would be a target the approvals surface probes for row scope and
	// pins a version against, neither of which exists yet.
	stagesACreate := map[string]string{
		"create_record": `{"record_type":"person","fields":{"full_name":"Fresh"}}`,
	}

	registry := NewRegistry(&recordingApprovals{}, nil)
	// fixedStages only satisfies the constructor: refuseStagingElsewhere returns
	// before advance_deal/progress_deal reach StageSemantic, so its answer is
	// never read on this path.
	RegisterCoreTools(registry, elsewhereProvider{}, fixedStages{semantic: "won"}, nil, noConflicts{})
	RegisterCommsTools(registry, &recordingComms{}, elsewhereProvider{})

	// registry.tools IS the universe — walking Specs() and looking the name back
	// up adds a miss branch that could silently hide a tool from this pin.
	walked := 0
	for name, tool := range registry.tools {
		stageable, isStageable := tool.(stageableTool)
		if !isStageable {
			continue
		}
		walked++
		if in, creates := stagesACreate[name]; creates {
			info, err := stageable.StageInfo(context.Background(), json.RawMessage(in))
			if err != nil {
				t.Errorf("%s.StageInfo err = %v, want a staged create — a create reads no record, "+
					"so nothing here should refuse it", name, err)
			}
			if !info.TargetID.IsZero() {
				t.Errorf("%s staged target id %s; a create has no row yet, and naming one makes the "+
					"approvals surface probe and pin a record that does not exist", name, info.TargetID)
			}
			continue
		}
		in, known := args[name]
		if !known {
			t.Errorf("%s can stage an approval but this pin carries no arguments for it — "+
				"add them, so its refusal of an externally-held target is actually exercised", name)
			continue
		}
		if _, err := stageable.StageInfo(context.Background(), json.RawMessage(in)); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("%s.StageInfo err = %v, want ErrUnsupportedBySoR — it would mint an approval "+
				"no human can release, because redemption re-reads a row this record does not have", name, err)
		}
	}
	if pinned := len(args) + len(stagesACreate); walked != pinned {
		t.Errorf("walked %d stageable core tools, pinned %d — the core set changed, so a staging site "+
			"may now be unexercised", walked, pinned)
	}
}

// A merge touches TWO records, so validating only the pinned survivor leaves
// the other half unguarded: the merge archives and relinks the source, and an
// externally-held source under a locally-authoritative survivor is still a
// change no approval could release.
func TestMergeRefusesAnExternallyHeldSourceUnderALocalSurvivor(t *testing.T) {
	survivor, src := ids.NewV7(), ids.NewV7()
	survivorRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: survivor}
	sourceRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: src}
	p := &fakeSoR{records: map[datasource.EntityRef]datasource.Record{
		survivorRef: nativeRecord(datasource.Record{Ref: survivorRef, Fields: json.RawMessage(`{}`), Version: 4}),
		// Deliberately unstamped: this record's authority lives elsewhere.
		sourceRef: {Ref: sourceRef, Fields: json.RawMessage(`{}`)},
	}}

	_, err := mergeRecords{p: p}.StageInfo(context.Background(),
		json.RawMessage(fmt.Sprintf(`{"record_type":"person","source_id":%q,"target_id":%q}`, src, survivor)))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("StageInfo err = %v, want ErrUnsupportedBySoR — the merge source was not validated", err)
	}
}
