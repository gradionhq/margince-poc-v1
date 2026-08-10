// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// docs/reference/mcp-info.md — the same served surface as its .json sibling,
// for a reader rather than a differ.
//
// Both artifacts are rendered in one pass and gated together, and the markdown
// is rendered FROM the JSON document rather than from the registry a second
// time. That is what makes it a view and not a second source: a page built from
// its own walk of the specs could disagree with the payload it claims to
// describe, and nothing would catch it.
//
// The JSON stays the artifact of record because it is what a client receives
// byte for byte. It is also 200 KB of nested schema, which is not a thing a
// person reads to answer "what can this tool do" — so this page carries the
// index, the sizes and the prose, and keeps each schema behind a fold.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// mcpInfoPage is the published markdown, beside the JSON it is rendered from.
var mcpInfoPage = filepath.Join("..", "..", "..", "docs", "reference", "mcp-info.md")

// mcpToolEntry is the part of a served tool this page reads. It decodes the
// PUBLISHED json rather than mcp.ToolSpec, because the page describes what the
// wire carried — the transport appends the governance clause to the description
// and derives the annotations, and neither is visible on the spec.
type mcpToolEntry struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	//nolint:tagliatelle // inputSchema is the MCP wire member, camelCase by the protocol
	InputSchema json.RawMessage `json:"inputSchema"`
	//nolint:tagliatelle // outputSchema is the MCP wire member, camelCase by the protocol
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  struct {
		//nolint:tagliatelle // readOnlyHint is the MCP wire member, camelCase by the protocol
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
	// Meta carries the tool's view binding. A tool associates itself with the
	// document that renders its result through `_meta.ui.resourceUri` on the
	// DECLARATION — the App extension puts it here rather than on the result —
	// and the server serves it only where the deployment holds that document.
	Meta struct {
		UI *struct {
			//nolint:tagliatelle // resourceUri is the App extension's wire member, camelCase by the protocol
			ResourceURI string   `json:"resourceUri"`
			Visibility  []string `json:"visibility"`
		} `json:"ui"`
	} `json:"_meta"` //nolint:tagliatelle // _meta is the protocol's reserved extension member, and the leading underscore is what reserves it
}

type mcpResourceEntry struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	//nolint:tagliatelle // mimeType is the MCP wire member, camelCase by the protocol
	MIMEType string `json:"mimeType"`
	//nolint:tagliatelle // _meta is the protocol's reserved extension member, and the leading underscore is what reserves it
	Meta json.RawMessage `json:"_meta"`
}

// renderMCPInfoMarkdown builds the page from the published document.
func renderMCPInfoMarkdown(t *testing.T, doc mcpInfo) []byte {
	t.Helper()
	var tools struct {
		Tools []mcpToolEntry `json:"tools"`
	}
	if err := json.Unmarshal(doc.Tools, &tools); err != nil {
		t.Fatalf("reading the published tool catalog: %v", err)
	}
	var resources struct {
		Resources []mcpResourceEntry `json:"resources"`
	}
	if err := json.Unmarshal(doc.Resources, &resources); err != nil {
		t.Fatalf("reading the published resource catalog: %v", err)
	}

	var page strings.Builder
	writeMCPInfoHead(&page, doc)
	writeMCPInfoIndex(&page, tools.Tools, resources.Resources)
	writeMCPInfoResources(&page, resources.Resources)
	writeMCPInfoTools(&page, tools.Tools)
	return []byte(page.String())
}

func writeMCPInfoHead(page *strings.Builder, doc mcpInfo) {
	page.WriteString("# The served MCP surface\n\n")
	page.WriteString("<!-- Generated together with mcp-info.json; do not edit by hand. -->\n\n")
	page.WriteString(doc.Note + "\n\n")
	page.WriteString("`mcp-info.json` beside this page is the same surface byte for byte, as a client\n")
	page.WriteString("receives it. This page is rendered from that file.\n\n")
	page.WriteString("## Totals\n\n| | |\n|---|---:|\n")
	fmt.Fprintf(page, "| Tools | %d |\n", doc.Totals.Tools)
	fmt.Fprintf(page, "| Resources | %d |\n", doc.Totals.Resources)
	fmt.Fprintf(page, "| Tool catalog | %s |\n", humanBytes(doc.Totals.ToolBytes))
	fmt.Fprintf(page, "| Resource catalog | %s |\n", humanBytes(doc.Totals.ResourceBytes))
	fmt.Fprintf(page, "| Approx. wire tokens | %d |\n", doc.Totals.ApproxTokens)
	fmt.Fprintf(page, "| Largest tool | `%s` (%s) |\n", doc.Totals.LargestToolNam, humanBytes(doc.Totals.LargestToolB))
	fmt.Fprintf(page, "| Scopes rendered | %s |\n\n", "`"+strings.Join(doc.Scopes, "`, `")+"`")
	page.WriteString("Those are the WIRE bytes: they carry each tool's output schema and the governance\n")
	page.WriteString("clause the transport appends. The Surface-B listing a run re-sends every step is\n")
	page.WriteString("smaller — name, description and input schema only — and is held against its own\n")
	page.WriteString("budget in `agenttooldescriptions_test.go`.\n\n")
}

