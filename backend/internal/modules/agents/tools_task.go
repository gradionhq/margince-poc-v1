// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// --- create_task (🟢 write) ---
//
// A task IS an activity of kind `task`, and log_activity can store one — but a
// verb catalog that makes "create a task" a special case of "log something that
// happened" makes every caller that wants a to-do learn the indirection. The
// next-best-action surface is the first such caller; this verb exists so its
// catalog, and an agent's, can say create_task and mean it.

type createTask struct {
	p datasource.SystemOfRecordProvider
}

// taskFields is the verb's own shape. Folded into the activity body the
// provider validates; `kind` is never a caller's to choose here.
type taskFields struct {
	Subject    string          `json:"subject"`
	Body       *string         `json:"body,omitempty"`
	DueAt      *string         `json:"due_at,omitempty"`
	AssigneeID *string         `json:"assignee_id,omitempty"`
	Links      json.RawMessage `json:"links,omitempty"`
	// Source is the REST door's provenance field. The tool stamps its own and
	// never reads this, but the compose decoder folds a REST body through the
	// same function, and a required field must not read as an unknown one.
	Source *string `json:"source,omitempty"`
}

func (t createTask) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "create_task", Title: "Create a task", Version: toolVersionV1,
		Description:   createTaskCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "createTask",
		// Terse on purpose: the listing rides every run under a token ceiling,
		// and log_activity's schema already explains links and timestamps.
		InputSchema: schema(`{"type":"object","required":["subject"],"properties":{
			"subject":{"type":"string"},"body":{"type":"string"},
			"due_at":{"type":"string","format":"date-time"` + timestampNote + `},
			"assignee_id":{"type":"string","format":"uuid","description":"Defaults to the human you act for."},
			"links":{"type":"array","items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":` + activityLinkEntityTypeEnum + `},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false}}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[wireRecord](),
	}
}

func (t createTask) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	fields, err := TaskAsActivity(in)
	if err != nil {
		return nil, err
	}
	ref, err := t.p.Create(ctx, datasource.CreateInput{
		EntityType: datasource.EntityActivity,
		Fields:     fields,
		Source:     ToolSource,
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// TaskAsActivity is the fold: the verb's arguments as the activity body, with
// the kind stamped. Decoded strictly first so an unknown field is refused as
// this verb's rather than surfacing as the provider's complaint about one the
// caller never typed.
func TaskAsActivity(in json.RawMessage) (json.RawMessage, error) {
	var args taskFields
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	subject := strings.TrimSpace(args.Subject)
	if subject == "" {
		return nil, &BadArgsError{Cause: fmt.Errorf("subject is required: a task has to say what is to be done")}
	}
	body := map[string]any{"kind": "task", "subject": subject}
	if args.Body != nil {
		body["body"] = *args.Body
	}
	if args.DueAt != nil {
		body["due_at"] = *args.DueAt
	}
	if args.AssigneeID != nil {
		body["assignee_id"] = *args.AssigneeID
	}
	if len(args.Links) > 0 {
		body["links"] = args.Links
	}
	if args.Source != nil {
		body["source"] = *args.Source
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("create_task: encoding the activity body: %w", err)
	}
	return out, nil
}
