// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mcp

// The resource half of the governed surface: read-only documents a client
// fetches to learn what it may ask, rather than probing the tool surface for
// it. A resource is NOT a tool — it takes no arguments, changes nothing, and
// carries no autonomy tier. What it does carry is the same per-caller
// admission the tools ride: a provider composes its document from what THIS
// principal may read, so a resource can never become a discovery channel for
// what the caller is denied.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Resource is one published document, as resources/list advertises it.
type Resource struct {
	// URI is the stable identity a client reads by; it is what
	// resources/read echoes back.
	URI string
	// Name is the programmatic identifier; Title the human-readable display
	// name a client shows in its place (the protocol's precedence is
	// title > name, mirroring ToolSpec).
	Name        string
	Title       string
	Description string
	MIMEType    string
	// RequiredScope is the passport scope an AGENT must hold to see or read
	// this document, mirroring ToolSpec.RequiredScope. It is not optional
	// governance: the query vocabulary names a workspace's own custom-field
	// columns, so a passport with no read grant learning them would make the
	// resource surface the discovery channel the scope-filtered tool list is
	// careful not to be. A human's authority is their RBAC, not a scope, so
	// the filter applies to agents alone.
	RequiredScope principal.Scope
	// UI is non-nil on an interactive view, and nil on every ordinary
	// document. What it declares is what the host's sandbox will admit, so it
	// travels with the document rather than with the tool that names it. See
	// ResourceUI.
	UI *ResourceUI
}

// ResourceContents is one resources/read result. Text only: every document
// this surface publishes is a UTF-8 payload (JSON today), and a binary
// member would be a shape no provider here can fill.
type ResourceContents struct {
	URI      string
	MIMEType string
	Text     string
}

// ResourceProvider publishes documents beside the tool surface. Both methods
// take the bound caller's context because both answers are per-caller:
// Resources lists only what this principal may read, and ReadResource
// composes the document from the same narrowed view.
//
// An unknown (or caller-invisible) URI answers apperrors.ErrNotFound — the
// two are deliberately indistinguishable, exactly as a row-scope miss is on
// the record surface.
type ResourceProvider interface {
	Resources(ctx context.Context) []Resource
	ReadResource(ctx context.Context, uri string) (ResourceContents, error)
}
