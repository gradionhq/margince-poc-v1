// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The read/write asymmetry of a manual record grant, as a fitness function:
// a path that CHANGES a shareable record probes for write authority, not for
// visibility.
//
// The class this closes is one column that nothing read. record_grant.access
// has always carried two levels and the schema has always said "write satisfies
// read", but the only consumer was the visibility arm in platform/auth, which
// matches any live grant by design — correctly, since a `read` share must let
// its holder open the record. Every mutation then gated on that same arm, so a
// `read` share was a licence to write, silently, while the sharing screen told
// the user it was not. It was not one call site: it was thirty-odd, in nine
// modules, each of them individually defensible.
//
// What the gate asks, in three derived steps:
//
//   - the VOCABULARY is platform/auth's own shareableTables — the closed set a
//     grant can name, and therefore the only tables where the two authorities
//     can differ at all. A table that becomes shareable widens this census
//     without anyone remembering to; a table that never was (a list, a saved
//     view) is out of scope because its visibility already IS its write
//     authority.
//   - the SITES are every row probe in internal/modules naming one of those
//     tables, from inside a function that MUTATES: it reaches the storekit write
//     shape, or it sits under an entry point that took auth.Require(update|
//     delete) on anything at all. Both are read out of the tree, so a new store
//     inherits the obligation.
//   - the OBLIGATION is that the probe is one of the write-authority spellings.
//     Object admission does not count and is not consulted: every site this gate
//     was written for already held auth.Require(…, ActionUpdate) and mutated on
//     a `read` share anyway — a gate that accepted it would read green over the
//     defect it exists for.
//
// One probe family is deliberately outside the census, and the absence is not
// an oversight: auth.EnsureLinkTarget. It asks whether the caller may REFERENCE
// a record — attach an activity, name a parent org, add a list member — and
// whether "add" needs write authority on the thing added TO is a product
// question UC-E11-08 E2 raises rather than settles. It is tracked as its own
// issue rather than decided inside a security sweep.
//
// The LIST predicates (auth.ScopeClauseFor, auth.VisiblePredicate) are IN the
// census, and that is a correction rather than a flourish. A first draft left
// them out on the reasoning that a mutation's row gate is a single-row probe
// and the write-path sites holding one are conflict reads about a rival row.
// Review found the counterexample inside this gate's own tier: the contracts
// module owns no owner_id, derives its whole row scope from its anchor through
// a rendered clause, and used that ONE clause as the gate in front of every
// patch, archive, status change, cancellation and renewal — so the list
// predicate WAS the mutation's row gate, and the exclusion read green over it.
//
// The tier is internal/modules, for the reason composerowscope_test.go gives
// for choosing the opposite one: a module store owns its table and is the last
// line in front of the write, which is a different question from a compose read
// model pointing at somebody else's record. Bringing compose under this gate is
// its own change with its own evidence.
//
// Exceptions are explicit, keyed by "package-dir:FuncName", each with the
// rationale that ratified it; a reasonless or stale waiver fails.

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

const (
	// shareableVocabularyPkg holds shareableTables — the closed set of tables a
	// manual grant can name, and so the only tables where write authority and
	// visibility can disagree.
	shareableVocabularyPkg = "internal/platform/auth"

	// wantMinimumWriteProbes guards the way this gate fails silently: an
	// extractor that stops recognising probes finds no sites and reports
	// nothing, which is indistinguishable from a clean tier. It counts DISTINCT
	// sites — the walk reaches each one once per ancestor, so counting the raw
	// list would put the number five times higher and let a collapse to four
	// real probes sail past. Fifty-nine stand today; the floor sits well below
	// that, so removing a mutation stays an ordinary change and only a collapse
	// is a finding.
	wantMinimumWriteProbes = 20
)

