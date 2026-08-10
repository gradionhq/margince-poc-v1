// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// docs/reference/mcp-info.json is the served MCP surface, as a client receives
// it — the two catalogs a client fetches before it may do anything, captured
// from the real hosted handler rather than re-rendered from the registry.
//
// It exists because the surface's SIZE and its CONTENT are both review
// questions and neither was visible in a diff. A tool's description is prose in
// a Go file; what a client actually holds is that prose plus a JSON schema plus
// the governance clause the transport appends, and the difference between those
// two things is where a catalog quietly doubles. Publishing the served bytes
// means a change that adds 4 KB to every client's session shows up as 4 KB in
// the pull request that adds it.
//
// It is the ALL-SCOPE listing, deliberately, and the file says so. tools/list
// is scope-filtered per caller, so there is no single "the" catalog — a
// read-scoped passport is served a much shorter one. The complete surface is
// the only rendering that is stable to review against and the only one that is
// the worst case, which is the same reason the listing budget is measured
// against it.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// mcpInfoDoc is the published artifact. docs/ belongs to the repository, one
// level above the Go module that renders it.
var mcpInfoDoc = filepath.Join("..", "..", "..", "docs", "reference", "mcp-info.json")

// updateMCPInfo rewrites BOTH artifacts instead of comparing against them. One
// flag, so a regeneration never leaves the page describing a surface the
// payload beside it no longer carries.
var updateMCPInfo = flag.Bool("update-mcp-info", false,
	"rewrite docs/reference/mcp-info.{json,md} from the served surface")

// mcpInfoNote travels IN the document, because a reader who opens a 90 KB JSON
// file will not come looking for a Go comment to find out whose view it is.
const mcpInfoNote = "Generated from the served MCP surface by " +
	"`go test ./internal/compose/ -run TestPublishedMCPSurface -update-mcp-info`; do not edit by hand. " +
	"This is the ALL-SCOPE view: tools/list and resources/list are both filtered per caller, so a " +
	"passport holding fewer scopes is served less than this. It is the CORE catalog: extension units " +
	"register onto the same registry and are not composed here. It is captured as an Apps-capable " +
	"host sees it, so a tool bound to a view carries `_meta.ui.resourceUri`; a client that does not " +
	"declare the UI extension is served no such member. The `ui://` view descriptors ARE " +
	"included, and a deployment publishes each only once its boot has fetched and admitted that " +
	"document, so an api serving neither advertises neither."

// mcpInfo is the published shape: the two catalogs, and the sizes that make the
// document reviewable at a glance rather than by diffing 90 KB of schema.
type mcpInfo struct {
	Note      string          `json:"note"`
	Scopes    []string        `json:"scopes"`
	Totals    mcpInfoTotals   `json:"totals"`
	Tools     json.RawMessage `json:"tools"`
	Resources json.RawMessage `json:"resources"`
	// Documents is each published resource's CONTENT, keyed by uri — the half a
	// descriptor does not carry and the half this surface's whole argument rests
	// on. A catalog that names a vocabulary and never shows it cannot be
	// reviewed: the question a reader has is whether the document a tool defers
	// to actually answers what the tool stopped saying.
	//
	// A `ui://` view is absent, and its absence is recorded rather than silent.
	// Its bytes are a per-deployment artifact fetched from a web origin at boot,
	// so whatever this test could put here would be a stand-in — and a stand-in
	// published as the served document is the one thing this artifact must never
	// contain.
	Documents map[string]string `json:"documents"`
}

// mcpInfoTotals is the arithmetic a reviewer would otherwise do by hand.
//
// These are the WIRE bytes, and they are deliberately larger than the Surface-B
// listing the budget gate holds: this payload carries each tool's OUTPUT schema
// and the governance clause the transport appends, and the runner's listing
// renders name, description and input schema alone. The two answer different
// questions — what a client downloads, and what a run re-sends every step — and
// reading either number as the other is the mistake this comment exists to
// prevent. The ~4-bytes rule is the same one the window estimates with, so the
// figure is an estimate of the same KIND, not of the same thing.
type mcpInfoTotals struct {
	Tools          int    `json:"tools"`
	Resources      int    `json:"resources"`
	ToolBytes      int    `json:"tool_catalog_bytes"`
	ResourceBytes  int    `json:"resource_catalog_bytes"`
	ApproxTokens   int    `json:"approx_wire_tokens_total"`
	LargestToolB   int    `json:"largest_tool_bytes"`
	LargestToolNam string `json:"largest_tool"`
}

