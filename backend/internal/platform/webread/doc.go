// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"mime"
	"strings"
)

// Doc is one fetched page reduced to model-ready text, plus the media type the
// server served — so a caller can tell verbatim markdown from stripped HTML and
// log which it got. Text is the server's markdown verbatim when it negotiated
// text/markdown; otherwise it is StripTags of the HTML, exactly as before.
type Doc struct {
	Text      string
	MediaType string // parsed, parameter-stripped (e.g. "text/markdown"); "" when the server declared none
}

// IsMarkdown reports whether the server served markdown — text/markdown, or the
// legacy text/x-markdown spelling. This is the one place those media types are
// named, so the fetch branch and callers agree on the test.
func (d Doc) IsMarkdown() bool {
	switch d.MediaType {
	case "text/markdown", "text/x-markdown":
		return true
	default:
		return false
	}
}

// parseMediaType extracts the bare media type from a Content-Type header,
// lowercased and without parameters. A malformed header is not an error here —
// a mislabeled server must never fail a fetch — so the trimmed, lowercased raw
// value is returned as a best effort, which simply will not match a markdown type.
func parseMediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		return mediaType
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}
