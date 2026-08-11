// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-aitasks compiles backend/api/ai-tasks.yaml — the AI task
// contract (ai-operational-spec §1.2) — into the tables package ai
// consumes at runtime: the Task/Tier constants, the per-task routing
// ladders, the degrade-to map, and each task's execution mode. It also regenerates
// config/ai-routing.schema.json's tier enum from the same contract, so a
// tier can be added or renamed in exactly one place (ai-tasks.yaml) and
// every downstream artifact — the binary and the deployment schema —
// picks it up on the next `make gen`.
//
// A contract that is internally inconsistent (a ladder or degrade_to
// entry naming a tier the tiers list never declares) fails generation
// rather than silently compiling into a broken routing table.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	contractPath  = flag.String("contract", "../api/ai-tasks.yaml", "the AI task contract to compile")
	outGoPath     = flag.String("out-go", "../internal/modules/ai/tasks_gen.go", "generated Go table destination")
	outSchemaPath = flag.String("out-schema", "../../config/ai-routing.schema.json", "generated routing-schema destination")
)

func main() {
	flag.Parse()

	raw, err := os.ReadFile(*contractPath) // #nosec G304 -- build-time tool, operator-chosen contract path
	if err != nil {
		log.Fatalf("gen-aitasks: reading %s: %v", *contractPath, err)
	}

	c, err := parseContract(raw)
	if err != nil {
		log.Fatalf("gen-aitasks: %v", err)
	}

	hash := sha256.Sum256(raw)
	goSrc, err := emitGo(c, hex.EncodeToString(hash[:]))
	if err != nil {
		log.Fatalf("gen-aitasks: %v", err)
	}
	if err := os.WriteFile(*outGoPath, []byte(goSrc), 0o600); err != nil {
		log.Fatalf("gen-aitasks: writing %s: %v", *outGoPath, err)
	}

	schemaSrc, err := emitSchema(c.Tiers)
	if err != nil {
		log.Fatalf("gen-aitasks: %v", err)
	}
	if err := os.WriteFile(*outSchemaPath, []byte(schemaSrc), 0o600); err != nil {
		log.Fatalf("gen-aitasks: writing %s: %v", *outSchemaPath, err)
	}

	fmt.Printf("%d tasks, %d tiers generated\n", len(c.Tasks), len(c.Tiers))
}

// siteDef is one entry of a task's sites[]: the named model-invocation site
// the build registers. Written either as a bare name (kind defaults to
// one_shot) or as a mapping when the kind differs.
type siteDef struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

// UnmarshalYAML accepts both spellings so the common case — a one-shot site —
// stays a bare string in the contract.
func (s *siteDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		s.Name, s.Kind = node.Value, kindOneShot
		return nil
	}
	var raw struct {
		Name string `yaml:"name"`
		Kind string `yaml:"kind"`
	}
	if err := decodeMapping(node, &raw); err != nil {
		return fmt.Errorf("site: %w", err)
	}
	s.Name, s.Kind = raw.Name, raw.Kind
	if s.Kind == "" {
		s.Kind = kindOneShot
	}
	return nil
}

// companyContextDef is the ADR-0065 policy: which anchor-company scopes ride
// this task's prompt, under what character budget, and whether the caller must
// ask. Absent means the task takes none.
type companyContextDef struct {
	Scopes      []string `yaml:"scopes"`
	TokenBudget int      `yaml:"token_budget"`
	Conditional bool     `yaml:"conditional"`
}

// UnmarshalYAML accepts the scalar `none` alongside a policy mapping: most
// tasks take no company context, and spelling that as an empty mapping would
// read as an oversight rather than a decision.
func (p *companyContextDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value != "none" {
			return fmt.Errorf("company_context: want a policy mapping or %q, got %q", "none", node.Value)
		}
		*p = companyContextDef{}
		return nil
	}
	var raw struct {
		Scopes      []string `yaml:"scopes"`
		TokenBudget int      `yaml:"token_budget"`
		Conditional bool     `yaml:"conditional"`
	}
	if err := decodeMapping(node, &raw); err != nil {
		return fmt.Errorf("company_context: %w", err)
	}
	p.Scopes, p.TokenBudget, p.Conditional = raw.Scopes, raw.TokenBudget, raw.Conditional
	return nil
}

