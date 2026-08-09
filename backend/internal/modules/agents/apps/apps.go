// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package apps serves this surface's interactive views: the documents an MCP
// host fetches by `ui://` URI, sandboxes, and renders beside a tool's answer
// (SEP-1865).
//
// WHY IT IS A SUBPACKAGE. modules/agents grows one only when a named trigger
// fires, and two fire here. These are static ASSETS in three other languages —
// HTML, CSS, JavaScript — embedded rather than written in Go, and a
// `go:embed` directive binds a package to a directory layout, so they need a
// directory of their own. And the sweep that holds them to their own security
// claims reads the asset bytes, which is a different kind of test from
// everything else in the parent package.
//
// WHAT A VIEW IS, in this tree's terms: a second RENDERER for an answer a tool
// already gives in text. It owns no data path, holds no credential, and calls
// nothing. Every fact it displays arrived in the tool result the host pushed
// into it, which is why this package has no dependency on a store, a seam, or a
// principal — it composes documents, and the documents are the same for every
// caller.
//
// WHY THE DOCUMENTS ARE SELF-CONTAINED. Each is assembled with its stylesheet
// and its scripts INLINE, and declares an empty origin allowlist. A host builds
// its content-security policy from that declaration and admits nothing the
// declaration does not name, so "this view reaches no network" is a promise kept
// by having no origin to name rather than by an allowlist someone maintains. The
// day one is needed, it arrives in a diff beside the reason for it — and
// appsfitness_test.go fails the build if an asset reaches off-origin without one.
package apps

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// assets holds the view sources. They are embedded rather than read from disk so
// a deployed binary carries its own views: a document fetched from a path at
// runtime is a document that can differ from the one this build was reviewed
// with.
//
//go:embed bridge.js app.css accountbrief.js relationshipmap.js
var assets embed.FS

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

// view is one published document before it is assembled: its identity, and the
// script that renders it.
type view struct {
	uri         string
	name        string
	title       string
	description string
	// script is the per-view renderer, loaded after the shared bridge.
	script string
}

// catalog is every view this surface serves. Adding one is an entry here plus
// its script; nothing else in this package is per-view.
var catalog = []view{
	{
		uri:  AccountBriefURI,
		name: "account_brief_view",
		// A title a human reads in a host's own UI chrome, so it says what the
		// panel shows rather than naming the tool behind it.
		title:       "Morning brief",
		description: "The ranked brief queue, with the factor decomposition each item ranked on.",
		script:      "accountbrief.js",
	},
	{
		uri:         RelationshipMapURI,
		name:        "relationship_map_view",
		title:       "Who knows this contact",
		description: "The colleagues who know a contact, warmest first, with the interactions behind each warmth band.",
		script:      "relationshipmap.js",
	},
}

// Provider publishes the views over the resource seam. It holds the assembled
// documents, built once at construction: a document is the same for every
// caller, so assembling per read would be the same string work on every request
// for no difference in the answer.
type Provider struct {
	published []mcp.Resource
	documents map[string]string
}

// NewProvider assembles every view and returns the provider that serves them.
//
// It returns an error rather than panicking, and the error is worth having:
// assembly reads embedded files, and a rename that misses one is a build that
// compiles and serves a broken document. The caller is composition code, which
// turns this into a refusal to start.
func NewProvider() (*Provider, error) {
	bridge, err := assets.ReadFile("bridge.js")
	if err != nil {
		return nil, fmt.Errorf("crmapps: reading the view bridge: %w", err)
	}
	style, err := assets.ReadFile("app.css")
	if err != nil {
		return nil, fmt.Errorf("crmapps: reading the view stylesheet: %w", err)
	}
	p := &Provider{
		published: make([]mcp.Resource, 0, len(catalog)),
		documents: make(map[string]string, len(catalog)),
	}
	for _, v := range catalog {
		script, err := assets.ReadFile(v.script)
		if err != nil {
			return nil, fmt.Errorf("crmapps: reading %s for %s: %w", v.script, v.uri, err)
		}
		p.published = append(p.published, describe(v))
		p.documents[v.uri] = document(v.title, string(style), string(bridge), string(script))
	}
	return p, nil
}

