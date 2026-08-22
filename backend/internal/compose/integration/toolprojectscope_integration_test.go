// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Narrowing a read to ONE body of work, through the tool surface: the same
// "filed under this project, or filed under none" rule the timeline list and
// the record pages apply, reached by an agent naming project_id.
//
// The negative half is the proof. A scope that silently filters nothing reads
// in a brief exactly like one that works, so every test here asserts the
// other engagement's row is ABSENT and then asserts the unscoped answer still
// carries it, so the absence is the scope's doing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// toolData runs one tool through the governed registry and hands back the
// envelope's payload.
func toolData(ctx context.Context, t *testing.T, registry *agents.Registry, tool, args string) json.RawMessage {
	t.Helper()
	out, err := registry.Invoke(ctx, tool, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", tool, args, err)
	}
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("%s: the result is not an envelope: %v", tool, err)
	}
	return sealed.Data
}

// contextIDs flattens an assembled picture to the record ids it carried.
func contextIDs(t *testing.T, data json.RawMessage) map[string]bool {
	t.Helper()
	var picture agents.AssembledContextResult
	if err := json.Unmarshal(data, &picture); err != nil {
		t.Fatalf("decode the assembled picture: %v", err)
	}
	out := map[string]bool{}
	for _, section := range picture.Sections {
		for _, item := range section.Items {
			out[item.RecordID.String()] = true
		}
	}
	return out
}

func TestCatchMeUpOnScopedToAProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	anchor := fmt.Sprintf(`"record_type":"person","record_id":%q,"max_items":20`, f.person.String())

	scoped := contextIDs(t, toolData(ctx, t, registry, "catch_me_up_on",
		fmt.Sprintf(`{%s,"project_id":%q}`, anchor, f.erp.String())))
	if scoped[f.onOther] {
		t.Error("catch_me_up_on carried the other engagement's mail into a picture scoped to ERP-27")
	}
	if !scoped[f.onERP] {
		t.Error("the scoped project's own mail is missing from the picture")
	}
	if !scoped[f.unfiled] {
		t.Error("the picture dropped mail filed under no project; the rule keeps it")
	}

	wide := contextIDs(t, toolData(ctx, t, registry, "catch_me_up_on", `{`+anchor+`}`))
	if !wide[f.onOther] {
		t.Error("an unscoped catch-up lost the other engagement, so the scoped absence proves nothing")
	}
}

// prep_for_meeting on the meeting itself takes the brief route. The brief is
// scoped by the project the meeting is filed under, so a project_id naming
// the SAME project is agreement and the brief says which project that is.
// A project_id naming ANOTHER project is not an argument error but a scope
// the meeting is outside of: the scoped walk in front of the brief answers
// not-found, existence-hiding, and the brief is never read.
func TestPrepForMeetingUnderTheMeetingsOwnProjectNamesItAndUnderAnotherIsNotFound(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	meeting := fmt.Sprintf(`"record_type":"activity","record_id":%q`, f.erpMeeting)

	agreeing := toolData(ctx, t, registry, "prep_for_meeting",
		fmt.Sprintf(`{%s,"project_id":%q}`, meeting, f.erp.String()))
	var prepared agents.PrepForMeetingResult
	if err := json.Unmarshal(agreeing, &prepared); err != nil {
		t.Fatalf("decode the prep: %v", err)
	}
	if prepared.Brief == nil {
		t.Fatal("the meeting anchored prep carried no brief")
	}
	if prepared.Brief.ProjectID == nil || *prepared.Brief.ProjectID != f.erp.UUID {
		t.Errorf("brief.project_id = %v, want the project the meeting is filed under (%s)", prepared.Brief.ProjectID, f.erp)
	}

	_, err := registry.Invoke(ctx, "prep_for_meeting",
		json.RawMessage(fmt.Sprintf(`{%s,"project_id":%q}`, meeting, f.other.String())))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("a project_id the meeting is not filed under = %v, want ErrNotFound", err)
	}
}

