// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// decodeCanonical validates the write payload against the entity's contract
// request struct and normalizes it into the canonical bag mapWrite consumes.
// These pure unit tests cover the per-entity type switch, unknown-field
// rejection, and the precision-preserving json.Number round-trip without a DB.

func TestDecodeCanonicalValidPerson(t *testing.T) {
	fields, err := decodeCanonical(datasource.EntityPerson, false, map[string]any{"first_name": "Ada", "last_name": "Lovelace"})
	if err != nil {
		t.Fatalf("decodeCanonical: %v", err)
	}
	if fields["first_name"] != "Ada" || fields["last_name"] != "Lovelace" {
		t.Errorf("bag = %#v, want first_name=Ada last_name=Lovelace", fields)
	}
}

func TestDecodeCanonicalRejectsUnknownField(t *testing.T) {
	// A misspelled field must 422 (like the native providers), not silently
	// no-op — StrictDecode rejects it.
	if _, err := decodeCanonical(datasource.EntityPerson, false, map[string]any{"frist_name": "Ada"}); err == nil {
		t.Error("an unknown/misspelled field must be rejected, not silently dropped")
	}
}

func TestDecodeCanonicalPreservesLargeIntPrecision(t *testing.T) {
	// An UpdateDealRequest is a partial patch (no required pipeline/stage UUID),
	// so it isolates the amount_minor precision round-trip.
	fields, err := decodeCanonical(datasource.EntityDeal, true, map[string]any{
		"amount_minor": int64(9007199254740993), "currency": "JPY",
	})
	if err != nil {
		t.Fatalf("decodeCanonical deal: %v", err)
	}
	// The value must arrive as a json.Number carrying the exact digits, never a
	// rounded float64.
	n, ok := fields["amount_minor"].(json.Number)
	if !ok || n.String() != "9007199254740993" {
		t.Errorf("amount_minor = %#v, want json.Number 9007199254740993 (no float64 rounding)", fields["amount_minor"])
	}
}

func TestDecodeCanonicalNilPayloadIsEmptyMap(t *testing.T) {
	fields, err := decodeCanonical(datasource.EntityPerson, true, nil)
	if err != nil {
		t.Fatalf("decodeCanonical nil: %v", err)
	}
	if fields == nil {
		t.Error("a nil payload must decode to a non-nil empty map (no nil-map panic downstream)")
	}
}

func TestWriteContractTargetCoversEveryEntity(t *testing.T) {
	for _, et := range []datasource.EntityType{
		datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityDeal,
		datasource.EntityLead, datasource.EntityActivity,
	} {
		for _, upd := range []bool{false, true} {
			target, err := writeContractTarget(et, upd)
			if err != nil || target == nil {
				t.Errorf("writeContractTarget(%s, upd=%v) = (%v, %v), want a non-nil target", et, upd, target, err)
			}
		}
	}
	if _, err := writeContractTarget(datasource.EntityType("widget"), false); err == nil {
		t.Error("writeContractTarget of an unknown entity must error")
	}
}

// completeWritePatch rejects a deal money change that carries only one of the
// amount_minor/currency pair, and carries an activity's immutable kind forward
// from the mirror row.
func TestCompleteWritePatchDealMoneyPair(t *testing.T) {
	p := NewProvider(nil, nil)
	cases := []struct {
		name    string
		fields  map[string]any
		wantErr bool
	}{
		{"amount only", map[string]any{"amount_minor": json.Number("1000")}, true},
		{"currency only", map[string]any{"currency": "EUR"}, true},
		{"both", map[string]any{"amount_minor": json.Number("1000"), "currency": "EUR"}, false},
		{"neither", map[string]any{"name": "Renamed"}, false},
	}
	for _, c := range cases {
		err := p.completeWritePatch(datasource.EntityDeal, c.fields, Row{})
		if c.wantErr && !errors.Is(err, apperrors.ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", c.name, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
		}
	}
}

func TestCompleteWritePatchActivityCarriesKindForward(t *testing.T) {
	p := NewProvider(nil, nil)
	fields := map[string]any{"subject": "Follow up"}
	row := Row{Fields: map[string]any{"kind": "call"}}
	if err := p.completeWritePatch(datasource.EntityActivity, fields, row); err != nil {
		t.Fatalf("completeWritePatch activity: %v", err)
	}
	if fields["kind"] != "call" {
		t.Errorf("kind = %v, want 'call' carried forward from the mirror row", fields["kind"])
	}
}
