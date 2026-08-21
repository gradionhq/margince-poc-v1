// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The tag verbs (🟢 read + write, 🟡 archive): the workspace's own vocabulary
// for grouping records, which the tool surface could not touch at all.
//
// `tag` and `taggable` are real tables the web app uses, and no tool could
// list, create, apply or remove one — so the payoff of a capture flow ("add
// tag: K5 Conference 2026") could be described and never performed. The tag IS
// the outcome in those flows; without it the assistant reports work it did not
// do.
//
// Tags are a vocabulary, not records: workspace-shared, no owner, no row
// scope. That is why they get their own verbs rather than a `tag` record_type
// on create_record — the contract used to declare exactly that, naming tools
// which never served it.

import (
	"context"
	"encoding/json"

	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Tag is one word in the workspace's vocabulary.
type Tag struct {
	TagID ids.UUID `json:"tag_id"`
	Name  string   `json:"name"`
	Color string   `json:"color,omitempty"`
	// Archived says the word was retired. apply_tag will NOT reuse it — a
	// name whose only holder is archived is a conflict, not a match — so a
	// caller that cannot see this would read a refusal as a bug.
	Archived bool `json:"archived,omitempty"`
}

// Tags is the seam onto the collections module's tag paths.
type Tags interface {
	// ListTags answers the workspace's vocabulary, newest spelling rules and
	// all. Read before applying: apply_tag creates the word it is given, so a
	// caller who cannot see the existing ones coins near-duplicates.
	ListTags(ctx context.Context, includeArchived bool) ([]Tag, error)
	// EnsureTaggable refuses a record the caller cannot tag, before a tag is
	// created for it. Same check ApplyTag makes at the end of its own
	// transaction; asked earlier so a failed apply leaves nothing behind.
	EnsureTaggable(ctx context.Context, entityType string, entityID ids.UUID) error
	// FindTag answers the id of the live workspace tag with this name, or
	// ok=false when there is none. Remove uses it: a name that names nothing
	// means the tagging is already absent.
	FindTag(ctx context.Context, name string) (ids.UUID, bool, error)
	// EnsureTag answers the id of the workspace tag with this name, creating
	// it only when no such word exists. Reuse is the default because a
	// vocabulary that grows a near-duplicate per call stops being one.
	EnsureTag(ctx context.Context, name string) (ids.UUID, error)
	ApplyTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error
	RemoveTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error
}

// RegisterTagTools joins the tag verbs to the surface; a nil seam registers
// nothing.
func RegisterTagTools(r *Registry, tags Tags) {
	if tags == nil {
		return
	}
	r.Register(listTags{tags: tags})
	r.Register(applyTag{tags: tags})
	r.Register(removeTag{tags: tags})
}

// tagTargetEnum is the vocabulary a tagging may name, and it is the store's:
// person, organization, deal and lead are what `taggable` admits.
const tagTargetEnum = `["person","organization","deal","lead"]`

// taggingSchema is apply's and remove's argument shape, spelled once: they name
// the same three things, and two copies is two places for them to drift.
const taggingSchema = `{"type":"object","required":["record_type","record_id"],"properties":{
	"tag_id":{"type":"string","format":"uuid"},
	"tag_name":{"type":"string","maxLength":120,"description":"Instead of tag_id: the tag is created if the workspace has no such word"},
	"record_type":{"type":"string","enum":` + tagTargetEnum + `},
	"record_id":{"type":"string","format":"uuid"}},"additionalProperties":false}`

// --- list_tags (🟢 read) ---

type listTags struct{ tags Tags }

func (t listTags) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "list_tags", Title: "List tags", Version: toolVersionV1,
		Description:   listTagsCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listTags",
		InputSchema: schema(`{"type":"object","properties":{
			"include_archived":{"type":"boolean","description":"Also list retired words; they cannot be applied"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ListTagsResult](),
	}
}

func (t listTags) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		IncludeArchived bool `json:"include_archived"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	tags, err := t.tags.ListTags(ctx, args.IncludeArchived)
	if err != nil {
		return nil, err
	}
	// No noteEvidence, for the reason list_colleagues gives: a tag is
	// vocabulary, not a record the answer rests on. Stamping one would put
	// every word in the workspace into the evidence list of a call that only
	// asked what the words are.
	if tags == nil {
		tags = []Tag{}
	}
	return json.Marshal(ListTagsResult{Tags: tags})
}

// --- apply_tag / remove_tag (🟢 write) ---

type applyTag struct{ tags Tags }

func (t applyTag) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "apply_tag", Title: "Apply a tag to a record", Version: toolVersionV1,
		Description:   applyTagCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "applyTag",
		InputSchema:  schema(taggingSchema),
		OutputSchema: schemaFor[TagAppliedResult](),
	}
}

