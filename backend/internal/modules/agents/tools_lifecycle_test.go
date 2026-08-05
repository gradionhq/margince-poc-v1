// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What these tools decide BEFORE the seam is the part that is theirs: the
// admission a bad argument meets, and — for the two confirm-first ones — the
// refusal a human must never be asked to approve. Everything past the seam is
// the owning module's own behaviour, tested there.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seamNotReached fails the test if the tool passed arguments through that it
// should have refused: a refusal that happens AFTER the write is not a refusal.
var errLifecycleSeamReached = errors.New("the seam was reached")

type recordingRelinker struct{ entityType string }

func (r *recordingRelinker) RelinkActivity(
	_ context.Context, _ ids.UUID, entityType string, _ ids.UUID, _ bool,
) (json.RawMessage, error) {
	r.entityType = entityType
	return json.RawMessage(`{}`), nil
}

type unreachableAdvancer struct{}

func (unreachableAdvancer) AdvanceProjectPhase(
	context.Context, ids.UUID, string, *string, *int64,
) (json.RawMessage, error) {
	return nil, errLifecycleSeamReached
}

// recordingAdvancer captures what the tool decided to pass on.
type recordingAdvancer struct{ ifVersion *int64 }

func (a *recordingAdvancer) AdvanceProjectPhase(
	_ context.Context, _ ids.UUID, _ string, _ *string, ifVersion *int64,
) (json.RawMessage, error) {
	a.ifVersion = ifVersion
	return json.RawMessage(`{}`), nil
}

func TestRelinkActivityRefusesATargetTypeTheStoreWouldNotAccept(t *testing.T) {
	seam := &recordingRelinker{}
	tool := relinkActivity{relinker: seam}

	args := func(entityType string) json.RawMessage {
		return json.RawMessage(`{"activity_id":"` + ids.NewV7().String() +
			`","entity_type":"` + entityType + `","entity_id":"` + ids.NewV7().String() + `"}`)
	}

	if _, err := tool.Handle(context.Background(), args("activity")); err == nil {
		t.Fatal("linking an activity to an activity was accepted; the store's own enum refuses it")
	} else if seam.entityType != "" {
		t.Fatalf("the refusal reached the seam first (entity_type=%q)", seam.entityType)
	}

	if _, err := tool.Handle(context.Background(), args("person")); err != nil {
		t.Fatalf("a person is a link target: %v", err)
	}
	if seam.entityType != "person" {
		t.Fatalf("seam saw entity_type %q, want person", seam.entityType)
	}
}

// Closing a project without a reason is a 422 the contract states, so a call
// that stages, waits for a human, and THEN fails is a person asked to decide
// something that could never have applied.
func TestAdvanceProjectPhaseRefusesAClosureWithNoReasonBeforeStaging(t *testing.T) {
	tool := advanceProjectPhase{advancer: unreachableAdvancer{}}
	closing := json.RawMessage(`{"project_id":"` + ids.NewV7().String() + `","to_phase":"closed"}`)

	if _, err := tool.StageInfo(context.Background(), closing); err == nil {
		t.Fatal("a reasonless closure was staged; a human would approve a call the store then refuses")
	} else if !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("err = %v, want the reason requirement named", err)
	}

	if _, err := tool.Handle(context.Background(), closing); err == nil {
		t.Fatal("a reasonless closure reached the seam")
	}
}

func TestAdvanceProjectPhaseRefusesAPhaseOutsideTheLadder(t *testing.T) {
	tool := advanceProjectPhase{advancer: unreachableAdvancer{}}
	_, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+ids.NewV7().String()+`","to_phase":"archived"}`))
	if err == nil || !strings.Contains(err.Error(), "not a project phase") {
		t.Fatalf("err = %v, want the phase refused by name", err)
	}
}

// The pin is the CALLER's or it is nothing: a version the tool read itself
// would be compared against itself and could never detect skew.
func TestAdvanceProjectPhasePassesTheCallersVersionThrough(t *testing.T) {
	seam := &recordingAdvancer{}
	tool := advanceProjectPhase{advancer: seam}
	id := ids.NewV7().String()

	if _, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+id+`","to_phase":"delivering","if_version":7}`)); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion == nil || *seam.ifVersion != 7 {
		t.Fatalf("if_version reached the store as %v, want 7", seam.ifVersion)
	}

	seam.ifVersion = nil
	if _, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+id+`","to_phase":"delivering"}`)); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion != nil {
		t.Fatalf("an omitted if_version became %v — the tool invented a pin", *seam.ifVersion)
	}
}
