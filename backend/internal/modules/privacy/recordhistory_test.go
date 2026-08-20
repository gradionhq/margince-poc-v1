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
		passportBacked   bool
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
			// PD-002: the granting human is the SUBJECT and the agent is a
			// qualifier on them. The old phrasing made the machine the subject
			// and the person a prepositional phrase, which is the inversion
			// this decision exists to forbid: "an agent did it" names nobody
			// who can be asked about the change.
			name:             "agent acting with authority names the human first",
			actorType:        "agent",
			actorDisplayName: "Bot",
			onBehalfOfName:   strPtr("Devin"),
			action:           "archive",
			want:             "Devin, via an agent, archived the record",
		},
		{
			// A PASSPORT was presented and no human resolved behind it. That is
			// a gap — passport.on_behalf_of is NOT NULL, so the authority
			// existed at grant time and the row failed to carry it. It is NOT
			// rendered as "System": system is reserved for a change that
			// genuinely has nobody behind it, and letting it absorb a missing
			// attribution would hide the gap instead of showing it.
			name:             "passport presented but no authority resolved is a gap, never 'System'",
			actorType:        "agent",
			actorDisplayName: "Bot",
			action:           "create",
			passportBacked:   true,
			want:             "A machine with no recorded human authority created the record",
		},
		{
			// The deciding input is whether a GRANT was presented, not which
			// machine word actor_type carries. A background pass nobody's
			// context ran has no human to name and no gap to report —
			// compose/extjobsrun.go writes one per extension job tick, and
			// calling that a missing authority would report a defect where
			// there is none.
			name:             "background agent with no passport is not a gap",
			actorType:        "agent",
			actorDisplayName: "Bot",
			action:           "create",
			want:             "Agent created the record",
		},
		{
			name:             "empty onBehalfOfName is treated as absent, not as authority",
			actorType:        "agent",
			actorDisplayName: "Bot",
			onBehalfOfName:   strPtr(""),
			action:           "create",
			passportBacked:   true,
			want:             "A machine with no recorded human authority created the record",
		},
		{
			// A connector presenting a passport is the same gap as an agent
			// presenting one: the rule keys on the grant, not the word.
			name:             "passport-backed connector with no authority is the same gap",
			actorType:        "connector",
			actorDisplayName: "gmail",
			action:           "import",
			passportBacked:   true,
			want:             "A machine with no recorded human authority imported the record",
		},
		{
			// A connector's human authority resolves the same way an agent's
			// does: an inbound sync a person authorized reads as that person.
			name:             "connector acting with authority names the human first",
			actorType:        "connector",
			actorDisplayName: "gmail",
			onBehalfOfName:   strPtr("Devin"),
			action:           "import",
			want:             "Devin, via a connector, imported the record",
		},
		{
			name:             "system",
			actorType:        "system",
			actorDisplayName: "system",
			action:           "export",
			want:             "System exported the record",
		},
		{
			// A bare connector keeps its own subject rather than reporting a
			// missing authority: some connectors have no connect flow and so no
			// granting human BY DESIGN (compose/jobs_finance.go writes one), and
			// naming a gap there would report a defect that does not exist. This
			// is the deliberate asymmetry with the agent case above.
			name:             "connector with no authority is not a gap, unlike an agent",
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
			got := composeRecordSummary(tt.actorType, tt.actorDisplayName, tt.onBehalfOfName,
				tt.action, tt.passportBacked)
			if got != tt.want {
				t.Errorf("composeRecordSummary(%q, %q, %v, %q, passport=%v) = %q, want %q",
					tt.actorType, tt.actorDisplayName, tt.onBehalfOfName, tt.action,
					tt.passportBacked, got, tt.want)
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
// CHECK + re-add wider), so the effective set is the LAST migration that
// restates it — reading one pinned file instead is how a later widening
// (0133 added advance_phase) sailed past this gate while the new verb
// rendered no phrase at all. core versions compare as strings, and its
// closed sequence is zero-padded so it sorts below the later unix-second
// stamps, which makes lexical order migration order and the last match win.
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