func TestReviewCommitmentsScopedToAProjectDropsTheOtherEngagementsPromise(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	commitments := func(args string) map[string]bool {
		t.Helper()
		var result agents.ReviewCommitmentsResult
		if err := json.Unmarshal(toolData(ctx, t, registry, "review_commitments", args), &result); err != nil {
			t.Fatalf("decode the commitments: %v", err)
		}
		out := map[string]bool{}
		for _, item := range result.Commitments {
			out[item.TaskID.String()] = true
		}
		return out
	}

	scoped := commitments(fmt.Sprintf(`{"project_id":%q}`, f.erp.String()))
	if scoped[f.otherTask] {
		t.Error("review_commitments scoped to ERP-27 still reports the other engagement's task")
	}
	if !scoped[f.erpTask] {
		t.Error("the scoped project's own task is missing from the sweep")
	}
	wide := commitments(`{}`)
	if !wide[f.otherTask] {
		t.Error("an unscoped sweep lost the other engagement's task, so the scoped absence proves nothing")
	}
}

// A project_id is a read of the project it names, on every tool that takes
// one: no project grant refuses it outright, and a project the caller cannot
// see — here, one that does not exist — answers as not found rather than as a
// full, unscoped picture that looks as though the filter matched.
func TestAProjectScopeTheCallerCannotSeeIsRefusedThroughTheToolSurface(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	granted := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	noProjectGrant := e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(roomPerms, "project"))
	anchor := fmt.Sprintf(`"record_type":"person","record_id":%q`, f.person.String())

	for _, call := range []struct{ tool, args string }{
		{"catch_me_up_on", `{` + anchor + `,"project_id":%q}`},
		{"prep_for_meeting", `{` + anchor + `,"project_id":%q}`},
		{"review_commitments", `{"project_id":%q}`},
	} {
		t.Run(call.tool, func(t *testing.T) {
			_, err := registry.Invoke(noProjectGrant, call.tool, json.RawMessage(fmt.Sprintf(call.args, f.erp.String())))
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("without the project grant = %v, want ErrPermissionDenied", err)
			}
			_, err = registry.Invoke(granted, call.tool, json.RawMessage(fmt.Sprintf(call.args, ids.NewV7().String())))
			if !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("a project that does not exist = %v, want ErrNotFound", err)
			}
		})
	}
}

// An activity ANCHOR filed under another project is outside the scoped
// picture entirely: not-found, the same answer an invisible one gives, rather
// than the room and its participants handed over under a scope that claims to
// have excluded them. This holds on both routes prep_for_meeting takes — the
// brief for a meeting, and the assembled-picture fallback for any other
// activity — because the fallback is the same scoped walk.
func TestAnActivityAnchorFiledUnderAnotherProjectIsNotFoundUnderTheScope(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)

	for _, call := range []struct{ name, tool, anchor string }{
		{"catch up on the other engagement's meeting", "catch_me_up_on", f.otherMeeting},
		{"prep for the other engagement's meeting", "prep_for_meeting", f.otherMeeting},
		{"prep anchored on the other engagement's mail, which has no brief", "prep_for_meeting", f.onOther},
	} {
		t.Run(call.name, func(t *testing.T) {
			args := fmt.Sprintf(`{"record_type":"activity","record_id":%q,"project_id":%q}`, call.anchor, f.erp.String())
			_, err := registry.Invoke(ctx, call.tool, json.RawMessage(args))
			if !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("%s scoped to ERP-27 = %v, want ErrNotFound", call.tool, err)
			}
			// The same anchor under its own project, and under no scope, still
			// answers — so the refusal above is the scope's doing.
			for _, scope := range []string{fmt.Sprintf(`,"project_id":%q`, f.other.String()), ""} {
				own := fmt.Sprintf(`{"record_type":"activity","record_id":%q%s}`, call.anchor, scope)
				if _, err := registry.Invoke(ctx, call.tool, json.RawMessage(own)); err != nil {
					t.Errorf("%s(%s): %v, want an answer", call.tool, own, err)
				}
			}
		})
	}
}
