// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// A deal has no (source_system, source_id) replay key, so `source` is
// the only provenance it carries — and the flip's crash repair reads it
// back to recognize deals it created but had not yet recorded in the
// identity map. That repair is safe only while no client can write the
// importer's namespace, which is what this mapper enforces.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

func TestDealCreateInputRefusesTheImporterNamespace(t *testing.T) {
	_, err := dealCreateInput(crmcontracts.CreateDealRequest{
		Name: "Planted", Source: "mirror:hubspot:deal:d-1",
	})
	var refused *provenance.ReservedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want provenance.ReservedError — a planted deal could be adopted as the importer's own", err)
	}
	if refused.Field != "source" {
		t.Errorf("refusal names field %q, want source", refused.Field)
	}
}

func TestDealCreateInputAcceptsAnOrdinarySource(t *testing.T) {
	in, err := dealCreateInput(crmcontracts.CreateDealRequest{Name: "Real", Source: "webform"})
	if err != nil {
		t.Fatalf("an ordinary source must stay writable: %v", err)
	}
	if in.Source != "webform" {
		t.Errorf("Source = %q, want it carried through", in.Source)
	}
}