// readAuthorityOnAWritePath ratifies a mutating path that probes a shareable
// record for VISIBILITY rather than for write authority. Each entry says what
// the probe is actually deciding, because that is always the answer: the probe
// is not this mutation's own row gate.
var readAuthorityOnAWritePath = gatekit.Waive(map[string]string{
	// Conflict and disclosure probes about a RIVAL row. Each answers "may this
	// refusal NAME the incumbent I just collided with", never "may this caller
	// change it", and the record actually being written is gated on its own way
	// in. Widening one would withhold from a caller the id of a record they can
	// perfectly well open — a worse answer, not a safer one.
	"internal/modules/people:ensureOrgDomainsUnclaimed":       "the create path's domain-collision probe: whether the 409 may name the organization already holding the domain. The organization being written is a different row, and its own gate is the organization:create grant",
	"internal/modules/people:ensureOrgDomainsUnclaimedExcept": "the edit path's twin of the same probe over the same rival row; the organization being edited took auth.EnsureWritable before this runs",
	"internal/modules/people:refusedOrgCreate":                "the duplicate-domain 409's disclosure decision: it names the incumbent organization only when the caller could have READ it, and writes nothing to that row. The create it is refusing is gated by organization:create",
	"internal/modules/people:refusedPersonCreate":             "the person twin of refusedOrgCreate, and the same decision: whether the conflict may carry the incumbent's id, never whether the caller may change that person",
	"internal/modules/people:GetDedupeCandidate":              "a READ of one dedupe pair, probing BOTH sides because the evidence names both and a pair with an out-of-scope side must read as absent. EVERY verb that may follow takes its own write-authority probe on each end — dismiss and undo through ensurePairWritable, and the merge through mergePair on top of it. Naming only one of the three is what let a dismissal ride this read for as long as it did",
	"internal/modules/capture:findLeadCollision":              "the captured lead's incumbent probe, and its answer is to SKIP: an incumbent the granting human cannot see is neither merged into nor written around, so the record is refused and the address left for someone with the scope to act. Nothing is written to that lead on this path — the merge a human may later confirm goes through the approvals engine, which takes its own authority. Widening to write authority would turn a colleague's read share into a reason to capture a SECOND lead for the same address, forking the record across scopes, which is the outcome the skip exists to prevent",

	// Reads that sit inside a flow which also writes. Each is the read half,
	// and each names where the write half takes its own authority.
	"internal/modules/people:visibleLeadScope":        "the lead-vocabulary writers' returned representation carries a lead_count — a count of the leads the CALLER MAY SEE, a read decision that only narrows. The probe has its own spelling so ONLY this count inherits the waiver; a future write path reaching the shared scopeOrAllRows still fails this gate. The vocabulary row being written is gated on its own object grant before the store runs, and no lead row is changed",
	"internal/modules/people:readAnchorForComparison": "the site-read comparison's CURRENT-STATE read, rendered beside the proposed one for a human to judge. The confirmation that may follow writes through resolveOrCreateAnchor, which takes the write-authority probe itself",
	"internal/modules/deals:visibleOffer":             "the READ spelling of the offer's deal-derived row scope, shared with the offer list, the single read and the render. Its mutation spelling is visibleOfferLocked, which calls this and then takes auth.EnsureWritable on the same deal — every offer edit goes through that one",
	"internal/modules/commissions:VisibleClause":      "the READ spelling of a commission entry's deal-derived row scope, shared with the ledger page, the summary and the single read. It decides only whether a row may be SEEN: both paths that change one — Decide and ReverseForDeal — call it to resolve the row and then take WritableEntriesForDeal, which is auth.EnsureWritable on the same deal, before writing anything",
	"internal/modules/privacy:AssembleSAR":            "an Art. 15 export is a READ, and read authority is the whole of what a read needs. It is flagged only because assembling a SAR records the request it answers; its Art. 17 sibling, which destroys rather than reads, uses auth.EnsureWritableForSubjectRights",

	// Writes that touch a shareable record's machinery without changing the
	// record, or anything a share can speak about.
	"internal/modules/consent:PreferenceTokenForEmail": "mints the unsubscribe capability the outbound message must carry (RFC 8058). The row is the RECIPIENT's own preference-centre credential, not a field of the person and not something a colleague's share grants or withholds; the send that mints it is gated on the activity it creates",
	"internal/modules/integrations:QueueRun":           "an enrichment BUYS data about a person, which is why its object gate is person:READ by design and not update. The only row it writes is integrations' own run; a provider answer that later lands on a record goes through that record's own apply path, which takes the write-authority probe there",

	// Refusal-disclosure clauses: rendered so a refusal may NAME the rows it
	// collided with, and only the ones the caller could already read. The
	// DECISION each of them feeds is unscoped on purpose — work the caller
	// cannot see must still block the write — so narrowing the clause would not
	// tighten the refusal, only strip a legitimate caller of the ids that tell
	// them what to go and look at.
	"internal/modules/deals:ensureProjectKeyFree":         "whether a key collision may name the project already holding it. The unique index is the authority that refuses either way; this clause decides only disclosure",
	"internal/modules/deals:refuseIfOccupied":             "the stage-removal refusal's naming clause: which of the deals still sitting on the stage may be listed back. Every occupant blocks the removal, visible or not",
	"internal/modules/people:refuseWhenBothCarryProjects": "the organization-merge precondition, and its own comment states the split this waiver rests on: the decision counts EVERY live project on both endpoints, and the clause governs only which of them the refusal may name",

	// The `add` verb again (#1405), in its rendered-clause form: each of these
	// writes the row that HANGS OFF the record — a link, a participant, an
	// imported connection — and probes the record only to decide which ones may
	// be touched or named.
	"internal/modules/activities:deleteVisibleLinksOfType":     "clears an activity's links to records the caller can see, so a relink cannot silently drop an edge to a record they cannot; what it deletes is the LINK row, and its own comment already states the one bit that escapes",
	"internal/modules/activities:repointDisplacedParticipants": "moves activity_participant rows from the displaced people to the relink target; the row written is the participant edge, never the person",
	"internal/modules/people:matchGhostsByEmail":               "confirms the UPLOADER's own linkedin_connection rows against people they can see. The scope is there to stop the confirmed count becoming an existence oracle — a read concern, which its own comment spells out — and the row it writes is the ghost, not the person",
	"internal/modules/capture:projectScopeClause":              "the project-attribution ladder's row predicate, narrowing which projects a captured message may be FILED UNDER. Read authority is the whole of what it needs: the project is referenced, never changed — the rows written are the activity_link edge and the activity's own version bump, and both are gated on activity:update in linkActivityToProject. A `read` share on a project is exactly the authority to see mail land on it, so requiring write here would refuse a filing to the colleague the project was deliberately shared with",

	// Read predicates whose mutating callers take the write probe elsewhere.
	"internal/modules/contracts:VisibleClause": "the contracts module's READ predicate, shared by the list, the single read and the company-value rollup. A contract owns no owner_id and inherits its whole row scope from its anchor, so this one clause used to stand in front of every mutation too; the patch, archive, status change, cancellation and renewal now go through writableContract, which takes auth.EnsureWritable on that same anchor",

	// A gap named rather than reasoned away.
	"internal/modules/people:applySitePersonFieldsTx": "the probe is on the ORGANIZATION whose published site was read, and it is a read of that company. The person this then fills is resolved from the org's employment edges and carries NO row-scope probe of its own — which is a separate defect, filed as #1406, not a property that makes this probe correct. This waiver goes when that issue does",
})

