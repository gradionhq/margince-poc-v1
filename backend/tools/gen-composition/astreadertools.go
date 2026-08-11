// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The TOOL half of the declaration read, split out of astreader.go — which
// reads an extension.Extension literal generally, while everything here is
// about one field of it and the join that field participates in.
//
// The seam is real rather than a line-count dodge: after the narrowing, a Tools
// entry is {Name, Handle} and contributes NOTHING to the manifest. What this
// file does is decide whether a unit's Go behavior and its contract-declared
// operations agree, and turn the contract-declared side into the manifest's
// risk tiers. astreader.go never needs to know how either is spelled.

import (
	"go/ast"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// readTools reads the unit's Tools slice. After the narrowing, a Tools entry
// carries only {Name, Handle} — the verb, and the behavior. Nothing here
// reaches the manifest: what an operator resolves is DECLARED in the unit's
// contract fragment and derived from the merged contract (extverbs.go). What
// this read is for is the join between the two halves, and its one refusal:
// behavior for a verb no contract operation declares.
func (r *unitReader) readTools(expr ast.Expr, file *ast.File) ([]declaredTool, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Tools must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	tools := make([]declaredTool, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		t, err := r.readTool(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[t.name] {
			return nil, r.errAt(elt, "tool %s declared twice", t.name)
		}
		seen[t.name] = true
		tools = append(tools, t)
	}
	return tools, nil
}

func (r *unitReader) readTool(elt ast.Expr, ext string) (declaredTool, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "Tool")) {
		return declaredTool{}, r.errAt(elt, "a Tools entry must be an extension.Tool literal")
	}
	var d declaredTool
	d.at = lit
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return declaredTool{}, r.errAt(e, "Tool fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return declaredTool{}, r.errAt(kv.Key, "Tool fields must be keyed by name")
		}
		var err error
		switch key.Name {
		case "Name":
			d.name, err = r.stringLit(kv.Value, "Tool.Name")
		case "Handle":
			// Behavior is not a static declaration and never reaches the
			// manifest. Whether one is SERVED is read anyway, because that is
			// what separates a live capability from a verb the unit publishes
			// and does not run. A declared `Handle: nil` is inert — it is how
			// the seam spells "declare it, serve nothing", and the runtime
			// adapter skips exactly that — so the field's presence is not the
			// question; its value being non-nil is. See isStaticallyNil for the
			// spellings that count as nil, and readHandle for why a non-nil
			// value must be a bare identifier.
			d.served, err = r.readHandle(kv.Value, ext)
		default:
			// Fail closed, and this arm is what keeps the narrowing HONEST: a
			// unit still declaring Tier, Description or InputSchema in Go is
			// told, at that line, that the field moved to its contract
			// fragment — rather than having it silently ignored while the
			// contract's value governs.
			err = r.errAt(kv, "Tool field %s is not derivable by this generator — a Tool declares {Name, Handle}; tier, scope, version, title, description and the I/O schemas are declared in the unit's %s/<contract>.yaml fragment and read from the merged contract", key.Name, apiLayer)
		}
		if err != nil {
			return declaredTool{}, err
		}
	}
	// Grammar only. Every other rule about a tool is a rule about its
	// DECLARATION, and the declaration is the contract's (extension.Verb).
	if err := (extension.Tool{Name: d.name}).Validate(); err != nil {
		return declaredTool{}, r.errPos(lit, "%v", err)
	}
	return d, nil
}

// readHandle reports whether a Tools entry serves a handler, refusing any
// non-nil spelling that is not a bare identifier.
//
// A declared handler must name a package-level function the runtime adapter
// can call directly. The AST cannot tell an inert `pkg.Fn` (a value from
// some other package, still just a name) apart from `recv.Method` — a
// method value that closes over a receiver and can reopen liveness the
// declaration is supposed to foreclose — without type information the
// generator does not have. Nor can it evaluate `mkHandler()` without
// running code, which is the one thing a static reader must never do.
// Identifier-only is therefore the sole rule that keeps "a declaration is
// inert data" checkable: it accepts the one spelling the reader can judge
// by shape alone, and refuses every other one on the same conservative
// footing as isStaticallyNil below.
func (r *unitReader) readHandle(expr ast.Expr, ext string) (served bool, err error) {
	if isStaticallyNil(expr, ext) {
		return false, nil
	}
	if _, ok := expr.(*ast.Ident); ok {
		return true, nil
	}
	return false, r.errAt(expr, "Tool.Handle must be a plain identifier naming the handler function, or one of the documented inert nil spellings (nil, extension.ToolHandler(nil), (nil))")
}