// validate refuses a policy that cannot do what it says. Scope names are the
// composition layer's to resolve — this generator does not know the module's
// scope vocabulary — but coherence between the three fields is decidable here,
// and here is where every task is in view at once. An undeclared policy stays
// nil and is caught by CompanyContextFor's bool at the call, not by this rule.
func (p *companyContextDef) validate(task string) error {
	if p == nil {
		return nil
	}
	seen := make(map[string]bool, len(p.Scopes))
	for _, scope := range p.Scopes {
		if seen[scope] {
			return fmt.Errorf("task %q: company_context scope %q is declared twice", task, scope)
		}
		seen[scope] = true
	}
	if len(p.Scopes) == 0 {
		// `none` means none: a budget or a condition attached to a policy that
		// selects nothing reads as a scope list someone deleted, not a decision.
		if p.TokenBudget != 0 || p.Conditional {
			return fmt.Errorf("task %q: company_context selects no scopes but carries token_budget %d and conditional %t",
				task, p.TokenBudget, p.Conditional)
		}
		return nil
	}
	if p.TokenBudget <= 0 {
		// The budget is what the renderer bounds the block by; at zero it admits
		// no item, so the scopes would ride no prompt.
		return fmt.Errorf("task %q: company_context declares %d scope(s) and needs a positive token_budget, got %d",
			task, len(p.Scopes), p.TokenBudget)
	}
	return nil
}

// embedDef is the embeddings workload. It is NOT a task: its tier is not a
// chat tier, and it has no prompt, no text answer and no completion path, so
// it carries no sites and no certification obligation.
type embedDef struct {
	Tier     string `yaml:"tier"`
	CostUnit string `yaml:"cost_unit"`
}

// taskDef is one tasks.<name> entry: the routing ladder, the
// execution mode, budget-exhaustion policy, the declaration fields the census
// is built on (status, sites, payload posture, company-context policy,
// cost-unit rule name), and an optional doc string carried through
// to the generated constant's comment.
type taskDef struct {
	Ladder            []string           `yaml:"ladder"`
	ExecutionMode     string             `yaml:"execution_mode"`
	OnBudgetExhausted string             `yaml:"on_budget_exhausted"`
	Status            string             `yaml:"status"`
	Sites             []siteDef          `yaml:"sites"`
	NoPayload         bool               `yaml:"no_payload"`
	CompanyContext    *companyContextDef `yaml:"company_context"`
	CostUnit          string             `yaml:"cost_unit"`
	Doc               string             `yaml:"doc"`
}

// contract is the parsed ai-tasks.yaml. Tiers is a YAML sequence, so its
// declaration order survives decoding — that order becomes the Tier
// constant order and the routing schema's enum order, byte-stable across
// runs without an extra sort key. Tasks and DegradeTo are YAML mappings;
// Go map iteration order is not stable, so every consumer below sorts or
// walks Tiers explicitly instead of ranging over these maps directly.
type contract struct {
	Tiers     []string           `yaml:"tiers"`
	Tasks     map[string]taskDef `yaml:"tasks"`
	Embed     embedDef           `yaml:"embed"`
	DegradeTo map[string]string  `yaml:"degrade_to"`
}

// taskNameRE is the contract's task-naming rule: lowercase snake_case,
// matching the Go identifier derivation (pascalCase) 1:1.
var taskNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// siteNameRE mirrors taskNameRE: a site name is a Go-identifier source too.
var siteNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// The closed set of invocation kinds. A new kind is a code-and-test change in
// the census, never data: each one needs a certification strategy that can
// actually run it.
const (
	kindOneShot   = "one_shot"
	kindMultiTurn = "multi_turn"
	kindAgentLoop = "agent_loop"
)

var siteKinds = map[string]bool{kindOneShot: true, kindMultiTurn: true, kindAgentLoop: true}

// A task either ships — and owes a site the census can locate — or is declared
// but unbuilt, and owes none.
const (
	statusShipped = "shipped"
	statusPlanned = "planned"
)

const goConstBlockStart = "const (\n"

// parseContract decodes and validates ai-tasks.yaml. Unknown keys are
// errors: a typo'd field would otherwise silently drop routing policy. The
// two merge-safety rules that KnownFields alone does not buy — strictness
// inside the custom unmarshallers, and refusing anything after a second
// `---` — live in strictdecode.go and are wired here.
func parseContract(raw []byte) (contract, error) {
	var c contract
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return contract{}, fmt.Errorf("parsing contract: %w", err)
	}
	if err := rejectSecondDocument(dec); err != nil {
		return contract{}, err
	}
	if err := c.validate(); err != nil {
		return contract{}, err
	}
	return c, nil
}

