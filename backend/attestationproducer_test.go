// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Attestation-minting fitness function (ADR-0072 §1). The T1
// correspondence-positive gate spares an address from transactional
// suppression, and its whole safety rests on connector.Counterparty's
// outbound attestation being something a connector cannot state for itself.
//
// The field itself is unexported, so the compiler — not this test — refuses
// every route that sets it directly: a keyed or positional literal, an
// encoding/json unmarshal of a provider payload, reflection, a conversion from
// a look-alike struct, a pointer handed to a row scanner. What the compiler
// cannot refuse is the minting call, WithOwnerAttestation, which is exported
// because the mapper lives in another package. This test keeps that call in one
// place, so the set of code that can mint correspondence evidence stays a set of
// one and is reviewed as such.
//
// The honest boundary: the argument to that call is trusted by construction,
// not by this test. Today's mail connectors pass a bool derived from an
// authenticated provider handle — Gmail's SENT label, an IMAP \Sent special-use
// mailbox, Microsoft's SentItems folder. A future caller passing something
// shaped like `payload.Folder == "SentItems"` from an unauthenticated webhook
// body would satisfy both the compiler and this test while forging exactly what
// the field exists to prevent. Reviewing that argument is a human obligation,
// which is why this test exists to make the call sites few enough to review.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The walk below matches the minting call by NAME, so a rename would leave it
// finding nothing and passing forever, guarding an invariant that had moved.
// This pins the name AND the signature the walk assumes, so either changing
// stops the compile here, where the walk is, rather than retiring it in silence.
var _ func(bool) connector.Counterparty = connector.Counterparty{}.WithOwnerAttestation

const (
	// The sole sanctioned minter, relative to the backend module root.
	attestationMinter = "internal/modules/capture/mailmap"
	// The port's constructor — the one way into the unexported field.
	attestationCall = "WithOwnerAttestation"
)

func TestOnlyTheMailMapperMintsTheOutboundAttestation(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata is never compiled, so nothing in it can mint anything.
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		slashed := filepath.ToSlash(path)
		if strings.HasPrefix(slashed, attestationMinter+"/") {
			return nil
		}
		// Parsed with mode 0 so build constraints are ignored: a file excluded
		// on this platform still has to obey the rule.
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == attestationCall {
				offenders = append(offenders, fset.Position(sel.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the backend tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("%s is called outside %s:\n  %s\n\n"+
			"That call mints the T1 correspondence gate's only evidence (ADR-0072 §1). It may be made\n"+
			"ONLY where the message's authorship and a provider's own filing of it are both known —\n"+
			"which is what the mail mapper does. Minting it anywhere else lets whatever supplied that\n"+
			"argument whitelist an arbitrary address past transactional suppression.\n\n"+
			"To produce an attested record, build it through capture/mailmap (Parse → AttestSentByOwner),\n"+
			"passing your provider's own filing of the message — never a value derived from its content.",
			attestationCall, attestationMinter, strings.Join(offenders, "\n  "))
	}
}