func (t applyTag) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := decodeTagging(in)
	if err != nil {
		return nil, err
	}
	// A name rather than an id is the capture flow's shape: "add tag: K5
	// Conference 2026" is one act to the person asking, and making them call a
	// create verb first only to pass its answer back is a second call that
	// exists for the surface's convenience rather than theirs. Reuse first —
	// an existing word wins over a new one, so tagging twice does not leave
	// two spellings of the same tag.
	if args.TagID.IsZero() {
		if args.TagName == "" {
			return nil, &BadArgsError{Cause: errors.New("give tag_id, or tag_name to reuse or create one")}
		}
		// The TARGET is authorized before anything is created. Creating first
		// left a live, audited tag behind when the record turned out not to
		// exist or to be outside the caller's scope — a write nobody asked for,
		// produced by a call that then failed.
		if err := t.tags.EnsureTaggable(ctx, args.RecordType, args.RecordID); err != nil {
			return nil, err
		}
		resolved, err := t.tags.EnsureTag(ctx, args.TagName)
		if err != nil {
			return nil, err
		}
		args.TagID = resolved
	}
	if err := t.tags.ApplyTag(ctx, args.TagID, args.RecordType, args.RecordID); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityType(args.RecordType), args.RecordID)
	return json.Marshal(TagAppliedResult{Applied: true, TagID: args.TagID,
		RecordType: args.RecordType, RecordID: args.RecordID})
}

type removeTag struct{ tags Tags }

func (t removeTag) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "remove_tag", Title: "Take a tag off a record", Version: toolVersionV1,
		Description:   removeTagCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "removeTag",
		InputSchema:  schema(taggingSchema),
		OutputSchema: schemaFor[TagAppliedResult](),
	}
}

func (t removeTag) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := decodeTagging(in)
	if err != nil {
		return nil, err
	}
	// A name resolves here too — the schema offers it, and offering an
	// argument that is silently ignored is worse than not offering it: the
	// call passed validation with a zero tag id and answered a misleading 404.
	//
	// It LOOKS UP, never creates. Minting a tag in order to remove it is
	// nonsense, and a name that names nothing means the tagging is already
	// absent, which is the state the caller asked for.
	if args.TagID.IsZero() {
		if args.TagName == "" {
			return nil, &BadArgsError{Cause: errors.New("give tag_id or tag_name")}
		}
		found, ok, err := t.tags.FindTag(ctx, args.TagName)
		if err != nil {
			return nil, err
		}
		if !ok {
			return json.Marshal(TagAppliedResult{Applied: false,
				RecordType: args.RecordType, RecordID: args.RecordID})
		}
		args.TagID = found
	}
	if err := t.tags.RemoveTag(ctx, args.TagID, args.RecordType, args.RecordID); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityType(args.RecordType), args.RecordID)
	return json.Marshal(TagAppliedResult{Applied: false, TagID: args.TagID,
		RecordType: args.RecordType, RecordID: args.RecordID})
}

type taggingArgs struct {
	TagID      ids.UUID `json:"tag_id"`
	TagName    string   `json:"tag_name"`
	RecordType string   `json:"record_type"`
	RecordID   ids.UUID `json:"record_id"`
}

// decodeTagging is shared so apply and remove cannot drift into reading the
// same three arguments differently.
func decodeTagging(in json.RawMessage) (taggingArgs, error) {
	var args taggingArgs
	if err := decodeArgs(in, &args); err != nil {
		return taggingArgs{}, err
	}
	return args, nil
}
