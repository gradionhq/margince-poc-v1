package gate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gradionhq/margince/cli/craft/rubric"
)

// Inputs is everything the reviewer reads for one PR. The full touched-file
// content (not just hunks) and the sibling files give the agent the style
// baseline it judges drift against (T5).
type Inputs struct {
	Diff          string
	TouchedFiles  map[string]string // path -> full current content
	SiblingFiles  map[string]string // path -> content, for the surrounding-style baseline
	ModuleAGENTS  string            // the touched module's AGENTS.md ## Craftsmanship deltas
	InterfacesDoc string            // interfaces.md §0 — the error-sentinel registry
	Exemplars     string            // pinned few-shot exemplars (B-EP11.8a); empty until seeded
}

// buildPrompt renders the review prompt: the procedure, the rubric, the PR
// context, and the required output shape. It is deterministic in its inputs so
// the same (prompt, rubric, exemplars, model) tuple reproduces a verdict.
func buildPrompt(r *rubric.Rubric, in Inputs) string {
	var b strings.Builder

	b.WriteString(`You are the Margince craftsmanship reviewer. You judge whether a change reads as a senior human engineer's work — idiom, restraint, taste — NOT whether it compiles or passes tests (separate gates already proved that). Review only the diff; use the full files and siblings as the style baseline.

## Procedure
1. Read the PR diff and, for each touched file, its full current content.
2. For each hunk, compare it to the surrounding code and the sibling files. Identify any anti-tell below.
3. Verify the honest-edge-case dimension (T7): does the change drop the empty list, the timezone boundary, the concurrent write, the cross-tenant case?
4. Classify every issue by category, severity, and confidence.

## Severity (calibrated — this is why a no-override block is safe)
- BLOCKER: a clear, objectively-statable instance of a block-eligible anti-tell. Only use when you are certain.
- MAJOR: a plausible craft issue you are less than certain about, or a stylistic call that could go either way.
- MINOR: a nit or subjective polish.
Confidence is your own certainty: high | medium | low. A BLOCKER you are not "high" on must be MAJOR.

## Rubric (the standard — version `)
	b.WriteString(r.Version)
	b.WriteString("`)\n")
	b.WriteString("Meta-rule: ")
	b.WriteString(r.MetaRule)
	b.WriteString("\n\n")
	writeRules(&b, r)

	b.WriteString("\n## PR diff\n```diff\n")
	b.WriteString(in.Diff)
	b.WriteString("\n```\n")

	writeFiles(&b, "Full content of touched files", in.TouchedFiles)
	writeFiles(&b, "Sibling files (style baseline)", in.SiblingFiles)
	writeSection(&b, "Module AGENTS.md (## Craftsmanship deltas)", in.ModuleAGENTS)
	writeSection(&b, "interfaces.md §0 (error sentinels)", in.InterfacesDoc)
	writeSection(&b, "Pinned exemplars", in.Exemplars)

	b.WriteString(`
## Output
Return ONLY a JSON object, no prose around it:
{
  "scratchpad": "<your hunk-by-hunk reasoning>",
  "verdict": "PASS",
  "findings": [
    {"id":"f1","file":"path","line":42,"category":"<rubric category>","severity":"BLOCKER|MAJOR|MINOR","confidence":"high|medium|low","rationale":"why","suggested_fix":"what to change"}
  ]
}
Set "verdict" to your overall read, but know the merge-blocking verdict is recomputed from findings in code — only a BLOCKER at high confidence in a block-eligible category blocks. Use the exact rubric category ids/names below.`)

	return b.String()
}

func writeRules(b *strings.Builder, r *rubric.Rubric) {
	anti := r.Rules[:0:0]
	pos := r.Rules[:0:0]
	for _, rule := range r.Rules {
		if rule.Kind == rubric.KindAntiTell {
			anti = append(anti, rule)
		} else {
			pos = append(pos, rule)
		}
	}
	b.WriteString("### Anti-tells (block-eligible categories)\n")
	for _, rule := range anti {
		fmt.Fprintf(b, "- [%s] category=`%s`: %s\n", rule.ID, rule.Category, rule.Rule)
	}
	b.WriteString("\n### Positive rubric (never blocks; informs MAJOR/MINOR only)\n")
	for _, rule := range pos {
		fmt.Fprintf(b, "- [%s] %s: %s\n", rule.ID, rule.Title, rule.Rule)
	}
}

func writeFiles(b *strings.Builder, heading string, files map[string]string) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n", heading)
	for _, path := range sortedKeys(files) {
		fmt.Fprintf(b, "\n### %s\n```\n%s\n```\n", path, files[path])
	}
}

func writeSection(b *strings.Builder, heading, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, "\n## %s\n%s\n", heading, body)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
