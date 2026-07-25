// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Attestation-producer fitness function (ADR-0072 §1). The T1
// correspondence-positive gate spares an address from transactional
// suppression, and its whole safety rests on connector.Counterparty.SentByOwner
// being unforgeable. That property is not enforced by the type — the field is a
// plain bool on a struct any connector may build — it is enforced by there
// being exactly ONE producer, capture/mailmap, which sets it only where the
// provider's filing and the message's own authorship agree.
//
// Today that is true by accident of there being one mail mapper. The next
// connector to populate a Counterparty from a provider payload — a webhook
// body, a CRM export, a transcript import — could set the field straight from
// attacker-shaped JSON and bypass both halves without touching a line of the
// gate. So the obligation is derived from the tree rather than remembered: any
// new assignment outside the mapper fails here, at the moment it is written.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The sole sanctioned producer, relative to the backend module root.
const attestationProducer = "internal/modules/capture/mailmap"

// guardFile is this file: it names the field in its own pattern and message.
const guardFile = "attestationproducer_test.go"

// An assignment to the field in a composite literal (`SentByOwner: x`) or by
// selector (`… .SentByOwner = x`). Reads — the gate's own consumption — carry
// neither form and are not matched.
var attestationAssign = regexp.MustCompile(`SentByOwner\s*[:=][^=]`)

func TestOnlyTheMailMapperProducesTheOutboundAttestation(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "build" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		slashed := filepath.ToSlash(path)
		if strings.HasPrefix(slashed, attestationProducer+"/") {
			return nil
		}
		// The port declares the field and this file names it in the pattern
		// above; neither declares a value for it.
		if slashed == "internal/shared/ports/connector/connector.go" || slashed == guardFile {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if attestationAssign.Match(body) {
			offenders = append(offenders, slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the backend tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("SentByOwner is assigned outside %s:\n  %s\n\n"+
			"That field is the T1 correspondence gate's only evidence (ADR-0072 §1). It may be set\n"+
			"ONLY where a provider's own filing of the message and the message's authorship agree —\n"+
			"which is what the mail mapper does. Setting it anywhere else lets whatever produced that\n"+
			"value whitelist an arbitrary address past transactional suppression.",
			attestationProducer, strings.Join(offenders, "\n  "))
	}
}
