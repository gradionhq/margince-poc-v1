// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// `relationship` is a first-class RBAC object, and it is the only join table in
// the schema that is one. The reason is the whole obligation this gate holds:
// an edge discloses the two records it names AS A PAIR, which the grants on
// those two records do not cover. "Who works at Acme" is a fact about the pair.
//
// So every read of that table either passes the edge's own gate, or says which
// kind of read it is that does not need to. This gate asks for that verdict and
// fails a read carrying neither — because a read that quietly answers under the
// endpoints' grants is indistinguishable from one that was considered, and the
// tree has now produced nine of the first kind while a tenth sat one directory
// away doing it correctly.
//
// Why the gate is the fix rather than the helper. platform/auth.EdgeReadScope
// spells the admission once, but nothing forces a new SQL read through it — the
// tree's own precedent is explicit that one spelling holds because a gate
// enforces reaching it (RelationshipEndpointScope's "one spelling" comment, and
// composerowscope_test.go enforcing it). This is that gate.
//
// What it asks, in three derived steps:
//
//   - the SITES are every SQL string literal under internal/ naming the
//     relationship table, resolved from the literals themselves, so a new read
//     inherits the obligation with no edit here;
//   - the OBLIGATION is that the enclosing FUNCTION reaches an edge-gate
//     spelling, resolved transitively across its package;
//   - anything else carries a VERDICT in one of the three declarations below,
//     and which declaration a site sits in IS its verdict.
//
// Per FUNCTION and not per file, which is where it parts company with
// restrictedreaders_test.go's otherwise identical shape. That gate judges a
// whole file because its subject is one obligation every read in the file
// shares; here one file legitimately holds reads with DIFFERENT verdicts —
// org360/graphreads.go gates two and defers the third — and a file-level answer
// would let the gated pair vouch for the deferred one, which is precisely the
// hole being closed. A package-level SQL fragment has no function to belong to
// and is judged at file scope, as it is there.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// relationshipReadLiteral matches a SQL string literal that reads the
// relationship table by name.
//
// The trailing alternation ends the match on whitespace, a newline, the end of
// the text, or a delimiter, and the match is run against the literal's TEXT
// rather than its quoted token. Both halves matter and each was got wrong once:
//
//   - a pattern requiring a trailing SPACE misses every line ending in
//     `FROM relationship`, which is how this change's own first census
//     undercounted its subject by five sites;
//   - matching against ast.BasicLit.Value leaves the closing backquote in the
//     text, so `$` can never fire and a literal ENDING at `FROM relationship`
//     is invisible — the same class of miss, one layer down, and it survived
//     the first mutation drill because that probe ended the line rather than
//     the literal.
//
// literalText below is what strips the quoting. restrictedreaders_test.go
// carries the same expression and had the same second flaw; it is fixed there
// in this change, because one spelling of a rule with two copies is not a rule
// (review-loop rule 1).
var relationshipReadLiteral = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+relationship(\s|$|[,;)])`)

// edgeLiteralText is contentionprobe_test.go's literalText, narrowed to the
// one shape this gate asks about. Shared rather than respelled: it unquotes
// through strconv, which is stricter than trimming the delimiters by hand.
func edgeLiteralText(lit *ast.BasicLit) string {
	text, isString := literalText(lit)
	if !isString {
		return ""
	}
	return text
}

// edgeGateSeeds are the spellings that ARE the edge's read admission, anywhere.
// Only EdgeReadScope qualifies: it is the one that takes the object gate.
var edgeGateSeeds = []string{"EdgeReadScope"}

// rowHalfSeeds are the row-scope spellings. They bound WHICH edges, and answer
// nothing about whether the caller may read edges at all — so on their own they
// are the INVERTED form of the defect this gate exists to catch, and a compose
// read taking the conjunction without the gate must not read green.
//
// They still satisfy inside the packages that own the object's own surface,
// where the object gate is asked at the store entry point and these are the
// clause that entry point composes.
var rowHalfSeeds = []string{"RelationshipEndpointScope", "EnsureRelationshipVisible"}

// rowHalfOwners are the packages where a row-half spelling alone is enough:
// people owns the relationship surface and gates every store entry point on it,
// and auth is where the gate itself lives.
var rowHalfOwners = []string{"internal/modules/people", "internal/platform/auth"}

// requireCall matches a call that asks the object gate under any of this
// tree's spellings — auth.Require, or a package-local wrapper such as
// person360's requireRead. Paired with the object name in the same function
// body, it is the older form of the admission and still counts.
var requireCall = regexp.MustCompile(`^[Rr]equire[A-Za-z]*$`)

// predicateEdgeReads: the edge appears only inside a JOIN or EXISTS that
// selects or routes records the caller is separately gated on, and nothing
// about the edge reaches the response. The test that puts a read here rather
// than in the gated set: removing the edge condition could only WIDEN the
// result the caller already sees.
//
// The discriminator matters because the obvious wording of it is wrong. "The
// edge's columns are not in the select list" would file the employer filter on
// the people list here — and filtering a list by employer answers "who works at
// Acme" one page at a time, which is a STRONGER disclosure than the count on
// the account. It carries the gate, and is not in this set.
var predicateEdgeReads = gatekit.Waive(map[string]string{
	"internal/modules/activities/orgscope.go":  "the employment arm of the org row-scope walk, which is the machinery that ANSWERS what a caller may see — gating it on a grant would be circular, and the arm only ever widens the set of activities an org reaches. The cost is that this walk reaches an edge no grant was checked for, on a path whose whole output is a visibility predicate",
	"internal/modules/activities/lasttouch.go": "the stakeholder arm of the cold-queue selector: the edge decides which QUEUE ENTRIES are stale enough to surface, and no edge column or endpoint pair reaches the caller. Removing the arm would widen the queue, never narrow it. The cost is that a queue entry's presence weakly reflects that some seat exists on some open deal",
	"internal/modules/people/demote.go":        "the guard that refuses demoting an organization still carrying live edges — an existence test whose answer is the refusal itself, on a write the caller is already gated for. The cost is that a refused demote confirms edges exist, which the write grant already implies",
	"internal/modules/people/dedupe.go":        "the duplicate-candidate scan reads the employer edge to SCORE a pair of person records the caller can already read both of; the score reaches the caller, the edge does not. The cost is that a high match score weakly reflects a shared employer",
})

// lifecycleEdgeReads: cascades, merges, lawful-processing sweeps, capture and
// enrichment resolution, seeders. Each is gated by the WRITE it belongs to, or
// runs as PrincipalSystem — which auth.Require short-circuits outright
// (platform/auth/rbac.go), so the gate would admit them anyway and the entry
// records why asking was never the point.
//
// The privacy readers are the load-bearing ones. A retention or erasure sweep
// that respected a caller's grants would UNDER-DELETE, which is a worse defect
// than the disclosure this change fixes: the sweep's correctness depends on
// seeing every edge, and it runs as the system principal precisely so it does.
var lifecycleEdgeReads = gatekit.Waive(map[string]string{
	"internal/modules/privacy/sar.go":                                      "the subject-access export must enumerate every edge naming the data subject, or the export it produces is incomplete — a lawful-processing defect. Runs as the system principal on a request a human already authorised. The cost is that this path reads every edge of one subject with no per-caller gate",
	"internal/modules/privacy/erasure_graph.go":                            "the erasure walk must reach every edge naming the subject or it leaves data behind that was ordered deleted. Respecting a caller's grants here would under-delete. The cost is unlimited edge reach for the sweep, bounded by its system principal",
	"internal/modules/privacy/retention_graph.go":                          "the retention walk decides what a hold KEEPS, and an edge it cannot see is an edge it cannot preserve — under-retention breaks the statutory floor as surely as under-deletion breaks erasure. The cost is unlimited edge reach for the sweep",
	"internal/modules/privacy/retentionselectors.go":                       "the selector excludes rows still anchored by a live edge, so an edge it cannot see would be deleted while still referenced. The cost is unlimited edge reach in a predicate whose output is a delete bound",
	"internal/modules/people/relationship.go:lockPersonForEmployment":      "the row lock a stakeholder or employment write takes before it changes the edge, called only from CreateRelationship and UpdateRelationship, both of which ask the relationship create and update grants at their own entry. The cost is that the lock trusts its callers rather than re-asking",
	"internal/modules/people/merge.go":                                     "the person merge re-points and archives the loser's edges inside the merge write, gated by that write's own grant. The cost is that merge reaches edges the caller could not have listed",
	"internal/modules/people/merge_organization.go":                        "the organization merge, same shape and same write grant. The cost is the same",
	"internal/modules/people/employmentedge.go":                            "the shared employment upsert, gated by relationship create at its callers. The cost is none beyond that gate",
	"internal/modules/people/projectstakeholder.go:projectStakeholderEdge": "the private lookup of the seat a write is about to create or remove, called only from SetProjectStakeholder and RemoveProjectStakeholder, both of which ask the relationship create and delete grants at their own entry. The cost is that the helper trusts its two callers rather than re-asking",
	"internal/modules/people/domaintriageresolve.go":                       "domain triage promotes a captured person to an employment edge as part of resolving a triage decision — a write gated by its own grant, whose read checks whether the edge it is about to make already exists. The cost is that triage confirms an edge exists before creating it",
	"internal/modules/people/enrichment.go":                                "the enrichment writer reads the employer edge to decide which organization a fetched fact belongs to, on a path that runs as a system principal from the enrichment job. The cost is that enrichment resolves an employer with no per-caller gate",
	"internal/modules/people/sitepersonfields.go":                          "site-derived person fields are matched to the employer edge during capture resolution, as a system principal, before any human reads the result. The cost is the same as enrichment's",
	"internal/modules/people/linkedinmatch.go":                             "profile matching resolves a captured profile against the employer edge during capture, as a system principal. The cost is the same as enrichment's",
	"internal/modules/people/orgnamepromotion.go":                          "organization-name promotion reads the employment edges it is about to re-point when a captured company name is promoted to a record — a write path running as a system principal. The cost is the same as enrichment's",
	"internal/modules/signals/resolver.go":                                 "signal resolution matches an inbound signal to an organization through the employment edge, as a system principal on the capture path, before any human is shown the signal. The cost is that resolution reads edges with no per-caller gate",
	"internal/compose/personautoenrich.go":                                 "the auto-enrich pass resolves a person's current primary employer to decide which company site may describe them, under an explicit PrincipalSystem actor it binds itself. The cost is that the pass reads one employer edge per candidate with no per-caller gate",
	"internal/compose/captureofflinedemo.go":                               "the offline capture demo seeds a directory of people to write to from an account's edges; it is a development seeder, never a served read. The cost is none — it reaches no caller",
})

// deferredEdgeReads: a DISCLOSING read that is still ungated, each naming the
// issue that will close it.
//
// It is a real verdict and not a quiet exclusion, which is the distinction
// #1831 drew when it refused a bare exemption list: a bare list reads as
// "handled", this reads as "decided, and not yet done". The hole stays
// countable — `go test -run TestEveryReaderOfTheRelationshipTable -v` prints
// the count.
//
// Every reason here names its issue. That is a convention rather than a checked
// rule, deliberately: a parallel map of reasons to police it is exactly what
// TestEveryPackageLevelReasonMapIsAWaiverOrADeclaredFixture refuses, because a
// gate holding its own exceptions to its own standard is how the standards
// diverge. gatekit already refuses a reason that states no cost.
//
// Every entry here shares one reason, and it is not schedule. Each of these
// reads feeds a SCORE or a VERDICT on a response that carries no channel for
// saying a section was withheld, so gating it today would replace a disclosure
// with a wrong number — an empty risk list renders "Nothing flagged — this deal
// passes every coverage check", which is worse than the defect. The contract
// work comes first.
var deferredEdgeReads = gatekit.Waive(map[string]string{
	"internal/compose/org360/graphreads.go:readRelatedOrganizations": "the partner/referral/co-sell edges on the related-companies card: crm.yaml states these organizations need no grant beyond the organization read the endpoint already demands and can never be withheld wholesale, and groups_omitted's enum has no value that could name them. Gating it is a contract change and a product ruling, not a defect fix — #1846 follow-up, needs-decision",
	"internal/compose/network/coveragefacts.go:readDeparted":         "departed stakeholders become champion_left and stakeholder_left risks, and DealCoverage carries no withheld channel — an empty risk list renders as a clean coverage verdict, so gating this trades a disclosure for a false all-clear on deal risk. Blocked on the withheld channel — #1846 follow-up",
	"internal/modules/deals/engagement.go:EngagedStakeholders":       "the engagement set feeds the same DealCoverage payload and the health composite; withholding it silently lowers a score rather than absenting it. Blocked on the same withheld channel — #1846 follow-up",
	"internal/modules/deals/engagement.go:Stakeholders":              "the seat list on the coverage payload, whose stakeholders field is required with no withheld channel; an empty list reads as an uncovered deal rather than a withheld one. Blocked on the same withheld channel — #1846 follow-up",
	"internal/modules/deals/health.go:healthActivityEvidence":        "the health composite's engagement factor: a factor computed from edges the caller may not read yields a WRONG score, not a withheld one, and the health payload has no channel to say so. Blocked on the same withheld channel — #1846 follow-up",
})

// wantMinimumGatedSites is the floor below the count of sites that satisfy the
// gate today (twenty, across compose, the modules that read edges, and the
// module that owns the object's own surface).
//
// It exists for the reason composerowscope_test.go's equivalent does: an
// extractor that stops recognising SQL finds no sites and reports nothing,
// which is indistinguishable from a clean tree. The floor sits below the true
// count rather than on it, so removing one read stays an ordinary change and
// only a collapse is a finding.
const wantMinimumGatedSites = 12

// edgeReaderScope is every non-test, non-generated file under internal/ that
// reads the relationship table by name.
var edgeReaderScope = gatekit.Scope{
	Roots:   []string{"internal"},
	Subject: readsRelationshipTable,
	Exempt:  gatekit.Waive(map[string]string{}),
}

func readsRelationshipTable(filePath string, file *ast.File) bool {
	if strings.HasSuffix(filePath, "_gen.go") {
		return false
	}
	return holdsRelationshipLiteral(file)
}

func holdsRelationshipLiteral(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && relationshipReadLiteral.MatchString(edgeLiteralText(lit)) {
			found = true
		}
		return !found
	})
	return found
}

func TestEveryReaderOfTheRelationshipTableCarriesTheEdgeGateOrAVerdict(t *testing.T) {
	files := edgeReaderScope.Files(t)
	gated := gatedFunctionsByPackage(t, files)

	var satisfied int
	for _, parsed := range files {
		pkg := path.Dir(parsed.Path)
		for _, site := range relationshipReadSites(parsed) {
			subject := parsed.Path
			if site.function != "" {
				subject += ":" + site.function
			}
			// Its own body first, then the helpers IT CALLS — never its own
			// name. A by-name lookup would let a gated Store.X vouch for an
			// ungated Handlers.X, which is the vouching this gate is
			// per-function precisely to prevent.
			carriesGate := site.holdsGate || callsAGatedHelper(site.calls, gated[pkg])
			if site.function == "" {
				carriesGate = site.holdsGate || fileHoldsAGatedFunction(t, parsed, gated[pkg])
			}
			verdict := verdictFor(t, subject)

			switch {
			case carriesGate && verdict != "":
				// A satisfied site that is ALSO waived is the way this census
				// silently stops counting down: a deferred read that later gets
				// its gate keeps matching its waiver for ever, and the hole
				// reads as open when it is closed.
				t.Errorf("%s carries the edge gate AND a %s verdict — remove the verdict, it now "+
					"describes code that is gated", subject, verdict)
			case carriesGate:
				satisfied++
			case verdict == "":
				t.Errorf("%s reads the relationship table without the edge gate and without a verdict.\n"+
					"  Reading an edge discloses its endpoints AS A PAIR, which is what relationship.read "+
					"governs — the endpoints' own grants do not cover it.\n"+
					"  Either call auth.EdgeReadScope, or declare the read in predicateEdgeReads / "+
					"lifecycleEdgeReads / deferredEdgeReads with the reason it needs no gate.\n"+
					"  Left alone it is indistinguishable from a read nobody considered.\n"+
					"  The read: %s", subject, site.sql)
			}
		}
	}

	if satisfied < wantMinimumGatedSites {
		t.Errorf("only %d relationship reads satisfy the edge gate, want at least %d — a literal "+
			"extractor that stopped recognising this tree's SQL would report exactly this, and it "+
			"reads the same as a clean tree", satisfied, wantMinimumGatedSites)
	}
	t.Logf("edge reads: %d gated, %d predicate, %d lifecycle, %d DEFERRED (still disclosing)",
		satisfied, len(predicateEdgeReads.Subjects()), len(lifecycleEdgeReads.Subjects()),
		len(deferredEdgeReads.Subjects()))

	predicateEdgeReads.AssertAllMatched(t)
	lifecycleEdgeReads.AssertAllMatched(t)
	deferredEdgeReads.AssertAllMatched(t)
}

// verdictFor names the declaration a subject sits in, and refuses one sitting
// in two: the verdict IS the declaration, so a subject with two of them has no
// verdict at all.
func verdictFor(t *testing.T, subject string) string {
	t.Helper()
	var found []string
	for _, set := range []struct {
		name    string
		waivers *gatekit.Waivers[string]
	}{
		{"predicate", predicateEdgeReads},
		{"lifecycle", lifecycleEdgeReads},
		{"deferred", deferredEdgeReads},
	} {
		// A file-keyed verdict answers for every site in the file; a
		// function-keyed one answers for its own site only. Both are asked,
		// because a lifecycle FILE and a deferred FUNCTION are both real shapes.
		if set.waivers.Waived(t, subject) || set.waivers.Waived(t, fileOf(subject)) {
			found = append(found, set.name)
		}
	}
	if len(found) > 1 {
		t.Errorf("%s carries %s verdicts at once: which declaration a read sits in IS its verdict, "+
			"so two of them is none", subject, strings.Join(found, " and "))
		return ""
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

func fileOf(subject string) string {
	if idx := strings.LastIndex(subject, ":"); idx >= 0 {
		return subject[:idx]
	}
	return subject
}

// site is one relationship read: the function that holds it, empty for a
// package-level SQL fragment, the first line of the SQL for the report, and
// whether that function's OWN body takes the admission.
//
// holdsGate is answered from the declaration itself rather than by looking the
// function up by name, and that distinction is the whole reason this field
// exists. *Store and Handlers in one module routinely spell the same method
// names — people has both a Store.RemoveProjectStakeholder and a
// Handlers.RemoveProjectStakeholder — so a by-name index lets one answer for
// the other, and which one wins is Go map iteration order. rbacgate_test.go
// says so in its own header, having been bitten by exactly this. The name index
// below is still used, but only to reach a gate in a SIBLING function, where a
// collision can merely be optimistic rather than wrong.
type site struct {
	function  string
	sql       string
	holdsGate bool
	// calls is what this declaration calls, so a gate reached through a helper
	// in a sibling file resolves without consulting this declaration's name.
	calls map[string]bool
}

func callsAGatedHelper(calls map[string]bool, gated map[string]bool) bool {
	for name := range calls {
		if gated[name] {
			return true
		}
	}
	return false
}

func pkgOf(filePath string) string { return path.Dir(filePath) }

func relationshipReadSites(parsed gatekit.ParsedFile) []site {
	var sites []site
	for _, decl := range parsed.File.Decls {
		var reads []string
		ast.Inspect(decl, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && relationshipReadLiteral.MatchString(edgeLiteralText(lit)) {
				reads = append(reads, edgeLiteralText(lit))
			}
			return true
		})
		if len(reads) == 0 {
			continue
		}
		name := ""
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
			name = fn.Name.Name
		}
		refs := referencesIn(decl)
		sites = append(sites, site{
			function: name, sql: firstSQLLine(reads[0]),
			holdsGate: holdsSeedGate(refs, pkgOf(parsed.Path)), calls: refs.calls,
		})
	}
	return sites
}

// gatedFunctionsByPackage resolves, per package directory, every function that
// reaches an edge-gate spelling — directly, or through another function in the
// same package. Transitive because this tree routinely splits a read across the
// function holding the SQL and the helper building its predicate (org360's
// edgeScope, meetingbrief's seatJoinPredicate), and a gate asking only about
// direct calls would report the reader red while its admission sits in the file
// next door.
func gatedFunctionsByPackage(t *testing.T, files []gatekit.ParsedFile) map[string]map[string]bool {
	t.Helper()
	// The WHOLE package, not only the files that read the table. The admission
	// this tree writes routinely lives in a sibling file that holds no SQL of
	// its own — org360's edgeScope sits in sections.go while the three reads it
	// gates are in graphreads.go and contacts.go — so a resolution seeded from
	// the subject files alone reports gated code as ungated, which costs the
	// gate its credibility faster than a miss does.
	bodies := map[string]map[string][]references{}
	for _, parsed := range files {
		pkg := path.Dir(parsed.Path)
		if bodies[pkg] != nil {
			continue
		}
		bodies[pkg] = packageFunctionReferences(t, pkg)
	}

	// A name is gated when ANY declaration spelling it is — a union, never an
	// overwrite. Optimistic where two receivers share a method name, and
	// deliberately so: this index only ever answers "is there a gated helper
	// called X in this package", and the site's own declaration has already
	// been asked directly, so the optimism cannot excuse an ungated read whose
	// same-named neighbour happens to be gated.
	gated := map[string]map[string]bool{}
	for pkg, funcs := range bodies {
		gated[pkg] = map[string]bool{}
		for name, decls := range funcs {
			for _, refs := range decls {
				if holdsSeedGate(refs, pkg) {
					gated[pkg][name] = true
					break
				}
			}
		}
		for grew := true; grew; {
			grew = false
			for name, decls := range funcs {
				if gated[pkg][name] {
					continue
				}
				for _, refs := range decls {
					for callee := range gated[pkg] {
						if refs.calls[callee] {
							gated[pkg][name] = true
							grew = true
							break
						}
					}
					if gated[pkg][name] {
						break
					}
				}
			}
		}
	}
	return gated
}

// holdsSeedGate reports whether a function body IS the admission: one of the
// platform spellings, or the older object-gate form — a Require-shaped call
// somewhere in a body that also names the object. The pair is what makes it the
// edge's gate and not some other object's.
func holdsSeedGate(refs references, pkg string) bool {
	for _, seed := range edgeGateSeeds {
		if refs.calls[seed] {
			return true
		}
	}
	// The older form: a Require-shaped call taking the object as an ARGUMENT.
	// Read off the call rather than from the body at large, because a body
	// holding RequireHuman(ctx) and, separately, an unrelated "relationship"
	// string — an entity-type constant, a table name in a comment's sibling
	// literal — would otherwise vouch for itself.
	if refs.gatesTheEdge {
		return true
	}
	if !slices.Contains(rowHalfOwners, pkg) {
		return false
	}
	for _, seed := range rowHalfSeeds {
		if refs.calls[seed] {
			return true
		}
	}
	return false
}

// references is what a function CALLS and what string literals it holds. Read
// off the syntax rather than the source text, so a gate spelling cannot be
// matched inside a comment that merely discusses it — and calls only, because a
// parameter or local variable that happens to share a gated function's name is
// not a call to it.
type references struct {
	calls    map[string]bool
	literals map[string]bool
	// gatesTheEdge records a Require-shaped call that takes "relationship" as
	// one of its OWN arguments. Kept as a resolved fact rather than as two
	// facts a reader has to pair up, because pairing them at the body level is
	// what let RequireHuman(ctx) beside an unrelated "relationship" literal
	// vouch for a read.
	gatesTheEdge bool
}

// packageFunctionReferences parses every non-test source in one package
// directory and returns what each function mentions.
//
// The directory is read and its files parsed one at a time rather than through
// parser.ParseDir, which is deprecated for a reason that would bite here: it
// does not consider build tags when grouping files into packages, and several
// of the directories this walks hold tagged files.
func packageFunctionReferences(t *testing.T, pkg string) map[string][]references {
	t.Helper()
	// The path is relative to the module root, which is this test's own working
	// directory: package backendarch lives at the root of the backend module.
	dir := filepath.FromSlash(pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s to resolve its edge gates: %v", pkg, err)
	}
	fset := token.NewFileSet()
	refs := map[string][]references{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s/%s to resolve its edge gates: %v", pkg, name, parseErr)
		}
		for fn, decls := range functionBodies(gatekit.ParsedFile{File: file}) {
			refs[fn] = append(refs[fn], decls...)
		}
	}
	return refs
}

func functionBodies(parsed gatekit.ParsedFile) map[string][]references {
	bodies := map[string][]references{}
	for _, decl := range parsed.File.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc {
			continue
		}
		bodies[fn.Name.Name] = append(bodies[fn.Name.Name], referencesIn(fn))
	}
	return bodies
}

func referencesIn(node ast.Node) references {
	refs := references{calls: map[string]bool{}, literals: map[string]bool{}}
	ast.Inspect(node, func(n ast.Node) bool {
		switch typed := n.(type) {
		case *ast.CallExpr:
			if name := calleeName(typed); requireCall.MatchString(name) && namesTheEdge(typed) {
				refs.gatesTheEdge = true
			}
			// calleeName is retentionscope_test.go's, shared rather than
			// respelled: "the called function's own name, ignoring any
			// qualifier" is the same question here, and auth.EdgeReadScope and
			// a local edgeScope both need to resolve to what they call.
			if name := calleeName(typed); name != "" {
				refs.calls[name] = true
			}
		case *ast.BasicLit:
			refs.literals[edgeLiteralText(typed)] = true
		}
		return true
	})
	return refs
}

// fileHoldsAGatedFunction answers for a package-level SQL fragment, which has
// no declaration of its own to ask: the file that declares it is judged as a
// whole, as restrictedreaders_test.go judges one.
func fileHoldsAGatedFunction(t *testing.T, parsed gatekit.ParsedFile, gated map[string]bool) bool {
	t.Helper()
	for name, decls := range functionBodies(parsed) {
		if gated[name] {
			return true
		}
		for _, refs := range decls {
			if holdsSeedGate(refs, pkgOf(parsed.Path)) {
				return true
			}
		}
	}
	return false
}

func firstSQLLine(literal string) string {
	for _, line := range strings.Split(strings.Trim(literal, "`\""), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(literal)
}

