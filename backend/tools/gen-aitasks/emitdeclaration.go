// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"strings"
)

// writeDeclarationTables appends the ADR-0074 declaration half of
// tasks_gen.go: the status table, the invocation sites, the payload
// prohibition, the company-context policy, and the cost-unit rule names.
//
// Each table is reached through an accessor rather than an exported map. An
// exported map is a table any caller can mutate, which is not a contract —
// routing.go's existing wrapper around taskLadders is the same call.
//
// taskNames arrives already sorted so every emitted map literal keeps the one
// deterministic order the whole file is written in.
func writeDeclarationTables(b *strings.Builder, c contract, taskNames []string) {
	b.WriteString("// Status reports whether a task ships or is declared-but-unbuilt. A\n")
	b.WriteString("// planned task has no site, no corpus scenario, and no certification\n")
	b.WriteString("// record — which is what stops it presenting as certified.\n")
	b.WriteString(goConstBlockStart)
	fmt.Fprintf(b, "\tStatusShipped = %q\n\tStatusPlanned = %q\n", statusShipped, statusPlanned)
	b.WriteString(")\n\n")
	b.WriteString("var taskStatus = map[Task]string{\n")
	for _, name := range taskNames {
		fmt.Fprintf(b, "\t%s: %q,\n", taskConst(name), c.Tasks[name].Status)
	}
	b.WriteString("}\n\n")
	b.WriteString("// Status returns the declared status, or \"\" for a task this table does\n")
	b.WriteString("// not carry.\nfunc Status(t Task) string { return taskStatus[t] }\n\n")

	b.WriteString("// Site is one named model-invocation site of a task. A task is NOT one\n")
	b.WriteString("// prompt: rate_extract has two, cold_start four. Kind says how the site\n")
	b.WriteString("// invokes the model, because an agent loop is a cumulative tool-fed\n")
	b.WriteString("// window and must not be described as a request factory.\n")
	b.WriteString("type Site struct {\n\tName string\n\tKind string\n}\n\n")
	b.WriteString(goConstBlockStart)
	fmt.Fprintf(b, "\tSiteKindOneShot   = %q\n", kindOneShot)
	fmt.Fprintf(b, "\tSiteKindMultiTurn = %q\n", kindMultiTurn)
	fmt.Fprintf(b, "\tSiteKindAgentLoop = %q\n", kindAgentLoop)
	b.WriteString(")\n\n")
	b.WriteString("var taskSites = map[Task][]Site{\n")
	for _, name := range taskNames {
		sites := c.Tasks[name].Sites
		if len(sites) == 0 {
			continue
		}
		fmt.Fprintf(b, "\t%s: {\n", taskConst(name))
		for _, s := range sites {
			fmt.Fprintf(b, "\t\t{Name: %q, Kind: %q},\n", s.Name, s.Kind)
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("// SitesFor returns the task's declared sites in contract order. A\n")
	b.WriteString("// planned task returns none.\n")
	b.WriteString("func SitesFor(t Task) []Site { return taskSites[t] }\n\n")

	b.WriteString("// noPayloadTasks are the tasks whose content must NEVER reach\n")
	b.WriteString("// ai_call_payload, whatever the deployment's capture posture says. The\n")
	b.WriteString("// contract pins the prohibition as a hard property, not a default.\n")
	b.WriteString("var noPayloadTasks = map[Task]bool{\n")
	for _, name := range taskNames {
		if c.Tasks[name].NoPayload {
			fmt.Fprintf(b, "\t%s: true,\n", taskConst(name))
		}
	}
	b.WriteString("}\n\n")
	b.WriteString("// NoPayload reports the contract's payload prohibition for a task.\n")
	b.WriteString("func NoPayload(t Task) bool { return noPayloadTasks[t] }\n\n")

	writeCompanyContextTable(b, c, taskNames)

	b.WriteString("// taskCostUnit names each priced task's unit rule. The arithmetic is\n")
	b.WriteString("// behaviour and lives in the estimator; naming the rule here is what\n")
	b.WriteString("// lets the build prove the mapping is total.\n")
	b.WriteString("var taskCostUnit = map[Task]string{\n")
	for _, name := range taskNames {
		if u := c.Tasks[name].CostUnit; u != "" {
			fmt.Fprintf(b, "\t%s: %q,\n", taskConst(name), u)
		}
	}
	b.WriteString("}\n\n")
	b.WriteString("// CostUnitFor returns the task's unit-rule name, or \"\" when unpriced.\n")
	b.WriteString("func CostUnitFor(t Task) string { return taskCostUnit[t] }\n\n")
	b.WriteString("// EmbedCostUnit is the embeddings workload's unit rule. embed is not a\n")
	b.WriteString("// task — no prompt, no text answer, no completion path — so it carries\n")
	b.WriteString("// its own contract section and its own accessor.\n")
	fmt.Fprintf(b, "func EmbedCostUnit() string { return %q }\n", c.Embed.CostUnit)
}

// writeCompanyContextTable emits the ADR-0065 policy table.
//
// `company_context: none` decodes to a non-nil empty policy, so the task
// still appears in the map with zero scopes. That distinction is the whole
// point of CompanyContextFor's bool: a DECLARED empty policy is a decision,
// an absent one is a contract defect.
func writeCompanyContextTable(b *strings.Builder, c contract, taskNames []string) {
	b.WriteString("// CompanyContextPolicy is the ADR-0065 anchor-company policy: which\n")
	b.WriteString("// scopes ride the prompt, under what character budget, and whether the\n")
	b.WriteString("// caller must ask for them. Scopes are contract NAMES; the composition\n")
	b.WriteString("// layer maps them to its own scope type, because this package must not\n")
	b.WriteString("// import a sibling module.\n")
	b.WriteString("type CompanyContextPolicy struct {\n\tScopes      []string\n\tTokenBudget int\n\tConditional bool\n}\n\n")
	b.WriteString("var taskCompanyContext = map[Task]CompanyContextPolicy{\n")
	for _, name := range taskNames {
		p := c.Tasks[name].CompanyContext
		if p == nil {
			continue
		}
		fmt.Fprintf(b, "\t%s: {", taskConst(name))
		if len(p.Scopes) > 0 {
			b.WriteString("Scopes: []string{")
			for i, s := range p.Scopes {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%q", s)
			}
			b.WriteString("}, ")
		}
		fmt.Fprintf(b, "TokenBudget: %d, Conditional: %t},\n", p.TokenBudget, p.Conditional)
	}
	b.WriteString("}\n\n")
	b.WriteString("// CompanyContextFor returns the task's declared policy. The bool reports\n")
	b.WriteString("// whether the contract DECLARES one at all — an undeclared policy is a\n")
	b.WriteString("// contract defect, distinct from a declared empty one.\n")
	b.WriteString("func CompanyContextFor(t Task) (CompanyContextPolicy, bool) {\n\tp, ok := taskCompanyContext[t]\n\treturn p, ok\n}\n\n")
}