// validate enforces the contract's own invariants: every tier a ladder
// or degrade_to entry names must be declared in tiers, every task name
// is a valid Go-identifier source, and on_budget_exhausted is one of the
// two policies the runtime understands. Execution mode and exhaustion policy
// are a closed pair: interactive tasks degrade, background tasks queue.
// Status and sites are a closed pair too: a shipped task owes at least one
// uniquely named site of a known kind, a planned task owes none. A declared
// company-context policy must be internally coherent.
func (c contract) validate() error {
	if len(c.Tiers) == 0 {
		return fmt.Errorf("contract declares no tiers")
	}
	tierSet := make(map[string]bool, len(c.Tiers))
	for _, t := range c.Tiers {
		tierSet[t] = true
	}
	if len(c.Tasks) == 0 {
		return fmt.Errorf("contract declares no tasks")
	}
	for name, def := range c.Tasks {
		if !taskNameRE.MatchString(name) {
			return fmt.Errorf("task %q: name must match %s", name, taskNameRE.String())
		}
		if len(def.Ladder) == 0 {
			return fmt.Errorf("task %q: ladder is empty", name)
		}
		for _, tier := range def.Ladder {
			if !tierSet[tier] {
				return fmt.Errorf("task %q: ladder names unknown tier %q", name, tier)
			}
		}
		switch def.OnBudgetExhausted {
		case "queue", "degrade":
		default:
			return fmt.Errorf("task %q: on_budget_exhausted must be \"queue\" or \"degrade\", got %q", name, def.OnBudgetExhausted)
		}
		switch def.ExecutionMode {
		case "interactive":
			if def.OnBudgetExhausted != "degrade" {
				return fmt.Errorf("task %q: interactive execution_mode requires on_budget_exhausted \"degrade\"", name)
			}
		case "background":
			if def.OnBudgetExhausted != "queue" {
				return fmt.Errorf("task %q: background execution_mode requires on_budget_exhausted \"queue\"", name)
			}
		default:
			return fmt.Errorf("task %q: execution_mode must be \"interactive\" or \"background\", got %q", name, def.ExecutionMode)
		}
		switch def.Status {
		case statusShipped:
			if len(def.Sites) == 0 {
				return fmt.Errorf("task %q: status shipped requires at least one site — a shipped task with no site cannot be certified, and would present as covered", name)
			}
		case statusPlanned:
			if len(def.Sites) > 0 {
				return fmt.Errorf("task %q: status planned declares %d site(s); a planned task has no implementation", name, len(def.Sites))
			}
		default:
			return fmt.Errorf("task %q: status must be %q or %q, got %q", name, statusShipped, statusPlanned, def.Status)
		}
		seenSite := make(map[string]bool, len(def.Sites))
		for _, s := range def.Sites {
			if !siteNameRE.MatchString(s.Name) {
				return fmt.Errorf("task %q: site name %q must match %s", name, s.Name, siteNameRE.String())
			}
			if seenSite[s.Name] {
				return fmt.Errorf("task %q: site %q is declared twice", name, s.Name)
			}
			seenSite[s.Name] = true
			if !siteKinds[s.Kind] {
				return fmt.Errorf("task %q: site %q has unknown kind %q", name, s.Name, s.Kind)
			}
		}
		if err := def.CompanyContext.validate(name); err != nil {
			return err
		}
	}
	for from, to := range c.DegradeTo {
		if !tierSet[from] {
			return fmt.Errorf("degrade_to: unknown tier %q", from)
		}
		if !tierSet[to] {
			return fmt.Errorf("degrade_to: tier %q degrades to unknown tier %q", from, to)
		}
	}
	return nil
}

