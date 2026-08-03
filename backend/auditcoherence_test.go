// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The audit_log enum-coherence gate as a fitness function. crm.yaml's
// AuditLogEntry.action / .actor_type are duplicated as Postgres CHECK
// constraints (audit_log_action_check / audit_log_actor_type_check), the
// effective set being the highest-numbered migration that re-states each.
// When the two copies drift, an action the contract defines and code emits
// (e.g. send_email) is rejected by the DB at write time — a latent
// audit-integrity bug that only a real INSERT surfaces.
//
// enumsync_test.go pins the columns backed by a Go enum type; audit_log's
// action/actor_type carry no such type (they are written as string
// literals), so this test is their coherence floor. crm.yaml is the source
// of truth (P3): the contract set and the DDL CHECK must agree, with one
// sanctioned asymmetry — auditActionDBOnly records verbs the DDL allows
// ahead of the contract. That waiver is a ratchet: a waived verb must be in
// the DDL and absent from the contract, so when the spec adopts it the entry
// has to be removed or this test fails.
//
// The contract is read by text scan, not a YAML library: the root
// fitness-test package stays free of parser deps (the arch-lint boundary),
// the same reason enumsync_test.go derives the DDL sets by regex.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// auditActionDBOnly: verbs the audit_log_action_check CHECK allows that the
// contract does not (yet) define. Each carries a one-line reason.
var auditActionDBOnly = map[string]string{
	// live in signal_resolution writes (migration 0047); flagged upstream
	// for spec adoption (see migration 0053's note).
	"resolve": "signal_resolution writes (0047); flagged upstream for spec adoption",
}

func TestAuditLogEnumCoherence(t *testing.T) {
	contractAction, contractActorType := auditLogContractEnums(t)
	ddl := tableCheckSets(t) // reused from enumsync_test.go (last-wins over migrations)

	cases := []struct {
		column   string
		contract []string
		dbOnly   map[string]string
	}{
		{"audit_log.action", contractAction, auditActionDBOnly},
		{"audit_log.actor_type", contractActorType, nil},
	}

	for _, c := range cases {
		got, ok := ddl[c.column]
		if !ok {
			t.Errorf("%s: no CHECK (col IN (...)) constraint found in migrations", c.column)
			continue
		}
		ddlSet := auditStrSet(got)
		conSet := auditStrSet(c.contract)

		// Direction 1 (dangerous): every contract value must be a legal DB
		// value, else code that emits it would 500 at INSERT.
		for _, v := range c.contract {
			if _, ok := ddlSet[v]; !ok {
				t.Errorf("%s: contract defines %q but the DDL CHECK rejects it — a write of this action would 500; widen the CHECK in a new migration to match crm.yaml", c.column, v)
			}
		}
		// Direction 2: every DB value must be defined by the contract, unless
		// explicitly waived as a DB-only verb.
		for _, v := range got {
			if _, ok := conSet[v]; ok {
				continue
			}
			if _, waived := c.dbOnly[v]; waived {
				continue
			}
			t.Errorf("%s: DDL CHECK allows %q but the contract does not define it — add it to crm.yaml (P3), or record it in auditActionDBOnly with a reason", c.column, v)
		}
		// The waiver is a ratchet: a waived verb must genuinely be DB-only.
		for v := range c.dbOnly {
			if _, ok := ddlSet[v]; !ok {
				t.Errorf("%s: stale waiver %q — no DDL CHECK allows it; remove it from auditActionDBOnly", c.column, v)
			}
			if _, ok := conSet[v]; ok {
				t.Errorf("%s: stale waiver %q — the contract now defines it; remove it from auditActionDBOnly", c.column, v)
			}
		}
	}
}

// auditLogContractEnums returns the sorted action and actor_type enum sets
// declared on components.schemas.AuditLogEntry in api/crm.yaml.
func auditLogContractEnums(t *testing.T) (action, actorType []string) {
	t.Helper()
	raw, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	block := schemaBlock(t, string(raw), "AuditLogEntry")

	// action: a standalone `enum: [ ... ]` line — AuditLogEntry's only
	// multi-line enum property, so the sole match anchored to line start.
	am := regexp.MustCompile(`(?m)^\s+enum:\s*\[([^\]]*)\]`).FindStringSubmatch(block)
	if am == nil {
		t.Fatal("contract: AuditLogEntry.action enum not found")
	}
	// actor_type: an inline flow-mapping enum on the actor_type line.
	tm := regexp.MustCompile(`actor_type:[^\n]*enum:\s*\[([^\]]*)\]`).FindStringSubmatch(block)
	if tm == nil {
		t.Fatal("contract: AuditLogEntry.actor_type enum not found")
	}
	return splitEnumList(am[1]), splitEnumList(tm[1])
}

// schemaBlock returns the text of a components.schemas.<name> block: from its
// 4-space key line to the next 4-space schema key.
func schemaBlock(t *testing.T, doc, name string) string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^    ` + regexp.QuoteMeta(name) + `:[ \t]*$`).FindStringIndex(doc)
	if start == nil {
		t.Fatalf("contract: schema %s not found", name)
	}
	rest := doc[start[1]:]
	if end := regexp.MustCompile(`(?m)^    [A-Za-z]`).FindStringIndex(rest); end != nil {
		return rest[:end[0]]
	}
	return rest
}

// splitEnumList turns an `a, b, c` flow-list body into a sorted set slice.
func splitEnumList(list string) []string {
	var out []string
	for _, tok := range strings.Split(list, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	sort.Strings(out)
	return out
}

func auditStrSet(vals []string) map[string]struct{} {
	m := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}
