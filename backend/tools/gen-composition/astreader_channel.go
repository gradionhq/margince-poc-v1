// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Reading a unit's declared CHANNELS into the manifest (ADR-0107/A158).
//
// A channel is a governed capability in the sense that matters to an operator:
// it says this unit can transmit messages out of the installation, on a
// transport it names, under a member's own credential. That belongs in the
// manifest for the same reason a tool's requested tier does — an operator has to
// be able to see it before the unit ever runs.
//
// What the manifest records is the DECLARATION, not the behavior: the provider
// name and whether a Send was declared at all. Whether the function works is not
// derivable from source and is not claimed here.

import (
	"go/ast"
	"sort"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// readChannels reads the Channels slice literal.
func (r *unitReader) readChannels(expr ast.Expr, file *ast.File) ([]declaredChannel, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Channels must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	out := make([]declaredChannel, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		ch, err := r.readChannel(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[ch.Provider] {
			return nil, r.errAt(elt, "channel provider %q declared twice", ch.Provider)
		}
		seen[ch.Provider] = true
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

// readChannel reads one extension.Channel literal.
//
// Send and Live are read as PRESENCE only — a function value is not derivable
// from source, and the manifest's claim is deliberately narrow: that the unit
// declared a transport, not that the transport works. `supplies_transport`
// mirrors extension.Channel.SuppliesTransport so the manifest and the runtime
// answer the same question the same way.
func (r *unitReader) readChannel(elt ast.Expr, ext string) (declaredChannel, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "Channel")) {
		return declaredChannel{}, r.errAt(elt, "a Channels entry must be an extension.Channel literal")
	}
	var ch declaredChannel
	var hasLive bool
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return declaredChannel{}, r.errAt(e, "Channel fields must be keyed")
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok {
			return declaredChannel{}, r.errAt(kv.Key, "Channel fields must be keyed by name")
		}
		var err error
		switch k.Name {
		case "Provider":
			ch.Provider, err = r.stringLit(kv.Value, "Channel.Provider")
		case "Send":
			ch.SuppliesTransport = !isNilIdent(kv.Value)
		case "Live":
			hasLive = !isNilIdent(kv.Value)
		default:
			err = r.errAt(kv, "Channel field %s is not derivable by this generator", k.Name)
		}
		if err != nil {
			return declaredChannel{}, err
		}
	}
	// The SAME pairing rule the boot preflight enforces, run here so a unit is
	// refused at generation rather than at the boot of a build that shipped.
	if ch.SuppliesTransport && !hasLive {
		return declaredChannel{}, r.errPos(lit,
			"channel %q declares Send without Live — a transport that can send must be able to say whether it still may", ch.Provider)
	}
	// Validated through the PUBLISHED rule, exactly as an ingress source is, so
	// generation-time acceptance cannot diverge from boot-time.
	if err := (extension.Channel{Provider: ch.Provider}).Validate(); err != nil {
		return declaredChannel{}, r.errPos(lit, "%v", err)
	}
	return ch, nil
}

// isNilIdent reports whether an expression is the literal `nil`, which is how a
// capture-only channel spells "no transport".
func isNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}
