// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The resource fan-out, exercised as a fan-out.
//
// The App sweeps beside this file compose ONE provider — the views, with a nil
// vocabulary — because that is the wiring they are about. Which means they never
// reach the composite at all, and the code that decides which of two providers
// answers a URI would have gone in with no test over it. That is the shape a
// coverage read is for.
//
// The two providers here are stubs at a true seam (mcp.ResourceProvider), which
// is what a stub is for: the behaviour under test is the composition's, and a
// real provider would only make the failures harder to read.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// stubProvider serves a fixed set of documents and answers the declared
// not-found for anything else — or a fixed failure, when a test is about what a
// failing provider does to the walk.
type stubProvider struct {
	documents map[string]string
	failure   error
}

func (s stubProvider) Resources(context.Context) []mcp.Resource {
	out := make([]mcp.Resource, 0, len(s.documents))
	for uri := range s.documents {
		out = append(out, mcp.Resource{URI: uri, Name: uri, Title: uri, MIMEType: "application/json"})
	}
	return out
}

func (s stubProvider) ReadResource(_ context.Context, uri string) (mcp.ResourceContents, error) {
	if s.failure != nil {
		return mcp.ResourceContents{}, s.failure
	}
	text, served := s.documents[uri]
	if !served {
		return mcp.ResourceContents{}, apperrors.ErrNotFound
	}
	return mcp.ResourceContents{URI: uri, MIMEType: "application/json", Text: text}, nil
}

// Composing nothing yields nothing, so the transport takes its existing
// no-provider path rather than an empty composite that behaves identically and
// has to be reasoned about separately.
func TestComposingNoResourceProviderYieldsNone(t *testing.T) {
	if composed := composeResources(); composed != nil {
		t.Errorf("composing nothing returned %T, want nil", composed)
	}
	if composed := composeResources(nil, nil); composed != nil {
		t.Errorf("composing only absent providers returned %T, want nil", composed)
	}
}

// One provider is returned as itself. A fan-out that is only ever in the graph
// when it is fanning keeps a stack trace honest about what sits between the
// transport and the document.
func TestComposingOneResourceProviderReturnsItUnwrapped(t *testing.T) {
	only := stubProvider{documents: map[string]string{"margince://a": "{}"}}
	composed := composeResources(nil, only, nil)
	// Asserted by TYPE rather than by equality: a provider holding a map is not
	// comparable, and the question is whether a fan-out was interposed at all.
	if _, wrapped := composed.(resourceFanout); wrapped {
		t.Error("composing one provider interposed a fan-out; it should be returned as itself")
	}
	if _, err := composed.ReadResource(context.Background(), "margince://a"); err != nil {
		t.Errorf("the single composed provider does not serve its own document: %v", err)
	}
}

// Every provider's catalogue reaches the client. A fan-out that served only the
// first would hide a whole provider's documents with nothing reporting it.
func TestTheFanoutPublishesEveryProvidersCatalogue(t *testing.T) {
	composed := composeResources(
		stubProvider{documents: map[string]string{"margince://schema/query": "{}"}},
		stubProvider{documents: map[string]string{"ui://margince/a.html": "<!doctype html>"}},
	)
	published := map[string]bool{}
	for _, r := range composed.Resources(context.Background()) {
		published[r.URI] = true
	}
	for _, want := range []string{"margince://schema/query", "ui://margince/a.html"} {
		if !published[want] {
			t.Errorf("the fan-out does not publish %s, so a whole provider's documents are invisible", want)
		}
	}
}

// A read walks past a provider that does not serve the URI and reaches the one
// that does. Without this the second provider's documents would be advertised
// and unreadable — the failure the App sweep's own read-back check exists for,
// arriving one layer lower.
func TestAReadWalksPastAProviderThatDoesNotServeTheURI(t *testing.T) {
	composed := composeResources(
		stubProvider{documents: map[string]string{"margince://schema/query": `{"vocabulary":true}`}},
		stubProvider{documents: map[string]string{"ui://margince/a.html": "<!doctype html>"}},
	)
	for _, tc := range []struct{ uri, want string }{
		{"margince://schema/query", `{"vocabulary":true}`},
		{"ui://margince/a.html", "<!doctype html>"},
	} {
		contents, err := composed.ReadResource(context.Background(), tc.uri)
		if err != nil {
			t.Errorf("reading %s through the fan-out: %v", tc.uri, err)
			continue
		}
		if contents.Text != tc.want {
			t.Errorf("reading %s answered %q, want %q", tc.uri, contents.Text, tc.want)
		}
	}
}

// A URI nobody serves answers the DECLARED not-found, so the dispatcher's
// existence-hiding is unchanged by composition: a caller learns the same thing
// about a document that does not exist and one they may not see.
func TestAURINoProviderServesAnswersTheNotFoundSentinel(t *testing.T) {
	composed := composeResources(
		stubProvider{documents: map[string]string{"margince://a": "{}"}},
		stubProvider{documents: map[string]string{"margince://b": "{}"}},
	)
	_, err := composed.ReadResource(context.Background(), "margince://nope")
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("an unserved URI answered %v, want the declared not-found — anything else surfaces as a 500 "+
			"and tells the caller something is there", err)
	}
}

// A provider that FAILS stops the walk, and its failure is not laundered into a
// not-found.
//
// This is the assertion that matters most in this file. A pool fault while
// reading the vocabulary must not degrade into "no such document": the caller
// would be told a document does not exist on the strength of a failure to look,
// and the next provider's answer — or the absence of one — would stand in for a
// question nobody managed to ask.
func TestAFailingProviderStopsTheWalkRatherThanBecomingANotFound(t *testing.T) {
	poolFault := errors.New("connection refused")
	composed := composeResources(
		stubProvider{failure: poolFault},
		// This provider WOULD serve the URI. It must not be reached, or the
		// answer would depend on a failure that was silently stepped over.
		stubProvider{documents: map[string]string{"margince://a": "{}"}},
	)
	_, err := composed.ReadResource(context.Background(), "margince://a")
	if err == nil {
		t.Fatal("a failing provider was stepped over and a later one answered, so a read succeeded on the " +
			"strength of a failure nobody saw")
	}
	if errors.Is(err, apperrors.ErrNotFound) {
		t.Error("a provider failure was laundered into a not-found, which tells the caller the document does not " +
			"exist when the truth is that nothing could look")
	}
	if !errors.Is(err, poolFault) {
		t.Errorf("the fan-out answered %v, which does not carry the underlying failure", err)
	}
}

// The composed view provider is what a hosted request actually reaches. Its
// assembly panics on a bad build, so reaching this assertion at all is half of
// it; the other half is that it publishes something, because an empty provider
// panics nowhere and offers no host a view.
func TestTheViewProviderAssemblesForThisBuild(t *testing.T) {
	if len(appViews.Resources(context.Background())) == 0 {
		t.Error("the composed view provider publishes nothing, so no host is ever offered a view")
	}
}
