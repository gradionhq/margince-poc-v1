// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	nethttp "net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// embedReindexHandlers is the contract-first placeholder for the
// `/embeddings/reindex*` surface (ADR-0068 design §5.6, Task 13). The
// three ops ship one phase ahead of their wiring: until Task 14/15 wire
// the search module's binding-marker rollups (search/binding.go) into
// real handlers and shadow this whole set, every call answers a loud
// 501 — never a silent 404, and never a half-working surface. It is
// embedded in Server so ServerInterface stays fully covered.
type embedReindexHandlers struct{}

func (embedReindexHandlers) EmbedReindexStatus(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "EmbedReindexStatus")
}

func (embedReindexHandlers) EmbedReindexPreview(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "EmbedReindexPreview")
}

func (embedReindexHandlers) EmbedReindexStart(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "EmbedReindexStart")
}
