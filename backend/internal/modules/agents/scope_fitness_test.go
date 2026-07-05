// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Passport-scope coverage as a fitness function. A leaked or over-broad
// passport's blast radius is exactly the set of tools its scopes admit, so
// the mapping tool→scope is a security surface. Two invariants, derived from
// the registered surface so a new tool cannot quietly break them:
//   - every tool requires a scope from the closed passport vocabulary; and
//   - outbound egress (send_email, book_meeting) is gated on the dedicated
//     `send` scope, never plain `write` — so a passport can edit records yet
//     be barred from sending mail.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// passportScopeVocabulary is the closed set a passport may carry
// (interfaces.md §2, mirrored by identity.validScopes and the crm.yaml
// passport scope enum). A tool requiring anything outside it could never be
// admitted by any passport.
var passportScopeVocabulary = map[principal.Scope]bool{
	principal.ScopeRead:   true,
	principal.ScopeDraft:  true,
	principal.ScopeWrite:  true,
	principal.ScopeSend:   true,
	principal.ScopeEnrich: true,
}

func TestEveryToolScopeIsGrantableAndEgressNeedsSend(t *testing.T) {
	registry := NewRegistry(nil, nil)
	RegisterCoreTools(registry, nil, nil, nil, nil)

	for _, spec := range registry.Specs() {
		if !passportScopeVocabulary[spec.RequiredScope] {
			t.Errorf("tool %q requires scope %q outside the passport vocabulary — no passport could admit it",
				spec.Name, spec.RequiredScope)
		}
		if spec.Egress && spec.RequiredScope != principal.ScopeSend {
			t.Errorf("egress tool %q requires scope %q — outbound egress must be gated on %q, not %q, so a write passport is not implicitly a send passport",
				spec.Name, spec.RequiredScope, principal.ScopeSend, principal.ScopeWrite)
		}
	}
}
