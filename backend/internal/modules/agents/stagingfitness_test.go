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
// Two boundaries this walk does NOT cover, stated so neither reads as covered:
//   - update_record stages through stageConflicts, not StageInfo, so it is
//     invisible here; TestUpdateRecordRefusesStagingForATargetHeldElsewhere is
//     its pin, and that test fails if its guard is removed.
//   - the walk builds only RegisterCoreTools. Every stageable tool lives there
//     today; one registered by another family would escape, which is what the
//     count assertion below is for — it changes the moment the core set does.

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
	person, lead, deal, stage := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()
	args := map[string]string{
		"archive_record": fmt.Sprintf(`{"record_type":"person","id":%q}`, person),
		"promote_lead":   fmt.Sprintf(`{"lead_id":%q,"trigger":"meeting_booked"}`, lead),
		"merge_records":  fmt.Sprintf(`{"record_type":"person","source_id":%q,"target_id":%q}`, ids.NewV7(), person),
		"advance_deal":   fmt.Sprintf(`{"deal_id":%q,"to_stage_id":%q}`, deal, stage),
		"progress_deal":  fmt.Sprintf(`{"deal_id":%q,"to_stage_id":%q,"note":"n"}`, deal, stage),
	}

	registry := NewRegistry(&recordingApprovals{}, nil)
	// fixedStages only satisfies the constructor: refuseStagingElsewhere returns
	// before advance_deal/progress_deal reach StageSemantic, so its answer is
	// never read on this path.
	RegisterCoreTools(registry, elsewhereProvider{}, fixedStages{semantic: "won"}, nil, noConflicts{})

	// registry.tools IS the universe — walking Specs() and looking the name back
	// up adds a miss branch that could silently hide a tool from this pin.
	walked := 0
	for name, tool := range registry.tools {
		stageable, isStageable := tool.(stageableTool)
		if !isStageable {
			continue
		}
		walked++
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
	if walked != len(args) {
		t.Errorf("walked %d stageable core tools, pinned %d — the core set changed, so a staging site "+
			"may now be unexercised", walked, len(args))
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
