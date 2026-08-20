// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stubTags answers the seam without a store, and records what it was asked.
type stubTags struct {
	applied, removed *taggingArgs
	ensured          *string
}

func (stubTags) EnsureTaggable(context.Context, string, ids.UUID) error { return nil }

func (s stubTags) FindTag(_ context.Context, name string) (ids.UUID, bool, error) {
	if s.ensured != nil {
		*s.ensured = name
	}
	return ids.NewV7(), true, nil
}

func (s stubTags) EnsureTag(_ context.Context, name string) (ids.UUID, error) {
	if s.ensured != nil {
		*s.ensured = name
	}
	return ids.NewV7(), nil
}
func (s stubTags) ApplyTag(_ context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	if s.applied != nil {
		*s.applied = taggingArgs{TagID: tagID, RecordType: entityType, RecordID: entityID}
	}
	return nil
}
func (s stubTags) RemoveTag(_ context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	if s.removed != nil {
		*s.removed = taggingArgs{TagID: tagID, RecordType: entityType, RecordID: entityID}
	}
	return nil
}

// The tag IS the outcome of a capture flow ("add tag: K5 Conference 2026"),
// and no tool could perform it — so an assistant described work it had not
// done. Both halves are held here, because a vocabulary with no way back is
// the shape that made archive_record the only undo: retiring the word for
// everybody to correct one mistaken tagging.
func TestApplyAndRemoveReachTheSameTaggingBothWays(t *testing.T) {
	tag, org := ids.NewV7(), ids.NewV7()
	args := json.RawMessage(`{"tag_id":"` + tag.String() + `","record_type":"organization",` +
		`"record_id":"` + org.String() + `"}`)

	var applied, removed taggingArgs
	if _, err := (applyTag{tags: stubTags{applied: &applied}}).Handle(context.Background(), args); err != nil {
		t.Fatalf("applying answered %v", err)
	}
	if applied.TagID != tag || applied.RecordType != "organization" || applied.RecordID != org {
		t.Errorf("apply reached the seam as %+v, want the tag on that organization", applied)
	}
	if _, err := (removeTag{tags: stubTags{removed: &removed}}).Handle(context.Background(), args); err != nil {
		t.Fatalf("removing answered %v", err)
	}
	if removed != applied {
		t.Errorf("remove reached the seam as %+v, want the same tagging apply made", removed)
	}
}

// A NAME rather than an id is the capture flow's shape: "add tag: K5
// Conference 2026" is one act to the person asking. Making them call a create
// verb first, only to hand its answer straight back, is a second call that
// exists for the surface's convenience rather than theirs.
func TestApplyTagTakesANameAndReusesOrCreatesTheWord(t *testing.T) {
	var ensured string
	var applied taggingArgs
	_, err := (applyTag{tags: stubTags{applied: &applied, ensured: &ensured}}).Handle(
		context.Background(),
		json.RawMessage(`{"tag_name":"K5 Conference 2026","record_type":"organization",`+
			`"record_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatalf("applying by name answered %v, want the tag resolved", err)
	}
	if ensured != "K5 Conference 2026" {
		t.Errorf("the seam was asked to ensure %q, want the name as given", ensured)
	}
	if applied.TagID.IsZero() {
		t.Error("the tagging carries no tag id, want the resolved one")
	}
}

// Neither an id nor a name is nothing to tag with, and the refusal says which
// of the two to send.
func TestApplyTagRefusesWithNeitherIDNorName(t *testing.T) {
	_, err := (applyTag{tags: stubTags{}}).Handle(context.Background(),
		json.RawMessage(`{"record_type":"organization","record_id":"`+ids.NewV7().String()+`"}`))
	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("answered %v, want a BadArgsError naming tag_id or tag_name", err)
	}
}

// The target is authorized BEFORE a tag is created for it.
//
// Creating first left a live, audited word behind whenever the record turned
// out not to exist or to sit outside the caller's row scope — a write nobody
// asked for, produced by a call that then failed.
func TestApplyTagChecksTheRecordBeforeMintingAWord(t *testing.T) {
	var ensured string
	_, err := (applyTag{tags: refusingTaggable{ensured: &ensured}}).Handle(context.Background(),
		json.RawMessage(`{"tag_name":"K5 Conference 2026","record_type":"organization",`+
			`"record_id":"`+ids.NewV7().String()+`"}`))
	if err == nil {
		t.Fatal("applying to an unreachable record answered success, want the refusal")
	}
	if ensured != "" {
		t.Errorf("a tag named %q was created for a record the caller cannot tag", ensured)
	}
}

// A name that names nothing means the tagging is already absent — the state
// the caller asked for. It must not mint a word in order to remove it.
func TestRemoveTagByAnUnknownNameSucceedsWithoutCreatingIt(t *testing.T) {
	out, err := (removeTag{tags: noSuchTag{}}).Handle(context.Background(),
		json.RawMessage(`{"tag_name":"never used","record_type":"deal",`+
			`"record_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatalf("removing an unknown tag answered %v, want success", err)
	}
	var got TagAppliedResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Applied {
		t.Error("the result claims a tagging was applied")
	}
}

type refusingTaggable struct {
	stubTags
	ensured *string
}

func (r refusingTaggable) EnsureTaggable(context.Context, string, ids.UUID) error {
	return errNoSuchRecord
}

func (r refusingTaggable) EnsureTag(_ context.Context, name string) (ids.UUID, error) {
	if r.ensured != nil {
		*r.ensured = name
	}
	return ids.NewV7(), nil
}

var errNoSuchRecord = errors.New("no such record in this workspace")

type noSuchTag struct{ stubTags }

func (noSuchTag) FindTag(context.Context, string) (ids.UUID, bool, error) {
	return ids.UUID{}, false, nil
}