func TestPublishedMCPSurfaceMatchesWhatAClientIsServed(t *testing.T) {
	// The views are primed through the real fetch-and-admit path against a
	// stand-in origin, the same way every other view test reaches them: a
	// hand-built provider holding hand-inserted documents would publish
	// descriptors no deployment path produced. Only the DESCRIPTORS reach this
	// artifact — resources/list carries identity, mime type and sandbox policy,
	// never the document — and every one of those is a constant of the build, so
	// the stand-in's bytes are nowhere in the published page.
	views := primedViews(t, bothViews())
	tools := mcpResult(t, "tools/list", views)
	resources := mcpResult(t, "resources/list", views)
	doc := mcpInfoDocument(t, tools, resources)
	doc.Documents = publishedDocumentBodies(t, resources, views)
	rendered, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("rendering the served surface: %v", err)
	}
	syncMCPInfo(t, mcpInfoDoc, append(rendered, '\n'))
	// The page is rendered FROM the document just published, not from a second
	// walk of the registry — a page that built its own view could disagree with
	// the payload it claims to describe. Both are synced in one pass, so neither
	// can be committed without the other.
	syncMCPInfo(t, mcpInfoPage, renderMCPInfoMarkdown(t, doc))
}

func mcpInfoDocument(t *testing.T, tools, resources json.RawMessage) mcpInfo {
	t.Helper()
	name, size := largestTool(t, tools)
	return mcpInfo{
		Note:   mcpInfoNote,
		Scopes: mcpInfoScopeNames(),
		Totals: mcpInfoTotals{
			Tools:          countMembers(t, tools, "tools"),
			Resources:      countMembers(t, resources, "resources"),
			ToolBytes:      len(tools),
			ResourceBytes:  len(resources),
			ApproxTokens:   (len(tools) + len(resources)) / 4,
			LargestToolB:   size,
			LargestToolNam: name,
		},
		Tools:     tools,
		Resources: resources,
	}
}

// publishedDocumentBodies reads every advertised document that is a constant of
// the build, through the same resources/read a client calls.
//
// Read rather than rendered: a document this artifact composed itself would be
// this test's idea of the vocabulary, and the point of publishing it is that it
// is the one the server serves.
func publishedDocumentBodies(t *testing.T, resources json.RawMessage, views *apps.Provider) map[string]string {
	t.Helper()
	var catalog struct {
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resources, &catalog); err != nil {
		t.Fatalf("reading the resource catalog: %v", err)
	}
	bodies := make(map[string]string, len(catalog.Resources))
	for _, entry := range catalog.Resources {
		if strings.HasPrefix(entry.URI, "ui://") {
			bodies[entry.URI] = uiDocumentNotPublished
			continue
		}
		bodies[entry.URI] = readPublishedDocument(t, entry.URI, views)
	}
	return bodies
}

// uiDocumentNotPublished stands where a view's HTML would be, and says why
// rather than leaving a reader to wonder whether the view is empty.
const uiDocumentNotPublished = "(not published here: a ui:// document is fetched from a web origin at boot, " +
	"so its bytes are a property of the deployment rather than of this build)"

// readPublishedDocument fetches one document over resources/read.
func readPublishedDocument(t *testing.T, uri string, views *apps.Provider) string {
	t.Helper()
	// resources/read mirrors its uri in Mcp-Name: the header lets an intermediary
	// route on the document being read, and the server compares the two rather
	// than trusting either alone.
	result := mcpCall(t, "resources/read", `,"uri":`+strconv.Quote(uri), uri, views)
	var decoded struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("reading %s: %v", uri, err)
	}
	if len(decoded.Contents) != 1 {
		t.Fatalf("%s answered %d content blocks, want exactly one", uri, len(decoded.Contents))
	}
	return decoded.Contents[0].Text
}

