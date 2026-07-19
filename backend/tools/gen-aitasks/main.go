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
	"encoding/json"
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

// taskDef is one tasks.<name> entry: the routing ladder, the
// execution mode, budget-exhaustion policy, and an optional doc string carried through
// to the generated constant's comment.
type taskDef struct {
	Ladder            []string `yaml:"ladder"`
	ExecutionMode     string   `yaml:"execution_mode"`
	OnBudgetExhausted string   `yaml:"on_budget_exhausted"`
	Doc               string   `yaml:"doc"`
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
	DegradeTo map[string]string  `yaml:"degrade_to"`
}

// taskNameRE is the contract's task-naming rule: lowercase snake_case,
// matching the Go identifier derivation (pascalCase) 1:1.
var taskNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// parseContract decodes and validates ai-tasks.yaml. Unknown keys are
// errors: a typo'd field would otherwise silently drop routing policy.
func parseContract(raw []byte) (contract, error) {
	var c contract
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return contract{}, fmt.Errorf("parsing contract: %w", err)
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

// emitGo renders tasks_gen.go: the Task/Tier types and constants, the
// routing ladders, the degrade-to map, the execution-mode table, and
// knownTiers — the one table compiled from the contract, so tasks.go and
// routing.go never hand-maintain it. The result is gofmt-clean, matching
// every other *_gen.go the repo checks in.
func emitGo(c contract, contractHash string) (string, error) {
	taskNames := c.sortedTaskNames()

	var b strings.Builder
	b.WriteString("// Code generated by tools/gen-aitasks from api/ai-tasks.yaml. DO NOT EDIT.\n\n")
	b.WriteString("package ai\n\n")

	b.WriteString("// Task names one V1 AI workload. Routing is over capability tiers per\n")
	b.WriteString("// task (ai-operational-spec §1.2); code never names a vendor.\n")
	b.WriteString("type Task string\n\n")

	b.WriteString("const (\n")
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
	b.WriteString("const (\n")
	b.WriteString("\tExecutionModeInteractive ExecutionMode = \"interactive\"\n")
	b.WriteString("\tExecutionModeBackground  ExecutionMode = \"background\"\n")
	b.WriteString(")\n\n")

	b.WriteString("// Tier is a capability tier (§1.1); ai-routing.yaml binds each to a\n")
	b.WriteString("// provider+model per deployment.\n")
	b.WriteString("type Tier string\n\n")

	b.WriteString("const (\n")
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

	b.WriteString("// taskLadders is the §1.2 routing table: primary tier first, then the\n")
	b.WriteString("// fallback rungs fired on provider error or schema-validation failure.\n")
	b.WriteString("var taskLadders = map[Task][]Tier{\n")
	for _, name := range taskNames {
		rungs := make([]string, len(c.Tasks[name].Ladder))
		for i, tier := range c.Tasks[name].Ladder {
			rungs[i] = tierConst(tier)
		}
		fmt.Fprintf(&b, "\t%s: {%s},\n", taskConst(name), strings.Join(rungs, ", "))
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
		fmt.Fprintf(&b, "\t%s: %s,\n", tierConst(from), tierConst(to))
	}
	b.WriteString("}\n\n")

	b.WriteString("// taskExecutionModes is the scheduling contract compiled from\n")
	b.WriteString("// execution_mode. Every task is present by construction.\n")
	b.WriteString("var taskExecutionModes = map[Task]ExecutionMode{\n")
	for _, name := range taskNames {
		mode := "ExecutionMode" + pascalCase(c.Tasks[name].ExecutionMode)
		fmt.Fprintf(&b, "\t%s: %s,\n", taskConst(name), mode)
	}
	b.WriteString("}\n\n")

	b.WriteString("// knownTiers is the routing config's tier-name validation set: the\n")
	b.WriteString("// contract is the one place tier names are declared, so LoadRoutingFile\n")
	b.WriteString("// rejects any name this set doesn't contain.\n")
	b.WriteString("var knownTiers = map[Tier]bool{\n")
	for _, name := range c.Tiers {
		fmt.Fprintf(&b, "\t%s: true,\n", tierConst(name))
	}
	b.WriteString("}\n")

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", fmt.Errorf("formatting generated source: %w", err)
	}
	return string(formatted), nil
}

// schemaTemplate mirrors config/ai-routing.schema.json's structure
// exactly; %s is the tier enum (propertyNames.enum), the one part the
// contract drives. Keeping this as a literal template rather than
// round-tripping through encoding/json preserves the file's hand-tuned
// key order and formatting byte-for-byte across regenerations.
const schemaTemplate = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://margince.dev/schema/ai-routing.json",
  "title": "Margince AI routing config",
  "$comment": "GENERATED by tools/gen-aitasks from backend/api/ai-tasks.yaml — do not edit",
  "type": "object",
  "additionalProperties": false,
  "required": ["profile", "tiers", "embeddings"],
  "properties": {
    "profile": {
      "description": "Location ladder: eu_hosted (partner EU inference), sovereign (zero egress — cloud providers refused), cloud_frontier (BYOK cloud).",
      "enum": ["eu_hosted", "sovereign", "cloud_frontier"]
    },
    "tiers": {
      "description": "Capability tiers; bind each to one provider. An unbound tier is legal — the router degrades honestly.",
      "type": "object",
      "minProperties": 1,
      "additionalProperties": false,
      "propertyNames": { "enum": [%s] },
      "patternProperties": { ".*": { "$ref": "#/$defs/binding" } }
    },
    "embeddings": {
      "description": "The embedding lane, bound separately from chat (retrieval must survive a chat-budget exhaustion). Required.",
      "$ref": "#/$defs/binding"
    }
  },
  "$defs": {
    "binding": {
      "type": "object",
      "additionalProperties": false,
      "required": ["provider"],
      "properties": {
        "provider": {
          "description": "fake | anthropic | ollama | vllm | openai_compatible | openai | gemini. The only place vendor names appear.",
          "enum": ["fake", "anthropic", "ollama", "vllm", "openai_compatible", "openai", "gemini"]
        },
        "model":    { "type": "string", "description": "Provider-native model id. ollama/vllm default to a Gemma-class model when omitted (A23)." },
        "base_url": { "type": "string", "description": "Endpoint override. REQUIRED for openai_compatible (the vendor host root, NO /v1). Empty ⇒ provider default." }
      },
      "allOf": [
        {
          "if":   { "properties": { "provider": { "const": "openai_compatible" } } },
          "then": { "required": ["base_url"] }
        }
      ]
    }
  }
}
`

// emitSchema renders config/ai-routing.schema.json with its tier enum
// sourced from the contract's tiers list, in contract order. The result
// is validated as JSON before it is returned: a template substitution
// bug must fail generation, not ship a broken schema.
func emitSchema(tiers []string) (string, error) {
	quoted := make([]string, len(tiers))
	for i, t := range tiers {
		quoted[i] = fmt.Sprintf("%q", t)
	}
	schema := fmt.Sprintf(schemaTemplate, strings.Join(quoted, ", "))

	var v any
	if err := json.Unmarshal([]byte(schema), &v); err != nil {
		return "", fmt.Errorf("generated schema is not valid JSON: %w", err)
	}
	return schema, nil
}
