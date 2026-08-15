// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The transport directory as an agent tool (ADR-0107/A158).
//
// It exists because the model is asked for a value this module cannot enumerate
// for it: `log_activity` requires a `channel_provider` when the kind is
// `message`, and which providers exist is a DEPLOYMENT fact, so no generated
// enum can carry it into the tool schema. Without this the schema could only
// describe the rule in prose and leave the model guessing at the vocabulary —
// and a guessed provider fails a foreign key it has no way to anticipate.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ChannelProviderDirectory answers which transports this installation
// registered. The composition root implements it — the composed set is its
// fact, not a module's, and this module may not enumerate connectors.
type ChannelProviderDirectory interface {
	ChannelProviders(ctx context.Context) ([]ChannelProviderEntry, error)
}

// ChannelProviderEntry is one transport as the tool reports it. It mirrors the
// contract's shape rather than re-deciding it: a tool that answered a different
// set of fields than `GET /v1/channel-providers` would make the two surfaces
// disagree about the same question.
type ChannelProviderEntry struct {
	Provider          string `json:"provider"`
	Label             string `json:"label"`
	CredentialModel   string `json:"credential_model"`
	SuppliesTransport bool   `json:"supplies_transport"`
}

// ChannelProviderResult is the tool's output shape.
type ChannelProviderResult struct {
	Providers []ChannelProviderEntry `json:"providers"`
}

// RegisterChannelProviderTools registers the directory tool when the surface
// composed a directory to read. Nil means a role that wired none, and the tool
// is then absent from tools/list rather than present and failing — the same
// fail-closed shape every other optional registration here takes.
func RegisterChannelProviderTools(r *Registry, directory ChannelProviderDirectory) {
	if directory == nil {
		return
	}
	r.Register(listChannelProviders{directory: directory})
}

var listChannelProvidersCopy = toolCopy{
	Purpose: "Find out which messaging transports exist in THIS installation, and what each is called.",
	Limits: "It reports what the installation composed, not what this workspace has connected. " +
		"supplies_transport=false means the transport cannot carry an outbound message at all, so a reply on it " +
		"will be refused however the conversation was captured.",
	Instead: "To read the messages themselves, use search_records on activities and filter by channel_provider.",
	Retain: "Carry the `provider` value verbatim: log_activity requires it as channel_provider whenever kind is " +
		"\"message\", and a value not in this list fails a foreign key. Use `label` only for display.",
}

type listChannelProviders struct {
	directory ChannelProviderDirectory
}

func (t listChannelProviders) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "list_channel_providers", Title: "List messaging transports", Version: toolVersionV1,
		Description:   listChannelProvidersCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "listChannelProviders",
		InputSchema:  schema(`{"type":"object","properties":{},"additionalProperties":false}`),
		OutputSchema: schemaFor[ChannelProviderResult](),
	}
}

func (t listChannelProviders) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	entries, err := t.directory.ChannelProviders(ctx)
	if err != nil {
		return nil, err
	}
	// No evidence note: a provider id and a display label are deployment facts,
	// not record content, so an answer built from them is not tainted by data
	// the call never read.
	return json.Marshal(ChannelProviderResult{Providers: entries})
}