func writeMCPInfoIndex(page *strings.Builder, tools []mcpToolEntry, resources []mcpResourceEntry) {
	page.WriteString("## Index\n\n")
	fmt.Fprintf(page, "### Resources (%d)\n\n", len(resources))
	for _, r := range resources {
		fmt.Fprintf(page, "- [`%s`](#%s) — %s\n", r.URI, anchor(r.Name), r.Title)
	}
	fmt.Fprintf(page, "\n### Tools (%d)\n\n", len(tools))
	page.WriteString("| Tool | What it is for | Read-only | View | Size |\n|---|---|:-:|---|---:|\n")
	for _, tool := range tools {
		mark := ""
		if tool.Annotations.ReadOnlyHint {
			mark = "yes"
		}
		view := ""
		if tool.Meta.UI != nil {
			view = "[`" + tool.Meta.UI.ResourceURI + "`](#" + anchor(viewNameOf(tool.Meta.UI.ResourceURI)) + ")"
		}
		fmt.Fprintf(page, "| [`%s`](#%s) | %s | %s | %s | %s |\n",
			tool.Name, anchor(tool.Name), cell(tool.Title), mark, view, humanBytes(toolBytes(tool)))
	}
	page.WriteString("\n")
}

func writeMCPInfoResources(page *strings.Builder, resources []mcpResourceEntry) {
	page.WriteString("## Resources\n\n")
	page.WriteString("A resource takes no arguments and changes nothing, so it carries no autonomy\n")
	page.WriteString("tier — but it is scope-filtered exactly as a tool is, so a passport holding\n")
	page.WriteString("fewer scopes is served fewer documents.\n\n")
	for _, r := range resources {
		fmt.Fprintf(page, "### %s\n\n", r.Name)
		fmt.Fprintf(page, "`%s` · %s\n\n", r.URI, r.MIMEType)
		if r.Title != "" {
			fmt.Fprintf(page, "**%s**\n\n", r.Title)
		}
		page.WriteString(r.Description + "\n\n")
		if len(r.Meta) > 0 {
			page.WriteString("<details><summary>Sandbox policy (<code>_meta.ui</code>)</summary>\n\n```json\n")
			page.WriteString(indentJSON(r.Meta) + "\n```\n\n</details>\n\n")
		}
	}
}

func writeMCPInfoTools(page *strings.Builder, tools []mcpToolEntry) {
	page.WriteString("## Tools\n\n")
	for _, tool := range tools {
		fmt.Fprintf(page, "### %s\n\n", tool.Name)
		if tool.Title != "" {
			fmt.Fprintf(page, "**%s**\n\n", tool.Title)
		}
		page.WriteString(tool.Description + "\n\n")
		if tool.Meta.UI != nil {
			fmt.Fprintf(page, "Renders its result in [`%s`](#%s), visible to %s.\n\n",
				tool.Meta.UI.ResourceURI, anchor(viewNameOf(tool.Meta.UI.ResourceURI)),
				"`"+strings.Join(tool.Meta.UI.Visibility, "`, `")+"`")
		}
		page.WriteString("<details><summary>Input schema</summary>\n\n```json\n")
		page.WriteString(indentJSON(tool.InputSchema) + "\n```\n\n</details>\n\n")
		if len(tool.OutputSchema) > 0 {
			page.WriteString("<details><summary>Output schema</summary>\n\n```json\n")
			page.WriteString(indentJSON(tool.OutputSchema) + "\n```\n\n</details>\n\n")
		}
	}
}

// viewNameOf turns a view's URI into the heading its section is published
// under, so the index can link a tool straight to the document that renders it.
// ui://margince/account-brief.html is the account_brief_view section.
func viewNameOf(uri string) string {
	file := uri[strings.LastIndexByte(uri, '/')+1:]
	return strings.ReplaceAll(strings.TrimSuffix(file, ".html"), "-", "_") + "_view"
}

// toolBytes is one entry's served size, which is what the index sorts a
// reader's attention by: a catalog is bounded by its total, and the total is
// spent by whichever entries are largest.
func toolBytes(tool mcpToolEntry) int {
	encoded, err := json.Marshal(tool)
	if err != nil {
		// Marshal of a value decoded FROM json cannot fail; a size of zero is
		// still honest here rather than a panic in a page renderer.
		return 0
	}
	return len(encoded)
}

// anchor is the fragment GitHub derives from a heading: lowercased, spaces to
// hyphens, and anything else that is not a letter, digit, hyphen or underscore
// dropped.
func anchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// cell makes a value safe to put in a markdown table: a pipe would end the
// column, and a newline the row.
func cell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

func indentJSON(raw json.RawMessage) string {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		// The input came off a decoded document, so this cannot fail — and if it
		// somehow did, the raw bytes are more useful in the page than nothing.
		return string(raw)
	}
	return pretty.String()
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