// sortedTaskNames returns the contract's task names sorted, the single
// deterministic order every emitted const block, map literal, and
// AllTasks() walks — a map has no stable iteration order, so this is the
// one place that ordering is decided.
func (c contract) sortedTaskNames() []string {
	names := make([]string, 0, len(c.Tasks))
	for name := range c.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// pascalCase turns a snake_case contract name into the CamelCase suffix
// every Task/Tier constant uses (cert_judge -> CertJudge).
func pascalCase(snake string) string {
	var b strings.Builder
	for _, part := range strings.Split(snake, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

func taskConst(name string) string { return "Task" + pascalCase(name) }
func tierConst(name string) string { return "Tier" + pascalCase(name) }

// emitGo renders tasks_gen.go: the Task/Tier types and constants, then the
// routing tables and the declaration tables the census is built on. These are
// the tables compiled from the contract, so tasks.go and routing.go never
// hand-maintain them. The result is gofmt-clean, matching every other
// *_gen.go the repo checks in.
func emitGo(c contract, contractHash string) (string, error) {
	taskNames := c.sortedTaskNames()

	var b strings.Builder
	b.WriteString("// Code generated by tools/gen-aitasks from api/ai-tasks.yaml. DO NOT EDIT.\n\n")
	b.WriteString("package ai\n\n")

	b.WriteString("// Task names one V1 AI workload. Routing is over capability tiers per\n")
	b.WriteString("// task (ai-operational-spec §1.2); code never names a vendor.\n")
	b.WriteString("type Task string\n\n")

	b.WriteString(goConstBlockStart)
	for _, name := range taskNames {
		if doc := c.Tasks[name].Doc; doc != "" {
			fmt.Fprintf(&b, "\t// %s is %s\n", taskConst(name), doc)
		}
		fmt.Fprintf(&b, "\t%s Task = %q\n", taskConst(name), name)
	}
	b.WriteString(")\n\n")

	b.WriteString("// ExecutionMode distinguishes request-bound work from work carried by a\n")
	b.WriteString("// durable background job. Budget exhaustion degrades the former and\n")
	b.WriteString("// defers the latter.\n")
	b.WriteString("type ExecutionMode string\n\n")
	b.WriteString(goConstBlockStart)
	b.WriteString("\tExecutionModeInteractive ExecutionMode = \"interactive\"\n")
	b.WriteString("\tExecutionModeBackground  ExecutionMode = \"background\"\n")
	b.WriteString(")\n\n")

	b.WriteString("// Tier is a capability tier (§1.1); ai-routing.yaml binds each to a\n")
	b.WriteString("// provider+model per deployment.\n")
	b.WriteString("type Tier string\n\n")

	b.WriteString(goConstBlockStart)
	for _, name := range c.Tiers {
		fmt.Fprintf(&b, "\t%s Tier = %q\n", tierConst(name), name)
	}
	b.WriteString(")\n\n")

	b.WriteString("// TaskContractHash is the sha256 of api/ai-tasks.yaml at generation\n")
	b.WriteString("// time: a build fingerprint the cert runner can compare against a\n")
	b.WriteString("// freshly hashed contract file to catch a stale generated table.\n")
	fmt.Fprintf(&b, "const TaskContractHash = %q\n\n", contractHash)

	b.WriteString("// AllTasks returns every contract task, sorted — the completeness\n")
	b.WriteString("// check a certification run walks to prove it covers every routed\n")
	b.WriteString("// workload, not just the ones a test author remembered.\n")
	b.WriteString("func AllTasks() []Task {\n\treturn []Task{\n")
	for _, name := range taskNames {
		fmt.Fprintf(&b, "\t\t%s,\n", taskConst(name))
	}
	b.WriteString("\t}\n}\n\n")

	writeRoutingTables(&b, c, taskNames)
	writeDeclarationTables(&b, c, taskNames)

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", fmt.Errorf("formatting generated source: %w", err)
	}
	return string(formatted), nil
}

// writeRoutingTables appends the routing half of tasks_gen.go: the per-task
// ladder, the economy-mode degrade move, the execution-mode table, and the
// tier-name validation set.
//
// taskNames arrives already sorted, and the tier-keyed tables walk c.Tiers in
// declaration order, so every map literal is byte-stable across runs.
func writeRoutingTables(b *strings.Builder, c contract, taskNames []string) {
	b.WriteString("// taskLadders is the §1.2 routing table: primary tier first, then the\n")
	b.WriteString("// fallback rungs fired on provider error or schema-validation failure.\n")
	b.WriteString("var taskLadders = map[Task][]Tier{\n")
	for _, name := range taskNames {
		rungs := make([]string, len(c.Tasks[name].Ladder))
		for i, tier := range c.Tasks[name].Ladder {
			rungs[i] = tierConst(tier)
		}
		fmt.Fprintf(b, "\t%s: {%s},\n", taskConst(name), strings.Join(rungs, ", "))
	}
	b.WriteString("}\n\n")

	b.WriteString("// degradeTo is the one-tier-down move economy mode applies at 80–100%\n")
	b.WriteString("// budget utilization (§1.3).\n")
	b.WriteString("var degradeTo = map[Tier]Tier{\n")
	for _, from := range c.Tiers {
		to, ok := c.DegradeTo[from]
		if !ok {
			continue
		}
		fmt.Fprintf(b, "\t%s: %s,\n", tierConst(from), tierConst(to))
	}
	b.WriteString("}\n\n")

	b.WriteString("// taskExecutionModes is the scheduling contract compiled from\n")
	b.WriteString("// execution_mode. Every task is present by construction.\n")
	b.WriteString("var taskExecutionModes = map[Task]ExecutionMode{\n")
	for _, name := range taskNames {
		mode := "ExecutionMode" + pascalCase(c.Tasks[name].ExecutionMode)
		fmt.Fprintf(b, "\t%s: %s,\n", taskConst(name), mode)
	}
	b.WriteString("}\n\n")

	b.WriteString("// knownTiers is the routing config's tier-name validation set: the\n")
	b.WriteString("// contract is the one place tier names are declared, so LoadRoutingFile\n")
	b.WriteString("// rejects any name this set doesn't contain.\n")
	b.WriteString("var knownTiers = map[Tier]bool{\n")
	for _, name := range c.Tiers {
		fmt.Fprintf(b, "\t%s: true,\n", tierConst(name))
	}
	b.WriteString("}\n\n")
}
