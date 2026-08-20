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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Tag is one entry in the workspace vocabulary.
type Tag struct {
	TagID    ids.UUID `json:"tag_id"`
	Name     string   `json:"name"`
	Color    string   `json:"color,omitempty"`
	Archived bool     `json:"archived"`
}

// Tags is the seam onto the collections module's tag paths.
type Tags interface {
	ListTags(ctx context.Context, includeArchived bool) ([]Tag, error)
	CreateTag(ctx context.Context, name, color string) (Tag, error)
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
	r.Register(createTag{tags: tags})
	r.Register(applyTag{tags: tags})
	r.Register(removeTag{tags: tags})
}

// tagTargetEnum is the vocabulary a tagging may name, and it is the store's:
// person, organization, deal and lead are what `taggable` admits.
const tagTargetEnum = `["person","organization","deal","lead"]`

// --- list_tags (🟢 read) ---

type listTags struct{ tags Tags }

func (t listTags) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "list_tags", Title: "List tags", Version: toolVersionV1,
		Description:   listTagsCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listTags",
		InputSchema: schema(`{"type":"object","properties":{
			"include_archived":{"type":"boolean"}},"additionalProperties":false}`),
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
	if tags == nil {
		tags = []Tag{}
	}
	return json.Marshal(ListTagsResult{Tags: tags})
}

// --- create_tag (🟢 write: adds a word, changes no record) ---

type createTag struct{ tags Tags }

func (t createTag) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "create_tag", Title: "Create a tag", Version: toolVersionV1,
		Description:   createTagCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "createTag",
		InputSchema: schema(`{"type":"object","required":["name"],"properties":{
			"name":{"type":"string","maxLength":120},
			"color":{"type":"string","maxLength":32}},"additionalProperties":false}`),
		OutputSchema: schemaFor[Tag](),
	}
}

func (t createTag) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	tag, err := t.tags.CreateTag(ctx, args.Name, args.Color)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tag)
}

// --- apply_tag / remove_tag (🟢 write) ---

type applyTag struct{ tags Tags }

func (t applyTag) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "apply_tag", Title: "Apply a tag to a record", Version: toolVersionV1,
		Description:   applyTagCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "applyTag",
		InputSchema: schema(`{"type":"object","required":["tag_id","record_type","record_id"],"properties":{
			"tag_id":{"type":"string","format":"uuid"},
			"record_type":{"type":"string","enum":` + tagTargetEnum + `},
			"record_id":{"type":"string","format":"uuid"}},"additionalProperties":false}`),
		OutputSchema: schemaFor[TagAppliedResult](),
	}
}

func (t applyTag) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := decodeTagging(in)
	if err != nil {
		return nil, err
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
		OpenAPIOp: "removeTag",
		InputSchema: schema(`{"type":"object","required":["tag_id","record_type","record_id"],"properties":{
			"tag_id":{"type":"string","format":"uuid"},
			"record_type":{"type":"string","enum":` + tagTargetEnum + `},
			"record_id":{"type":"string","format":"uuid"}},"additionalProperties":false}`),
		OutputSchema: schemaFor[TagAppliedResult](),
	}
}

func (t removeTag) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := decodeTagging(in)
	if err != nil {
		return nil, err
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
