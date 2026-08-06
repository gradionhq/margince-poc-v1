// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package settings

// The mechanism's behaviour, without a database: validation, the canonical
// no-op comparison, and the refusal shape. The database-backed half (the gate,
// the audit row, the freeze probe inside a transaction) is proven in the
// integration lane, which fails loudly without Postgres rather than skipping.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

type overlayPair struct {
	Mode      string `json:"mode"`
	Incumbent string `json:"incumbent"`
}

func TestValidateRefusesAValueOfTheWrongType(t *testing.T) {
	e := DefineLocal[bool]("capture.probe", "capture_settings", "update", true, nil)

	err := e.ValidateJSON(json.RawMessage(`"yes"`))
	if err == nil {
		t.Fatal("a string passed validation for a bool setting; the wrong type must be refused, not coerced")
	}
	var fault apperrors.FieldFault
	if !errors.As(err, &fault) {
		t.Fatalf("refusal does not implement FieldFault, so it would report as an internal fault on the MCP surface: %v", err)
	}
	field, code, _ := fault.FieldFault()
	if field != "capture.probe" {
		t.Errorf("refusal names field %q, want the setting key so the caller knows what to change", field)
	}
	if code != "setting_type_mismatch" {
		t.Errorf("refusal code = %q, want setting_type_mismatch", code)
	}
}

func TestValidateCarriesTheOwningModulesReason(t *testing.T) {
	e := DefineLocal[string]("installation.probe", "installation_settings", "update", "EUR",
		func(v string) error {
			if len(v) != 3 {
				return errors.New("a base currency is three ISO-4217 letters")
			}
			return nil
		})

	err := e.ValidateJSON(json.RawMessage(`"EURO"`))
	if err == nil {
		t.Fatal("a four-letter currency passed a three-letter validator")
	}
	// The module's own sentence has to survive to the caller: platform could
	// not have written it, and a generic "invalid value" would not tell the
	// operator what to type instead.
	if !strings.Contains(err.Error(), "three ISO-4217 letters") {
		t.Errorf("refusal lost the module's reason: %v", err)
	}
}

func TestValidateAcceptsWhenTheEntryDeclaresNoValidator(t *testing.T) {
	e := DefineLocal[bool]("capture.probe", "capture_settings", "update", true, nil)
	if err := e.ValidateJSON(json.RawMessage(`false`)); err != nil {
		t.Fatalf("a well-typed value was refused by an entry with no validator: %v", err)
	}
}

// The regression this guards: `next` is encoded by Go, `before` comes back
// from Postgres, which normalizes jsonb on its own terms. Comparing the two
// byte-for-byte makes an unchanged composite look changed, so every write
// would store a row and an audit entry recording nothing.
func TestUnchangedCompositeValueIsRecognisedAcrossReEncoding(t *testing.T) {
	e := DefineLocal[overlayPair]("overlay.connection", "overlay_settings", "update", overlayPair{}, nil)

	// Field order reversed and whitespace added, exactly as a jsonb round-trip
	// is free to hand it back.
	stored := json.RawMessage(`{"incumbent": "hubspot",   "mode": "overlay"}`)
	next, err := json.Marshal(overlayPair{Mode: "overlay", Incumbent: "hubspot"})
	if err != nil {
		t.Fatalf("encoding the candidate value: %v", err)
	}

	same, err := sameValue(stored, next, e)
	if err != nil {
		t.Fatalf("comparing values: %v", err)
	}
	if !same {
		t.Error("a re-encoded identical value compared as changed; the write would be a no-op recorded as a change")
	}
}

func TestChangedValueIsRecognised(t *testing.T) {
	e := DefineLocal[overlayPair]("overlay.connection", "overlay_settings", "update", overlayPair{}, nil)
	next, err := json.Marshal(overlayPair{Mode: "native", Incumbent: ""})
	if err != nil {
		t.Fatalf("encoding the candidate value: %v", err)
	}

	same, err := sameValue(json.RawMessage(`{"mode":"overlay","incumbent":"hubspot"}`), next, e)
	if err != nil {
		t.Fatalf("comparing values: %v", err)
	}
	if same {
		t.Error("a genuinely different value compared as unchanged; the write would be silently dropped")
	}
}

func TestAnUndecodableStoredValueRefusesRatherThanOverwriting(t *testing.T) {
	e := DefineLocal[bool]("capture.probe", "capture_settings", "update", true, nil)

	// Whatever wrote this, this build cannot read it. Treating it as "different"
	// would overwrite it and destroy the only evidence of what happened.
	_, err := sameValue(json.RawMessage(`{"unexpected":"shape"}`), json.RawMessage(`true`), e)
	if err == nil {
		t.Fatal("an undecodable stored value was silently treated as comparable")
	}
	if !strings.Contains(err.Error(), "cannot decode") {
		t.Errorf("refusal does not say the stored value is unreadable: %v", err)
	}
}

func TestRegistryResolvesRegisteredKeysAndRefusesUnknownOnes(t *testing.T) {
	e := DefineLocal[bool]("capture.probe", "capture_settings", "update", true, nil)
	reg := NewRegistry(e)

	got, ok := reg.Lookup("capture.probe")
	if !ok {
		t.Fatal("a registered setting did not resolve")
	}
	if got.Object() != "capture_settings" || got.AuditVerb() != "update" {
		t.Errorf("registry lost the entry's governance: object=%q verb=%q", got.Object(), got.AuditVerb())
	}
	if _, ok := reg.Lookup("capture.never_declared"); ok {
		t.Error("an unregistered key resolved; a typo must not read as a real setting")
	}
}

func TestDefaultIsTheValueUntilSomeoneChangesIt(t *testing.T) {
	e := DefineLocal[bool]("capture.probe", "capture_settings", "update", true, nil)
	raw, err := e.DefaultJSON()
	if err != nil {
		t.Fatalf("encoding the default: %v", err)
	}
	if string(raw) != "true" {
		t.Errorf("default encoded as %s, want true — a read of an unset setting resolves to this", raw)
	}
}