// MustProvider is NewProvider for a composition root, which has nowhere to put
// an error: the views are a constant of this binary — the same documents for
// every process, every server and every caller — so a failure here is a build
// that shipped with a renamed asset, not a condition an installation can be in.
//
// It exists BESIDE NewProvider rather than replacing it so the assembly stays
// testable: a test asserts the error is nil and reads the documents, where a
// panicking-only constructor could only be exercised by surviving it.
func MustProvider() *Provider {
	p, err := NewProvider()
	if err != nil {
		//craft:ignore panic-in-domain composition-time assembly of this binary's own embedded assets — a failure is a bad build, not a runtime state
		panic("crmapps: " + err.Error())
	}
	return p
}

// describe is one view's published descriptor, including the sandbox policy a
// host builds its content-security policy from.
//
// EVERY allowlist is left empty, and every permission false. That is the whole
// security posture of these views stated in the one place a host reads: no
// fetch, no remote script, no nested frame, no camera, no clipboard. See the
// package comment for why an empty list is the promise rather than a placeholder.
func describe(v view) mcp.Resource {
	return mcp.Resource{
		URI: v.uri, Name: v.name, Title: v.title, Description: v.description,
		MIMEType: mcp.AppMIMEType,
		// A view shows what a read tool answered, so it is a read. The scope
		// filter on the resource surface applies to it exactly as to any other
		// document — a passport with no read grant is not shown these.
		RequiredScope: principal.ScopeRead,
		UI: &mcp.ResourceUI{
			// PrefersBorder, because these render as a panel of rows beside a
			// conversation and read better with an edge than bleeding into it.
			PrefersBorder: true,
		},
	}
}

// document assembles one view: the shared stylesheet and bridge, then the view's
// own renderer, all inline.
//
// The title is the only interpolated value and it comes from the catalog above —
// a constant in this package, never a record, never a caller. That is why this
// composes with a format string rather than reaching for html/template: there is
// no untrusted value here to escape. The untrusted values are the ones the HOST
// pushes in at runtime, and they are handled where they arrive, by a bridge that
// puts text on the page through textContent and never through markup.
func document(title, style, bridge, script string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>` + "\n")
	b.WriteString(`<html lang="en">` + "\n")
	b.WriteString(`<meta charset="utf-8">` + "\n")
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">` + "\n")
	b.WriteString(`<title>` + title + `</title>` + "\n")
	b.WriteString("<style>\n" + style + "\n</style>\n")
	b.WriteString(`<body><main id="root"></main>` + "\n")
	b.WriteString("<script>\n" + bridge + "\n</script>\n")
	b.WriteString("<script>\n" + script + "\n</script>\n")
	return b.String()
}

// Resources publishes the view catalog. It is the same for every caller: a view
// is a document with no data in it, so there is nothing here to narrow. The
// per-caller filter the resource surface applies on top still runs, which is
// what keeps a passport without a read grant from being shown them.
func (p *Provider) Resources(context.Context) []mcp.Resource {
	return p.published
}

// ReadResource answers one assembled document, or the declared not-found for a
// URI this provider does not serve — the same sentinel every other provider
// answers, so the dispatcher's existence-hiding applies unchanged.
func (p *Provider) ReadResource(_ context.Context, uri string) (mcp.ResourceContents, error) {
	text, served := p.documents[uri]
	if !served {
		return mcp.ResourceContents{}, apperrors.ErrNotFound
	}
	return mcp.ResourceContents{URI: uri, MIMEType: mcp.AppMIMEType, Text: text}, nil
}

var _ mcp.ResourceProvider = (*Provider)(nil)