// writeAuthorityProbes are the platform/auth spellings that ask the narrower
// question. Nothing else counts — least of all auth.Require, which every
// defective site already passed.
var writeAuthorityProbes = map[string]bool{
	"EnsureWritable": true, "EnsureWritableLive": true,
	"EnsureWritableForSubjectRights": true, "WritableBy": true,
}

// recordAuthorityProbes are the single-row probes that answer "may this caller
// act on THIS record": the write-authority spellings above and the visibility
// ones they narrow. A site is one call to any of them, so a converted site
// stays in the census rather than dropping out of the gate's sight.
var recordAuthorityProbes = map[string]bool{
	"EnsureVisible": true, "EnsureVisibleLive": true, "EnsureVisibleForSubjectRights": true,
	"VisibleTo": true,
	// The rendered list predicates. They name their table in a different
	// argument, which tableArgIndex accounts for.
	"ScopeClauseFor": true, "VisiblePredicate": true,
}

// tableArgIndex is where each probe names the table it is about. The single-row
// probes take (ctx, tx, table, id); the clause renderers take (ctx, table, …).
func tableArgIndex(spelling string) int {
	switch spelling {
	case "ScopeClauseFor", "VisiblePredicate":
		return 1
	default:
		return 2
	}
}

// mutationMarkers witness that a function reaches a WRITE: the storekit write
// shape and its guarded-apply/lock family. A raw INSERT/UPDATE/DELETE literal
// counts too, read out of the SQL itself, so a store that spells its own
// statement is not outside the census.
var mutationMarkers = map[string]bool{
	"Audit": true, "Emit": true, "StampFields": true,
	"ApplyWithVersion": true, "ApplyGuarded": true, "ApplyLocked": true,
	"LockRow": true, "LockPair": true,
}