// mcpInfoScopes is the whole passport vocabulary, so the artifact is the
// complete catalog. Ordered, because the document is compared byte for byte.
func mcpInfoScopes() []principal.Scope {
	return []principal.Scope{
		principal.ScopeRead, principal.ScopeDraft,
		principal.ScopeWrite, principal.ScopeSend, principal.ScopeEnrich,
	}
}

func mcpInfoScopeNames() []string {
	names := make([]string, 0, len(mcpInfoScopes()))
	for _, scope := range mcpInfoScopes() {
		names = append(names, string(scope))
	}
	return names
}

// appsCapableMeta declares the modern framing and the Apps extension, as a host
// that can render a `ui://` document does.
//
// It is not decoration. A tool associates itself with its view through
// `_meta.ui.resourceUri` on the TOOL — the App extension puts the association on
// the declaration, not on the result — and this server serves that member ONLY
// to a request that declared it can render one. Captured without the
// declaration, the artifact showed no tool bound to any view, which is true of
// a plain client and false of the surface.
// modernProtocolVersion is the revision this artifact is captured under. It is
// spelled here rather than reached for across the module boundary because the
// artifact records the surface AS OF a revision — a capture that silently
// followed the server to a new one would change the page without saying why.
const modernProtocolVersion = "2026-07-28"

const appsCapableMeta = `{` +
	`"io.modelcontextprotocol/protocolVersion":"` + modernProtocolVersion + `",` +
	`"io.modelcontextprotocol/clientCapabilities":{"extensions":{` +
	`"io.modelcontextprotocol/ui":{"mimeTypes":["text/html;profile=mcp-app"]}}}}`

// mcpResult calls one JSON-RPC method against the real hosted handler and
// returns its `result` member.
//
// Over a real server and a real request rather than off the registry helpers,
// because that is the whole point of the artifact: comparing two callers of one
// function proves they agree with each other and nothing about what reaches the
// wire. The transport appends the governance clause to every description and
// applies the scope filter, and neither is visible from Specs().
func mcpResult(t *testing.T, method string, views *apps.Provider) json.RawMessage {
	t.Helper()
	return mcpCall(t, method, "", "", views)
}

