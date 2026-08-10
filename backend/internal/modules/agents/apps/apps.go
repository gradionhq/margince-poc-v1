// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package apps serves this surface's interactive views: the documents an MCP
// host fetches by `ui://` URI, sandboxes, and renders beside a tool's answer
// (SEP-1865).
//
// WHY IT IS A SUBPACKAGE. modules/agents grows one only when a named trigger
// fires, and two fire here. This package owns an outbound HTTP client, an
// admission check and an availability state machine — a concern of its own, with
// its own failure modes — and it embeds the shared admission vocabulary, which a
// `go:embed` directive binds to a directory layout.
//
// WHAT A VIEW IS, in this tree's terms: a second RENDERER for an answer a tool
// already gives in text. It owns no data path, holds no credential, and calls
// nothing. Every fact it displays arrived in the tool result the host pushed
// into it, which is why this package has no dependency on a store, a seam, or a
// principal — it composes documents, and the documents are the same for every
// caller.
//
// WHY THE DOCUMENTS ARE SELF-CONTAINED. Each is built with its stylesheet and
// its scripts INLINE, and declares an empty origin allowlist. A host builds its
// content-security policy from that declaration and admits nothing the
// declaration does not name, so "this view reaches no network" is a promise kept
// by having no origin to name rather than by an allowlist someone maintains.
//
// WHERE THE DOCUMENTS COME FROM. They are authored in frontend/src/mcp-apps and
// served by the web tier as one fully-inlined file each; this package FETCHES
// them over HTTP (fetch.go), admits them (validate.go) and holds them (hold.go).
// Nothing here is embedded, which is why availability is a runtime state this
// package has to model rather than a property of the binary.
package apps

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The URIs the tools name. They are exported because a tool's declaration and
// the document that answers it are two halves of one promise, and the only way
// they cannot drift is for both to read the same constant — the composed-surface
// sweep proves every named URI is published, but a shared constant means there
// is nothing for it to catch.
const (
	// AccountBriefURI renders read_brief's queue.
	AccountBriefURI = "ui://margince/account-brief.html"
	// RelationshipMapURI renders who_knows's colleagues.
	RelationshipMapURI = "ui://margince/relationship-map.html"
)

// view is one published document's identity. The document itself is not here:
// it is fetched, admitted and held at run time, so this is the half that is a
// constant of the build.
type view struct {
	uri         string
	name        string
	title       string
	description string
}

// catalog bounds what this provider may ever publish. An entry is advertised
// only once its document has been fetched and admitted, and the document's URL
// is derived from the URI rather than listed beside it.
var catalog = []view{
	{
		uri:  AccountBriefURI,
		name: "account_brief_view",
		// A title a human reads in a host's own UI chrome, so it says what the
		// panel shows rather than naming the tool behind it.
		title:       "Morning brief",
		description: "The ranked brief queue, with the factor decomposition each item ranked on.",
	},
	{
		uri:         RelationshipMapURI,
		name:        "relationship_map_view",
		title:       "Who knows this contact",
		description: "The colleagues who know a contact, warmest first, with the interactions behind each warmth band.",
	},
}

// sandbox is the policy every view here declares, and the ONE place it is
// stated.
//
// EVERY allowlist is left empty, and every permission unasked. That is the whole
// security posture of these views: no fetch, no remote script, no nested frame,
// no camera, no clipboard. See the package comment for why an empty list is the
// promise rather than a placeholder.
//
// It is one function because the policy reaches a host TWICE — on the catalogue
// and on the read — and those are the two answers that must not be allowed to
// differ. A second literal would be a second chance to widen one of them.
func sandbox() *mcp.ResourceUI {
	return &mcp.ResourceUI{
		// PrefersBorder, because these render as a panel of rows beside a
		// conversation and read better with an edge than bleeding into it.
		PrefersBorder: true,
	}
}

// describe is one view's published descriptor, including the sandbox policy a
// host builds its content-security policy from.
func describe(v view) mcp.Resource {
	return mcp.Resource{
		URI: v.uri, Name: v.name, Title: v.title, Description: v.description,
		MIMEType: mcp.AppMIMEType,
		// A view shows what a read tool answered, so it is a read. The scope
		// filter on the resource surface applies to it exactly as to any other
		// document — a passport with no read grant is not shown these.
		RequiredScope: principal.ScopeRead,
		UI:            sandbox(),
	}
}

// Resources publishes the views this server is CURRENTLY HOLDING. It is the same
// for every caller: a view is a document with no data in it, so there is nothing
// here to narrow. The per-caller filter the resource surface applies on top still
// runs, which is what keeps a passport without a read grant from being shown them.
//
// A view that was never admitted is absent rather than advertised-and-broken: a
// host is entitled to prefetch what it is told about, so naming a document this
// deployment cannot serve is worse than naming none.
func (p *Provider) Resources(context.Context) []mcp.Resource {
	// Built per call, and the policy built with it. The provider is a
	// process-lifetime value shared by every caller, so handing out a retained
	// slice would let one caller's mutation change what every later host is told
	// — including the sandbox policy, which is the one security decision here.
	// ONE load, and the whole answer derived from it. Asking per catalog entry
	// would read the pointer once per view, so a refresh landing between two
	// iterations could compose a listing out of two snapshots — advertising a
	// pair no single immutable set ever contained, which is the exact property
	// the snapshot exists to provide.
	held := *p.held.Load()
	out := make([]mcp.Resource, 0, len(held))
	for _, v := range catalog {
		if _, serving := held[v.uri]; serving {
			out = append(out, describe(v))
		}
	}
	return out
}

// ReadResource answers one held document, or the declared not-found for a URI
// this provider is not serving — the same sentinel every other provider answers,
// so the dispatcher's existence-hiding applies unchanged. A view that failed to
// arrive and a URI that was never published are answered identically, which is
// the correct amount for a caller to learn.
func (p *Provider) ReadResource(_ context.Context, uri string) (mcp.ResourceContents, error) {
	text, served := p.served(uri)
	if !served {
		return mcp.ResourceContents{}, apperrors.ErrNotFound
	}
	// The policy travels WITH the bytes, from the same function the catalogue
	// entry took it from — so a host that read this document without listing
	// first sandboxes it under exactly the rules it would have been told.
	return mcp.ResourceContents{URI: uri, MIMEType: mcp.AppMIMEType, Text: text, UI: sandbox()}, nil
}

var _ mcp.ResourceProvider = (*Provider)(nil)