// mutatingActions are the grants whose holder is asking to CHANGE the record.
// ActionCreate is absent: a create has no row yet for a grant to widen.
var mutatingActions = map[string]bool{"ActionUpdate": true, "ActionDelete": true}

// probeSite is one record-authority probe: where it is, what it names, and
// which spelling it used.
type probeSite struct {
	dir, recv, fn string
	file          string
	line          int
	table         string
	// resolved is false when the table arrives as a parameter or a field this
	// pass cannot read — see resolveTableArg for why those stay in the census.
	resolved bool
	spelling string
}

// writeAuthorityFn is what this gate needs about one function: the probes its
// body holds, the object/action pairs it admits on, whether it reaches a write,
// and the names it mentions (the resolution edges).
type writeAuthorityFn struct {
	probes   []probeSite
	requires map[string]bool
	mutates  bool
	calls    map[string]bool
}

func TestEveryMutationOfAShareableRecordProbesForWriteAuthority(t *testing.T) {
	defer readAuthorityOnAWritePath.AssertAllMatched(t)

	tables := shareableTables(t)
	pkgs := writeAuthorityIndex(t, tables)

	// DISTINCT sites, deduplicated by where they are. The walk reaches one probe
	// once per ancestor that can call it, and the package-level bucket is merged
	// into every receiver's view, so the raw list runs five times the real
	// census — which would make the floor below vacuous: an extractor left
	// finding four real probes would still count past twenty and report a clean
	// tier. The floor has to measure what a reader would count.
	seen := map[string]bool{}
	var sites []probeSite
	for _, byReceiver := range pkgs {
		for recv, fns := range byReceiver {
			visible := visibleWriteAuthorityFns(byReceiver, recv)
			for name := range fns {
				for _, site := range mutatingProbesUnder(visible, name) {
					where := site.file + ":" + strconv.Itoa(site.line)
					if seen[where] {
						continue
					}
					seen[where] = true
					sites = append(sites, site)
				}
			}
		}
	}
	if len(sites) < wantMinimumWriteProbes {
		t.Fatalf("only %d distinct record-authority probes found on mutating paths in %s, want at least %d — the probe extractor lost its source",
			len(sites), modulesDir, wantMinimumWriteProbes)
	}

	for _, site := range sites {
		if writeAuthorityProbes[site.spelling] {
			continue
		}
		if readAuthorityOnAWritePath.Waived(t, site.dir+":"+site.fn) {
			continue
		}
		where := site.file + ":" + strconv.Itoa(site.line)
		named := strconv.Quote(site.table)
		if !site.resolved {
			named = "a record whose table this gate cannot read"
		}
		t.Errorf("%s: %s probes %s with auth.%s on a path that CHANGES it — a manual grant widens VISIBILITY at "+
			"either access level, so this admits a caller holding only a `read` share; use auth.EnsureWritable/"+
			"EnsureWritableLive/WritableBy, or ratify the probe in readAuthorityOnAWritePath",
			where, site.fn, named, site.spelling)
	}
}

