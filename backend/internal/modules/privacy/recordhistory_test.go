// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestComposeRecordSummary(t *testing.T) {
	tests := []struct {
		name             string
		actorType        string
		actorDisplayName string
		onBehalfOfName   *string
		action           string
		want             string
	}{
		{
			name:             "human",
			actorType:        "human",
			actorDisplayName: "Alice",
			action:           "update",
			want:             "Alice updated the record",
		},
		{
			name:             "agent acting with authority",
			actorType:        "agent",
			actorDisplayName: "Bot",
			onBehalfOfName:   strPtr("Devin"),
			action:           "archive",
			want:             "Agent acting for Devin archived the record",
		},
		{
			name:             "agent without authority",
			actorType:        "agent",
			actorDisplayName: "Bot",
			action:           "create",
			want:             "Agent created the record",
		},
		{
			name:             "agent with empty onBehalfOfName treated as absent",
			actorType:        "agent",
			actorDisplayName: "Bot",
			onBehalfOfName:   strPtr(""),
			action:           "create",
			want:             "Agent created the record",
		},
		{
			name:             "system",
			actorType:        "system",
			actorDisplayName: "system",
			action:           "export",
			want:             "System exported the record",
		},
		{
			name:             "connector",
			actorType:        "connector",
			actorDisplayName: "hubspot-sync",
			action:           "import",
			want:             "Connector imported the record",
		},
		{
			name:             "unrecognized actor type falls back to the raw type as its own subject",
			actorType:        "robot",
			actorDisplayName: "Robot",
			action:           "update",
			want:             "robot updated the record",
		},
		{
			name:             "unknown action falls back to the raw action string, never an error",
			actorType:        "human",
			actorDisplayName: "Alice",
			action:           "frobnicate",
			want:             "Alice frobnicate the record",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := composeRecordSummary(tt.actorType, tt.actorDisplayName, tt.onBehalfOfName, tt.action)
			if got != tt.want {
				t.Errorf("composeRecordSummary(%q, %q, %v, %q) = %q, want %q",
					tt.actorType, tt.actorDisplayName, tt.onBehalfOfName, tt.action, got, tt.want)
			}
		})
	}
}

// coreMigrationsDir is relative to this package directory
// (backend/internal/modules/privacy), the same "walk up to the repo tree"
// style as backend/license_test.go and backend/arch_test.go use from the
// backend root.
const coreMigrationsDir = "../../../migrations/core"

// auditActionConstraint is the CHECK this gate derives its vocabulary from —
// anchored on the constraint NAME so a sibling *_action vocabulary restated in
// a later migration cannot win the last-wins scan.
const auditActionConstraint = "audit_log_action_check"

// auditActionCheckClause matches the single-quoted literal list inside the
// audit_log_action_check CHECK constraint, across its multi-line layout.
var auditActionCheckLiteral = regexp.MustCompile(`'([a-z_]+)'`)

// verbsFromCheckConstraint returns every verb the effective
// audit_log_action_check admits. The vocabulary grows additively (drop
// CHECK + re-add wider), so the effective set is the HIGHEST-numbered
// migration that restates it — reading one pinned file instead is how a
// later widening (0133 added advance_phase) sailed past this gate while
// the new verb rendered no phrase at all. ADR-0017's 4-digit prefix makes
// lexical order migration order, so the last match wins.
func verbsFromCheckConstraint(t *testing.T) []string {
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
	return verbs
}

func TestRecordHistoryEntryMasksBothPayloadSidesByOmission(t *testing.T) {
	row := recordAuditRow{
		actorType: "human", actorID: "human:x", action: "update",
		before: map[string]any{"email": "old@x.com", "iban": "DE01"},
		after:  map[string]any{"email": "new@x.com", "iban": "DE02"},
	}

	entry := recordHistoryEntry(row, entityFieldMask{"iban": {}})
	for side, payload := range map[string]map[string]any{"before": entry.Before, "after": entry.After} {
		if _, leaked := payload["iban"]; leaked {
			t.Errorf("masked field leaked through %s: %v", side, payload)
		}
		if payload["email"] == nil {
			t.Errorf("unmasked field withheld from %s: %v", side, payload)
		}
	}

	// The default mask is empty: the payload passes through whole.
	entry = recordHistoryEntry(row, defaultFieldMasks["person"])
	if entry.Before["iban"] != "DE01" || entry.After["iban"] != "DE02" {
		t.Errorf("empty default mask must pass the payload through: before %v after %v", entry.Before, entry.After)
	}
}

func TestRecordHistoryEntryActorDisplayFallsBackToRawActorID(t *testing.T) {
	row := recordAuditRow{actorType: "human", actorID: "human:1a2b", action: "update"}
	if got := recordHistoryEntry(row, nil).Summary; got != "human:1a2b updated the record" {
		t.Errorf("unresolved actor summary = %q, want the raw actor_id, never an invented name", got)
	}
	row.actorDisplayName = strPtr("Uma Underwriter")
	if got := recordHistoryEntry(row, nil).Summary; got != "Uma Underwriter updated the record" {
		t.Errorf("resolved actor summary = %q", got)
	}
}

func TestRecordHistoryVerbsCoverTheAuditCheckVocabulary(t *testing.T) {
	verbs := verbsFromCheckConstraint(t)
	if len(verbs) < 25 {
		// A parse regression (e.g. the clause moved or the regex stopped
		// matching) would silently pass an empty/short list otherwise —
		// the known 0053 count is a floor, not a ceiling, so a widening
		// migration after this one still passes.
		t.Fatalf("parsed only %d verb(s) from %s, want at least 25 — parser likely broken: %v",
			len(verbs), coreMigrationsDir, verbs)
	}
	var missing []string
	for _, verb := range verbs {
		if _, ok := recordHistoryVerbs[verb]; !ok {
			missing = append(missing, verb)
		}
	}
	if len(missing) > 0 {
		t.Errorf("recordHistoryVerbs is missing a rendering phrase for: %s (every verb the audit_log_action_check CHECK admits must have one)",
			strings.Join(missing, ", "))
	}
}