// isStaticallyNil reports whether an expression is nil at the declaration —
// which is how a Tools entry says "declare it, serve nothing", and what the
// runtime adapter skips on.
//
// Two spellings, because both reach the adapter as the same nil function value:
// the bare `nil`, and a conversion of it through the PUBLISHED extension.
// ToolHandler type (`extension.ToolHandler(nil)`), which a unit author writes
// when the surrounding literal needs the type to be obvious.
//
// The CallExpr arm checks the callee, not just the argument count, and that
// check is load-bearing, not decorative: a syntactic conversion and an
// ordinary one-argument call are indistinguishable by shape alone (`T(x)` and
// `f(x)` parse identically), so accepting any one-argument call whose sole
// argument is nil — without checking what is being called — would read
// `mustDial(nil)` as inert too. That is not the safe failure mode: it does
// not merely admit one more spelling of "serve nothing", it exempts a call
// that already ran, at declaration time, from BOTH gates the tool has —
// readHandle's identifier-only rule for anything else, and readTool's
// served-tool-needs-a-Description refusal, which never even asks the
// question for something this function has already called inert. Requiring
// the callee to be exactly the published extension.ToolHandler conversion
// keeps this arm what its name promises: a real, code-free type conversion
// of the constant nil, not a call to arbitrary unit-authored code. Anything
// else — a function name, a literal, any other call — is refused outright by
// the Ident check in readHandle above; this reader never falls back to
// treating an unrecognized shape as inert.
func isStaticallyNil(expr ast.Expr, ext string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil"
	case *ast.CallExpr:
		return len(e.Args) == 1 && isSelector(e.Fun, ext, "ToolHandler") && isStaticallyNil(e.Args[0], ext)
	case *ast.ParenExpr:
		return isStaticallyNil(e.X, ext)
	}
	return false
}

// declaredTool is one Tools entry as the source states it: the verb, whether
// it serves a handler, and the position, so the join against the contract can
// report a mismatch at the line that caused it.
type declaredTool struct {
	name   string
	served bool
	at     ast.Node
}

// served keeps the entries that actually serve a handler, which is the only
// population either join has an opinion about.
//
// An inert entry is filtered out rather than joined, because the two rules
// would otherwise contradict each other: `Handle: nil` is DEFINED as "declare
// it, serve nothing" and the runtime adapter skips it, so an inert entry the
// contract does not declare registers nothing, publishes nothing, and is
// indistinguishable at boot from an entry that was never written — while the
// join would refuse the whole unit over it. What the join is for is behavior
// with no published surface, and an inert entry has no behavior.
func served(entries []declaredTool) []declaredTool {
	kept := make([]declaredTool, 0, len(entries))
	for _, e := range entries {
		if e.served {
			kept = append(kept, e)
		}
	}
	return kept
}

// joinToolsToContract reconciles the unit's Go behavior with the operations its
// contract fragment declares, and refuses the one direction that is a defect.
//
// Behavior for a verb no operation declares is refused: it is a capability with
// no published surface — nothing lists it, nothing documents it, no manifest
// entry asks an operator about it, and yet it would be registered into the same
// registry the core tools ride. The reverse is NOT a defect: a declared verb
// with no Go behavior is a contract-only governed request (fixtures'
// crm-hello), which the manifest records and the boot serves nothing for.
func (r *unitReader) joinToolsToContract(tools []declaredTool, verbs []declaredVerb) error {
	declared := make(map[string]bool, len(verbs))
	for _, d := range verbs {
		declared[d.verb.Tool] = true
	}
	for _, t := range tools {
		if declared[t.name] {
			continue
		}
		return r.errPos(t.at, "tool %q has behavior here but no operation in this unit's %s/ fragments declares it — declare it in the contract (the merged contract is what publishes a verb), or delete the entry", t.name, apiLayer)
	}
	return nil
}

// toolRequests turns the unit's contract-declared operations into its manifest
// risk-tier entries. A tool requires one scope; the descriptor carries it as
// its (single-element) scope set, the general shape shared across governed
// kinds.
func toolRequests(verbs []declaredVerb) ([]riskTierRequest, error) {
	out := make([]riskTierRequest, 0, len(verbs))
	for _, d := range verbs {
		c := riskTierRequest{
			ID:           "tool/" + d.verb.Tool,
			Unit:         string(d.verb.Unit),
			Kind:         kindAgentTool,
			Contract:     d.verb.Contract,
			Operation:    opAgentToolInvoke,
			OperationID:  d.verb.OperationID,
			Route:        d.verb.Route,
			Method:       d.verb.Method,
			Scopes:       []string{string(d.verb.RequestedScope)},
			Tier:         string(d.verb.Tier),
			FragmentHash: d.fragmentHash,
		}
		digest, err := descriptorDigest(c)
		if err != nil {
			return nil, err
		}
		c.Digest = digest
		out = append(out, c)
	}
	return out, nil
}