// shareableTables reads platform/auth's own shareableTables map. Deriving the
// vocabulary rather than restating it is what makes a newly shareable table
// widen this census on its own; a restated copy would go quietly stale, and the
// staleness would look exactly like a clean tier.
func shareableTables(t *testing.T) map[string]bool {
	t.Helper()
	consts := map[string]string{}
	var written map[string]bool
	for _, src := range tierFiles(t, shareableVocabularyPkg) {
		for _, decl := range src.File.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				if gen.Tok == token.CONST {
					if text, ok := stringConst(value.Values[0]); ok {
						consts[value.Names[0].Name] = text
					}
					continue
				}
				if gen.Tok == token.VAR && value.Names[0].Name == "shareableTables" {
					written = map[string]bool{}
					collectMapKeys(t, value.Values[0], written)
				}
			}
		}
	}
	if len(written) == 0 {
		t.Fatalf("%s declares no shareableTables map — teach this gate where the grant vocabulary moved", shareableVocabularyPkg)
	}
	resolved := make(map[string]bool, len(written))
	for key, spelledAsIdent := range written {
		text, isConst := consts[key]
		switch {
		case isConst:
			resolved[text] = true
		case spelledAsIdent:
			t.Fatalf("%s: shareableTables is keyed by %s, which resolves to no string const this pass collected — "+
				"teach this gate where the table name is declared", shareableVocabularyPkg, key)
		default:
			resolved[key] = true
		}
	}
	return resolved
}

// writeAuthorityIndex parses internal/modules and returns, per package dir, the
// receiver-bucketed function index. Receiver bucketing is rbacgate's, and for
// its reason: a handler and a store in one package routinely spell the same
// method name, and a flat by-name index lets one answer for the other.
func writeAuthorityIndex(t *testing.T, tables map[string]bool) map[string]map[string]map[string]*writeAuthorityFn {
	t.Helper()
	pkgs := map[string]map[string]map[string]*writeAuthorityFn{}
	for _, src := range tierFiles(t, modulesDir) {
		dir := filepath.ToSlash(filepath.Dir(src.Path))
		consts := packageStringConsts(src)
		if pkgs[dir] == nil {
			pkgs[dir] = map[string]map[string]*writeAuthorityFn{}
		}
		for _, decl := range src.File.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recv := receiverName(fn)
			if pkgs[dir][recv] == nil {
				pkgs[dir][recv] = map[string]*writeAuthorityFn{}
			}
			info := pkgs[dir][recv][fn.Name.Name]
			if info == nil {
				info = &writeAuthorityFn{requires: map[string]bool{}, calls: map[string]bool{}}
				pkgs[dir][recv][fn.Name.Name] = info
			}
			at := probeSite{dir: dir, recv: recv, fn: fn.Name.Name, file: src.Path}
			indexWriteAuthorityBody(fn, info, tables, consts, at, src)
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("%s holds no packages — teach this gate where the module tier moved", modulesDir)
	}
	return pkgs
}

// packageStringConsts collects one FILE's single-name string consts, so a probe
// written as auth.EnsureVisible(ctx, tx, entityPerson, id) resolves to the table
// it names. File-scoped rather than package-scoped on purpose: a const and the
// probes that use it sit together, and widening the resolution would let an
// unrelated same-named const in another file answer for it.
func packageStringConsts(src tierFile) map[string]string {
	consts := map[string]string{}
	for _, decl := range src.File.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			if text, ok := stringConst(value.Values[0]); ok {
				consts[value.Names[0].Name] = text
			}
		}
	}
	return consts
}

