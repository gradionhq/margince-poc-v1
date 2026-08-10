// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Two rules about what a tool may SAY about a document, both learned the
// expensive way and both derived from the surface rather than maintained as a
// list.
//
// A tool that names a document has made a promise on the server's behalf: that
// the document exists, and that reading it is worth a caller's round trip. The
// gates below hold each half. Neither is a style rule — the first closes a dead
// end a caller cannot get out of, and the second was measured on the
// certification band before it was written down.

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// schemaURIPattern finds every margince:// document a tool's text names.
//
// It matches the TEXT a caller reads rather than a constant a test could import,
// because the failure it exists to catch is a URI that was typed: a description
// naming margince://schema/record_fields with an underscore points at nothing,
// and only a reader of the string can tell.
var schemaURIPattern = regexp.MustCompile(`margince://[a-z0-9/_-]+`)

// namedDocuments reports every margince:// URI the tool surface points a caller
// at, mapped to the tools that name it.
func namedDocuments(specs []mcp.ToolSpec) map[string][]string {
	named := map[string][]string{}
	for _, spec := range specs {
		// Both halves of what a client reads: the prose and the argument
		// descriptions spliced into the schema, since a pointer lives in either.
		text := spec.Description + " " + string(spec.InputSchema)
		for _, uri := range schemaURIPattern.FindAllString(text, -1) {
			if !alreadyNamed(named[uri], spec.Name) {
				named[uri] = append(named[uri], spec.Name)
			}
		}
	}
	return named
}

func alreadyNamed(names []string, name string) bool {
	for _, existing := range names {
		if existing == name {
			return true
		}
	}
	return false
}

// publishedDocumentURIs is what the composed resource surface actually serves to
// a caller holding every scope.
func publishedDocumentURIs(t *testing.T) map[string]bool {
	t.Helper()
	provider := composeResources(
		mcpResourceProviders(search.NewQuerySchemaResource(queryVocabulary(nil)), nil)...)
	if provider == nil {
		t.Fatal("the composition published no resource provider, so no tool could name a document")
	}
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:pointer-gate", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeDraft,
			principal.ScopeWrite, principal.ScopeSend, principal.ScopeEnrich),
	})
	published := map[string]bool{}
	for _, doc := range provider.Resources(ctx) {
		published[doc.URI] = true
	}
	return published
}

// A tool that names a document this deployment does not serve has sent its
// caller somewhere there is nothing — the dead end list_pipelines was created to
// close, one surface along. It is worse than saying nothing, because a caller
// that reads the pointer spends a call proving it wrong.
func TestEveryDocumentAToolNamesIsOneTheSurfacePublishes(t *testing.T) {
	published := publishedDocumentURIs(t)
	for uri, namedBy := range namedDocuments(servedSurface(t).Specs()) {
		if !published[uri] {
			t.Errorf("%s names %s, which the resource surface does not publish — a caller following "+
				"that pointer finds nothing. Publish it, or stop naming it.",
				strings.Join(namedBy, ", "), uri)
		}
	}
}

// The rule only means something if some tool exercises it. A surface where no
// tool names any document would pass the gate above while proving nothing, and
// this change is precisely the one that made pointers load-bearing.
func TestTheToolSurfaceActuallyNamesADocument(t *testing.T) {
	if len(namedDocuments(servedSurface(t).Specs())) == 0 {
		t.Error("no tool names a document, so the pointer gate is watching an empty set")
	}
}

// readingImperatives are the shapes that turn a POINTER into an ORDER.
//
// The distinction is measured, not editorial. Phrased as an instruction — "read
// this before your first write" — a 14B binding obeyed it on goals with no write
// in them at all, and its first-step accuracy on the agent_loop band fell by a
// fifth. Phrased as a statement of where the vocabulary lives, the same binding
// read it once instead of thirteen times. A description names what a document
// HOLDS; the frame is the only place that may say when to go and get it.
var readingImperatives = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bread (it|this|that)\b`),
	regexp.MustCompile(`(?i)\bread margince://`),
	regexp.MustCompile(`(?i)\bread the [a-z ]*(document|resource|vocabulary|schema)\b`),
	regexp.MustCompile(`(?i)\bbefore (your|you) (first )?(write|call)\b`),
}

func readingImperative(text string) string {
	for _, pattern := range readingImperatives {
		if found := pattern.FindString(text); found != "" {
			return found
		}
	}
	return ""
}

func TestNoToolOrdersTheModelToReadADocument(t *testing.T) {
	for _, spec := range servedSurface(t).Specs() {
		text := spec.Description + " " + string(spec.InputSchema)
		if found := readingImperative(text); found != "" {
			t.Errorf("%s tells the model to %q. A description says what a document HOLDS; ordering a "+
				"read costs a turn on every goal, including the ones with nothing to read for.",
				spec.Name, found)
		}
	}
}

// A published document's own advertisement rides the same prompts, so it is held
// to the same rule — and it is the sentence that did the damage.
func TestNoPublishedDocumentOrdersItsOwnReading(t *testing.T) {
	provider := composeResources(
		mcpResourceProviders(search.NewQuerySchemaResource(queryVocabulary(nil)), nil)...)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:pointer-gate", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
	})
	for _, doc := range provider.Resources(ctx) {
		if found := readingImperative(doc.Description); found != "" {
			t.Errorf("%s advertises itself with %q. Its description is read by every client that lists "+
				"resources, and an instruction there is one a model acts on.", doc.URI, found)
		}
	}
}

// Both rules proved against text that BREAKS them, so a pattern that silently
// stopped matching fails here rather than passing over a clean tree forever.
func TestThePointerRulesFailOnTheTextTheyDescribe(t *testing.T) {
	for _, ordered := range []string{
		"Read margince://schema/record-fields for the fields each record_type takes.",
		"The shapes are published; read it before your first write of a record type.",
		"Read the document for the vocabulary.",
	} {
		if readingImperative(ordered) == "" {
			t.Errorf("an instruction to read was not reported: %q", ordered)
		}
	}
	stated := "The fields each record_type takes are published at margince://schema/record-fields — " +
		"that document, not this description, is what says what a write may name."
	if found := readingImperative(stated); found != "" {
		t.Errorf("a statement of where a vocabulary lives was reported as an order: %q", found)
	}
	if uris := schemaURIPattern.FindAllString(stated, -1); len(uris) != 1 {
		t.Errorf("the uri pattern found %v in text naming exactly one document", uris)
	}
}
