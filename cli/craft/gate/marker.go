package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CRAFT-FIX / CRAFT-DISPUTE markers are the closed-loop's on-disk medium. The
// annotator turns each blocking finding into a greppable, line-anchored marker;
// the residue gate (B-EP11.5) fails the merge while any remain; the local agent
// fixes the code and deletes the marker (or replaces it with a CRAFT-DISPUTE,
// B-EP11.7). The markers are deliberately idiomatic to the existing
// CONTROL:/JURISDICTION: source-marker family.

const (
	markerFix     = "CRAFT-FIX"
	markerDispute = "CRAFT-DISPUTE"
	lineComment   = "// " // line-comment prefix for Go/TS/JS and the default
)

// MarkerKind is which side of the loop a marker represents.
type MarkerKind string

const (
	// KindFix is a CRAFT-FIX marker: a blocking finding the agent must resolve.
	KindFix MarkerKind = markerFix
	// KindDispute is a CRAFT-DISPUTE marker: a finding the agent is contesting.
	KindDispute MarkerKind = markerDispute
)

// Marker is a parsed in-source marker recovered by the collector.
type Marker struct {
	Kind   MarkerKind
	ID     string
	File   string
	Line   int
	Reason string // CRAFT-DISPUTE only: the agent's contesting rationale
}

var (
	reFix     = regexp.MustCompile(`CRAFT-FIX\[([^\]]+)\]`)
	reDispute = regexp.MustCompile(`CRAFT-DISPUTE\[([^\]]+)\]:?\s*(.*)`)
)

// commentDelims returns the (prefix, suffix) for a craftsmanship marker in a file
// of this type. Line-comment languages use a prefix; SQL/CSS use a block comment.
func commentDelims(path string) (string, string) {
	switch filepath.Ext(path) {
	case ".sql":
		return "-- ", ""
	case ".css", ".scss":
		return "/* ", " */"
	default: // .go .ts .tsx .js .jsx and the rest
		return lineComment, ""
	}
}

// Annotate writes a CRAFT-FIX marker above the target line of each blocking
// finding, in the file's comment style, preserving the target line's indentation.
// Findings are applied bottom-up per file so earlier insertions don't shift later
// line numbers. It is deterministic: the same findings produce the same tree.
func Annotate(root string, blocking []Finding) error {
	byFile := map[string][]Finding{}
	for _, f := range blocking {
		byFile[f.File] = append(byFile[f.File], f)
	}
	for file, fs := range byFile {
		if err := annotateFile(root, file, fs); err != nil {
			return err
		}
	}
	return nil
}

func annotateFile(root, file string, fs []Finding) error {
	full := filepath.Join(root, file)
	content, err := os.ReadFile(full) //nolint:gosec // G304: reads a repo source file the gate is annotating, path under root
	if err != nil {
		return fmt.Errorf("annotate %s: %w", file, err)
	}
	lines := strings.Split(string(content), "\n")

	// Bottom-up so insertions above don't shift the lines below them.
	sort.Slice(fs, func(i, j int) bool { return fs[i].Line > fs[j].Line })
	prefix, suffix := commentDelims(file)
	for _, f := range fs {
		idx := f.Line - 1
		if idx < 0 || idx > len(lines) {
			return fmt.Errorf("annotate %s: finding %s line %d out of range", file, f.ID, f.Line)
		}
		marker := prefix + markerText(f) + suffix
		marker = leadingIndent(lines, idx) + marker
		lines = append(lines[:idx], append([]string{marker}, lines[idx:]...)...)
	}
	return os.WriteFile(full, []byte(strings.Join(lines, "\n")), 0o644) //nolint:gosec // G306: source files in the repo tree are world-readable by design
}

// markerText is the single-line, greppable body carrying the originating finding
// id plus the WHY and FIX the agent must act on.
func markerText(f Finding) string {
	return fmt.Sprintf("%s[%s] %s (%s/%s): %s | FIX: %s",
		markerFix, f.ID, f.Category, f.Severity, f.Confidence, f.Rationale, f.SuggestedFix)
}

// leadingIndent copies the whitespace of the line the marker sits above so the
// marker lines up with the code it annotates.
func leadingIndent(lines []string, idx int) string {
	if idx >= len(lines) {
		return ""
	}
	line := lines[idx]
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// Collect scans the tree for CRAFT-FIX and CRAFT-DISPUTE markers and returns them
// with their file, line, recovered id, and (for disputes) the contesting reason.
// It skips .git, node_modules, .swarm-worktrees, and non-source files so it
// never trips on the markers documented in prose (e.g. specs/quality/craftsmanship.md,
// AGENTS.md) nor on a sibling swarm worktree's in-progress markers when one is
// (against convention) nested inside the trunk. Any relative-path prefix in skip
// is also ignored — the residue gate passes the craft tool's own dir, whose
// source legitimately contains the marker tokens.
func Collect(root string, skip ...string) ([]Marker, error) {
	var markers []Marker
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".swarm-worktrees" || hasPrefix(rel, skip) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSource(d.Name()) || hasPrefix(rel, skip) {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // G304: path comes from WalkDir over the repo tree, not user input
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if m, ok := parseMarker(line); ok {
				m.File, m.Line = rel, i+1
				markers = append(markers, m)
			}
		}
		return nil
	})
	return markers, err
}

func hasPrefix(rel string, prefixes []string) bool {
	for _, p := range prefixes {
		if rel == p || strings.HasPrefix(rel, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// parseMarker recovers a marker from a line. CRAFT-DISPUTE is checked first
// because a dispute line also contains the CRAFT- prefix family.
func parseMarker(line string) (Marker, bool) {
	if m := reDispute.FindStringSubmatch(line); m != nil {
		return Marker{Kind: KindDispute, ID: m[1], Reason: strings.TrimSpace(m[2])}, true
	}
	if m := reFix.FindStringSubmatch(line); m != nil {
		return Marker{Kind: KindFix, ID: m[1]}, true
	}
	return Marker{}, false
}