// indexWriteAuthorityBody records one function's probes, admissions, write
// markers and call edges.
func indexWriteAuthorityBody(fn *ast.FuncDecl, info *writeAuthorityFn, tables map[string]bool,
	consts map[string]string, at probeSite, src tierFile,
) {
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			indexWriteAuthorityCall(n, info, tables, consts, at, src)
		case *ast.BasicLit:
			if text, isString := stringConst(n); isString && writesSQL(text) {
				info.mutates = true
			}
		}
		return true
	})
}

func indexWriteAuthorityCall(call *ast.CallExpr, info *writeAuthorityFn, tables map[string]bool,
	consts map[string]string, at probeSite, src tierFile,
) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		info.calls[fun.Name] = true
	case *ast.SelectorExpr:
		if pkg, isPkg := fun.X.(*ast.Ident); isPkg && pkg.Name == "auth" {
			recordAuthCall(fun.Sel.Name, call, info, tables, consts, at, src)
			return
		}
		if mutationMarkers[fun.Sel.Name] {
			info.mutates = true
		}
		info.calls[fun.Sel.Name] = true
	}
}

// recordAuthCall reads one platform/auth call: a record-authority probe becomes
// a site, an admission becomes an object/action pair.
func recordAuthCall(spelling string, call *ast.CallExpr, info *writeAuthorityFn, tables map[string]bool,
	consts map[string]string, at probeSite, src tierFile,
) {
	if spelling == "Require" && len(call.Args) == 3 {
		object, _ := resolveTableArg(call.Args[1], consts)
		if action, ok := call.Args[2].(*ast.SelectorExpr); ok && mutatingActions[action.Sel.Name] {
			info.requires[object] = true
		}
		return
	}
	if !recordAuthorityProbes[spelling] && !writeAuthorityProbes[spelling] {
		return
	}
	names := tableArgIndex(spelling)
	if len(call.Args) <= names {
		return
	}
	table, resolved := resolveTableArg(call.Args[names], consts)
	if resolved && !tables[table] {
		return
	}
	site := at
	site.table, site.resolved = table, resolved
	site.spelling, site.line = spelling, src.Line(call.Pos())
	info.probes = append(info.probes, site)
}

// resolveTableArg reads the table name an argument names: a string literal by
// its text, an identifier through the file's string consts.
//
// It reports whether it resolved, and that second return is what keeps the
// gate's blind spot from being silent. A probe whose table arrives as a
// parameter or a struct field — mergePair's kind, consent's subject entity —
// names a table this pass cannot see, and dropping those would mean the census
// read green over exactly the polymorphic write paths that are hardest to
// review by hand. They stay in, judged on the SPELLING alone: a write-authority
// probe is at least as narrow as a visibility one whatever table it turns out
// to name, so demanding one costs nothing on a table no grant can widen.
func resolveTableArg(expr ast.Expr, consts map[string]string) (string, bool) {
	if text, ok := stringConst(expr); ok {
		return text, true
	}
	if ident, ok := expr.(*ast.Ident); ok {
		if text, isConst := consts[ident.Name]; isConst {
			return text, true
		}
	}
	return "", false
}

// writesSQL reports whether a string literal is a statement that changes rows.
func writesSQL(text string) bool {
	upper := strings.ToUpper(strings.TrimLeft(text, " \t\r\n"))
	for _, verb := range []string{"INSERT ", "UPDATE ", "DELETE "} {
		if strings.HasPrefix(upper, verb) {
			return true
		}
	}
	return false
}