// namesTheEdge reports whether a call passes the relationship object as one of
// its own arguments — auth.Require(ctx, "relationship", …) and person360's
// requireRead(ctx, "relationship") alike. Asked of the CALL rather than of the
// body, so an unrelated "relationship" literal elsewhere in a function cannot
// pair with an unrelated Require-shaped call to vouch for a read.
func namesTheEdge(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if lit, isLit := arg.(*ast.BasicLit); isLit && edgeLiteralText(lit) == "relationship" {
			return true
		}
	}
	return false
}

// The extractor's own unit test, and it exists because the census is only as
// good as this regex: every miss it has had was a boundary the pattern could
// not see, and each one read exactly like a clean tree.
func TestTheRelationshipReadExtractorSeesEveryBoundary(t *testing.T) {
	seen := map[string]bool{
		"`SELECT r.id FROM relationship r WHERE r.kind = 'employment'`": true,
		// Ends at the table name, so the closing delimiter is the next
		// character. This is the one that slipped: matched against the quoted
		// token, `$` never fires and the read is invisible.
		"`SELECT r.person_id FROM relationship`":                          true,
		"`SELECT r.person_id\n\tFROM relationship\n\tWHERE r.kind = 'x'`": true,
		"`SELECT r.id FROM relationship, person p`":                       true,
		"`SELECT r.id FROM relationship)`":                                true,
		"`... JOIN relationship theirs ON theirs.person_id = p.id`":       true,
		"`SELECT 1 FROM RELATIONSHIP r`":                                  true,
		// Not reads of this table: a longer name that merely starts with it,
		// and a write, which the census deliberately does not judge.
		"`SELECT 1 FROM relationship_history r`":        false,
		"`INSERT INTO relationship (kind) VALUES ($1)`": false,
		"`SELECT relationship FROM person`":             false,
	}
	for literal, want := range seen {
		got := relationshipReadLiteral.MatchString(edgeLiteralText(&ast.BasicLit{
			Kind: token.STRING, Value: literal,
		}))
		if got != want {
			t.Errorf("the extractor %s %s\n  want it %s — a boundary the pattern cannot see reads "+
				"exactly like a clean tree",
				map[bool]string{true: "matched", false: "missed"}[got], literal,
				map[bool]string{true: "matched", false: "missed"}[want])
		}
	}
}