// mcpCall issues one request, with any extra params the method needs spliced
// beside the negotiated `_meta`.
func mcpCall(t *testing.T, method, extraParams, name string, views *apps.Provider) json.RawMessage {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:published-surface", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(mcpInfoScopes()...),
	})
	handler := agents.NewHTTPHandler(NewRegistry(nil, SendPath{}),
		func(*http.Request) (context.Context, error) { return ctx, nil },
		nil, "margince-crm", "published", slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The composed provider exactly as mcpedge wires it. The scope filter is
		// the dispatcher's own, applied behind this door, so what the artifact
		// records is the narrowed catalogue a caller is really served.
		agents.WithResourceProvider(composeResources(
			mcpResourceProviders(search.NewQuerySchemaResource(queryVocabulary(nil)), views)...)),
		// A tool names its view only where the deployment HOLDS that document,
		// which is the same promise the protocol makes: a `_meta.ui.resourceUri`
		// pointing at a document the server will not serve is a dangling
		// reference. Without this the artifact showed no tool bound to any view
		// — true of an api serving none, and false of one serving both.
		agents.WithHeldViews(views.Holds))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{"_meta":`+appsCapableMeta+extraParams+`}}`))
	if err != nil {
		t.Fatalf("building the %s request: %v", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The server refuses a modern body whose header does not agree with it, and
	// says so: the body is what it executes, so a disagreeing header is a client
	// that thinks it negotiated something else.
	req.Header.Set("MCP-Protocol-Version", modernProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	// A plain JSON answer, not the SSE framing the handler also offers: the
	// artifact is the payload, and the stream would wrap it.
	req.Header.Set("Accept", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("calling %s: %v", method, err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing the %s response: %v", method, err)
		}
	}()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the %s response: %v", method, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d: %s", method, res.StatusCode, body)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decoding %s: %v\n%s", method, err, body)
	}
	if envelope.Error != nil {
		t.Fatalf("%s answered an error: %s", method, envelope.Error)
	}
	// Re-marshal through a generic value so the artifact's key order is JSON's
	// own and not the handler's struct order — the file is compared byte for
	// byte, and a field reordered in Go would otherwise read as a changed
	// surface.
	var canonical any
	if err := json.Unmarshal(envelope.Result, &canonical); err != nil {
		t.Fatalf("canonicalising %s: %v", method, err)
	}
	stable, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("re-encoding %s: %v", method, err)
	}
	return stable
}

func countMembers(t *testing.T, result json.RawMessage, member string) int {
	t.Helper()
	// Decoded a member at a time: the modern framing puts its own `_meta` object
	// beside the list, so a map of LISTS would fail to decode the envelope this
	// artifact deliberately captures.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatalf("counting %s: %v", member, err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(envelope[member], &entries); err != nil {
		t.Fatalf("counting %s: %v", member, err)
	}
	return len(entries)
}

// largestTool names the entry a reader should look at first when the catalog
// grows. One tool carrying a per-type vocabulary is how this surface reached
// its budget once already, and a total alone does not say which one.
func largestTool(t *testing.T, result json.RawMessage) (string, int) {
	t.Helper()
	var decoded struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("reading the tool catalog: %v", err)
	}
	var raw struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &raw); err != nil {
		t.Fatalf("sizing the tool catalog: %v", err)
	}
	name, size := "", 0
	for i, entry := range raw.Tools {
		if len(entry) > size {
			name, size = decoded.Tools[i].Name, len(entry)
		}
	}
	return name, size
}

// syncMCPInfo compares the rendered artifact against its committed copy, or
// rewrites it under -update-mcp-info.
//
// The failure names the resolved path and the regeneration command: this
// package reaches the artifact by walking up out of the Go module, so a package
// move surfaces here as a missing file and the person doing the move needs to
// be told what to fix rather than handed a bare "no such file".
func syncMCPInfo(t *testing.T, path string, want []byte) {
	t.Helper()
	if *updateMCPInfo {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", path, err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		absolute, resolveErr := filepath.Abs(path)
		if resolveErr != nil {
			absolute = path
		}
		t.Fatalf("reading the committed artifact %s (resolved to %s): %v\n"+
			"This test renders it from the served surface and reaches it by walking up out of "+
			"internal/compose. If this package has moved, fix the walk-up in mcpInfoDoc. "+
			"Otherwise regenerate with: go test ./internal/compose/ -run TestPublishedMCPSurface -update-mcp-info",
			path, absolute, err)
	}
	if bytes.Equal(got, want) {
		return
	}
	t.Errorf("%s is stale — it no longer matches what a client is served.\n"+
		"Regenerate it with: go test ./internal/compose/ -run TestPublishedMCPSurface -update-mcp-info\n"+
		"and commit the result together with the change that moved the surface.\n%s",
		path, firstMCPInfoDifference(string(got), string(want)))
}

// firstMCPInfoDifference reports the first line where two renderings diverge. A
// full dump of two tool catalogs is unreadable in test output; the first
// divergent line is what a reader needs to see which entry moved.
func firstMCPInfoDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			return "first difference at line " + strconv.Itoa(i+1) + ":\n  committed: " +
				truncateLine(gotLines[i]) + "\n  served:    " + truncateLine(wantLines[i])
		}
	}
	return "the committed artifact has " + strconv.Itoa(len(gotLines)) +
		" lines and the served surface renders " + strconv.Itoa(len(wantLines))
}

// truncateLine bounds one reported line: a tool entry is a single line of JSON
// several kilobytes long, and printing two of them in full buries the one
// character that changed.
func truncateLine(line string) string {
	const limit = 240
	if len(line) <= limit {
		return line
	}
	return line[:limit] + "…"
}