// visibleWriteAuthorityFns returns the functions a name in this receiver can
// reach: its own methods plus the package-level ones, merging a name held by
// both — a bare foo(...) and an s.foo(...) are the same token in this index, so
// it cannot tell which was meant and must not drop either's edges.
func visibleWriteAuthorityFns(byReceiver map[string]map[string]*writeAuthorityFn, recv string) map[string]*writeAuthorityFn {
	fns := make(map[string]*writeAuthorityFn, len(byReceiver[""])+len(byReceiver[recv]))
	for name, info := range byReceiver[""] {
		fns[name] = info
	}
	for name, info := range byReceiver[recv] {
		pkgLevel, both := fns[name]
		if !both {
			fns[name] = info
			continue
		}
		merged := &writeAuthorityFn{
			mutates:  pkgLevel.mutates || info.mutates,
			requires: map[string]bool{},
			calls:    map[string]bool{},
		}
		for _, src := range []*writeAuthorityFn{pkgLevel, info} {
			merged.probes = append(merged.probes, src.probes...)
			for key := range src.requires {
				merged.requires[key] = true
			}
			for key := range src.calls {
				merged.calls[key] = true
			}
		}
		fns[name] = merged
	}
	return fns
}

// mutatingProbesUnder returns the probes reachable from one function that sit on
// a MUTATING path, judged two ways because a store splits the two shapes.
//
// The first is the probe's own frame: the function holding it also reaches a
// write, which is the ordinary store method that gates then writes. The second
// is the frame above: this function admits on update or delete of table X and
// reaches a probe on X somewhere below, which is the gate-helper shape —
// promotableLead and mergePair hold the probe and write nothing themselves,
// and only their callers say what the probe is for.
func mutatingProbesUnder(fns map[string]*writeAuthorityFn, name string) []probeSite {
	reached := reachableWriteAuthority(fns, name, map[string]bool{})
	var sites []probeSite
	for _, site := range reached.probes {
		switch {
		case reached.byFrame[site.fn]:
		case len(reached.requires) > 0:
			// The match is the ADMISSION, not the table: this path admits on
			// update or delete of something and probes a shareable row on the
			// way down. Insisting the two name the same table looked tighter and
			// was wrong — a contract admits on `contract` and inherits its row
			// scope from `deal`, so the pairing never fired over the module
			// this gate's own review found. What the looser match costs is
			// conflict-disclosure reads inside a write flow, which are ratified
			// below and each say what they are deciding.
		default:
			continue
		}
		sites = append(sites, site)
	}
	return sites
}

// reachedSet is what one downward walk collected.
type reachedSet struct {
	probes   []probeSite
	requires map[string]bool
	// byFrame names the functions that both hold a probe and reach a write
	// through their own callees.
	byFrame map[string]bool
}

func reachableWriteAuthority(fns map[string]*writeAuthorityFn, name string, seen map[string]bool) reachedSet {
	out := reachedSet{requires: map[string]bool{}, byFrame: map[string]bool{}}
	if seen[name] {
		return out
	}
	seen[name] = true
	info, indexed := fns[name]
	if !indexed {
		return out
	}
	out.probes = append(out.probes, info.probes...)
	for key := range info.requires {
		out.requires[key] = true
	}
	if len(info.probes) > 0 && reachesMutation(fns, name, map[string]bool{}) {
		out.byFrame[name] = true
	}
	for call := range info.calls {
		below := reachableWriteAuthority(fns, call, seen)
		out.probes = append(out.probes, below.probes...)
		for key := range below.requires {
			out.requires[key] = true
		}
		for key := range below.byFrame {
			out.byFrame[key] = true
		}
	}
	return out
}

// reachesMutation resolves "this function writes" transitively over
// same-package calls; seen breaks recursion cycles.
func reachesMutation(fns map[string]*writeAuthorityFn, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	info, indexed := fns[name]
	if !indexed {
		return false
	}
	if info.mutates {
		return true
	}
	for call := range info.calls {
		if reachesMutation(fns, call, seen) {
			return true
		}
	}
	return false
}
