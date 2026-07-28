// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// The audit attribution gate as a fitness function. Every mutation writes
// audit_log.authorization_rule from AuthzRule, which renders "which rule
// allowed this" by looking the verb up in auditActionGrant. A verb missing
// from that map renders the EMPTY string, and audit_log.authorization_rule is
// nullable text — so the blank is accepted silently and the accountability
// answer is gone for good once the acting role's policy is edited or the role
// deleted. That is how advance_phase (migration 0133) shipped writing blank
// attribution on every project phase change, while its sibling advance_stage
// rendered correctly.
//
// The obligation is DERIVED, not restated: the vocabulary comes from the
// highest-numbered migration that states audit_log_action_check, so the next
// widening migration fails here instead of writing blanks. Verbs no governed
// principal writes are declared in auditVerbNoGrant with a reason, and that
// waiver is a ratchet — a waived verb must still be in the vocabulary and must
// NOT have a grant.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// coreMigrationsDir is relative to this package directory
// (backend/internal/platform/auth) — the same walk-up style the privacy
// module's record-history vocabulary gate uses.
const coreMigrationsDir = "../../../migrations/core"

// auditActionConstraint is the CHECK this gate derives its vocabulary from.
const auditActionConstraint = "audit_log_action_check"

// auditActionCheckLiteral pulls the quoted verbs out of an IN-list.
var auditActionCheckLiteral = regexp.MustCompile(`'([a-z_]+)'`)

// auditVerbNoGrant: verbs the audit_log_action_check CHECK admits that no
// human- or agent-authored storekit.Audit call writes, so no CRUD grant
// attributes them. Each carries the reason it needs none.
var auditVerbNoGrant = map[string]string{
	"approve": "approvals writes its own decision row (approvals/service.go) without the authorization_rule column",
	// Rule() renders the AUDITED entity, and this verb's only storekit writer
	// audits voice_profile_version — not an RBAC policy object, so any rule it
	// rendered would name a grant that cannot exist. The transition is really
	// admitted by voice_profile.update; naming the right object means auditing
	// under it, which belongs with that module, not with this map.
	"reject":      "audited on voice_profile_version, which is not an RBAC policy object; the governing grant is voice_profile.update",
	"anonymize":   "privacy erasure only, under the system principal — AuthzRule answers \"system\" before the map is read",
	"demote":      "admitted by the contract vocabulary; no Go writer emits it yet",
	"import":      "admitted by the contract vocabulary; no Go writer emits it yet",
	"import_undo": "admitted by the contract vocabulary; no Go writer emits it yet",
	"disqualify":  "admitted by the contract vocabulary; no Go writer emits it yet",
	"send_email":  "the agent tool of that name stages an approval; the send itself audits the activity as create",
}

func TestEveryAuditVerbRendersItsAuthorizationRule(t *testing.T) {
	verbs := auditActionVocabulary(t)

	// A HUMAN principal: AuthzRule short-circuits to "system" for the system
	// principal, which would pass every verb vacuously.
	human := principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:fitness",
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}, RowScope: principal.RowScopeTeam},
	}

	admitted := make(map[string]struct{}, len(verbs))
	for _, verb := range verbs {
		admitted[verb] = struct{}{}
		reason, waived := auditVerbNoGrant[verb]
		rule := AuthzRule(human, "project", verb)

		if waived {
			if reason == "" {
				t.Errorf("audit verb %q: reasonless waiver in auditVerbNoGrant", verb)
			}
			if rule != "" {
				t.Errorf("audit verb %q: stale waiver — it now renders %q; remove it from auditVerbNoGrant", verb, rule)
			}
			continue
		}
		if rule == "" {
			t.Errorf("audit verb %q: AuthzRule renders a BLANK authorization_rule — every write of this verb would record no governing grant. Add it to auditActionGrant naming the grant its write path actually requires, or record it in auditVerbNoGrant with a reason", verb)
		}
	}

	// The ratchet in the other direction: a waived verb the vocabulary no
	// longer admits is dead weight that hides the next drift.
	for verb := range auditVerbNoGrant {
		if _, ok := admitted[verb]; !ok {
			t.Errorf("audit verb %q: stale waiver — no audit_log_action_check admits it; remove it from auditVerbNoGrant", verb)
		}
	}
}

// auditActionVocabulary returns the effective audit_log.action set. The
// vocabulary grows additively (drop CHECK + re-add wider), so the effective set
// is the HIGHEST-numbered migration that states it; ADR-0017's 4-digit prefix
// makes lexical order migration order.
func auditActionVocabulary(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(coreMigrationsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", coreMigrationsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var verbs []string
	var source string
	for _, name := range names {
		path := filepath.Join(coreMigrationsDir, name)
		raw, err := os.ReadFile(path) // #nosec G304 -- a *.up.sql name from the trusted migrations tree, test-only
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		text := string(raw)
		// Anchored on the CONSTRAINT name, not on "action IN (": a sibling
		// vocabulary (automation_action, retention_action, …) restated in a
		// later migration would otherwise win the last-wins scan and this gate
		// would validate the wrong verb set.
		named := strings.Index(text, auditActionConstraint)
		if named == -1 {
			continue
		}
		rest := text[named:]
		off := strings.Index(rest, "action IN (")
		if off == -1 {
			continue
		}
		clause := rest[off:]
		end := strings.Index(clause, ")")
		if end == -1 {
			t.Fatalf("%s: unterminated \"action IN (\" clause", path)
		}
		matches := auditActionCheckLiteral.FindAllStringSubmatch(clause[:end], -1)
		if len(matches) == 0 {
			t.Fatalf("%s: matched the IN clause but found no quoted verbs inside it", path)
		}
		verbs = verbs[:0]
		for _, m := range matches {
			verbs = append(verbs, m[1])
		}
		source = path
	}
	if source == "" {
		t.Fatalf("%s: no migration states an \"action IN (\" clause — has the constraint been renamed?", coreMigrationsDir)
	}
	if len(verbs) < 25 {
		// A parse regression would otherwise pass a short set vacuously.
		t.Fatalf("parsed only %d verb(s) from %s, want at least the 25 the shipped vocabulary carries: %v",
			len(verbs), source, verbs)
	}
	return verbs
}
