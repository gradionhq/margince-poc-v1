// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The contract addresses one fact as `<field>:<value_key>` (the FactKey
// parameter). Reading that back into the two columns is pure string work, and
// it is where a correction goes to the wrong row: `value_key` alone is empty
// for every single-value company fact, so a key that loses its field half stops
// naming anything in particular.

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

func TestSplitFactKeyNamesBothColumnsOrNeither(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      string
		field    string
		valueKey string
		ok       bool
	}{
		{"a single-value fact ends in a bare colon", "phone:", "phone", "", true},
		{"a multi-value fact carries its key", "named_customer:acme-inc", "named_customer", "acme-inc", true},
		// The split is on the FIRST colon, so a normalized key may contain one
		// — a customer named "Acme: The Sequel" normalizes to a key with a
		// colon in it, and splitting on the last would move the boundary.
		{"a value key containing a colon", "named_customer:acme:the-sequel", "named_customer", "acme:the-sequel", true},
		{"no separator at all", "phone", "", "", false},
		{"empty", "", "", "", false},
		{"a key with no field half", ":acme-inc", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field, valueKey, ok := splitFactKey(tc.key)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if field != tc.field || valueKey != tc.valueKey {
				t.Errorf("got (%q, %q), want (%q, %q)", field, valueKey, tc.field, tc.valueKey)
			}
		})
	}
}

// A key that names no row is the caller's mistake, so it is a 422 naming the
// parameter — not a 404, which would tell the caller this fact once existed.
func TestAMalformedFactKeyNamesTheParameterToFix(t *testing.T) {
	var parse *values.ParseError
	if !errors.As(errMalformedFactKey(), &parse) {
		t.Fatal("the refusal is not the typed shape httperr renders as a 422")
	}
	if parse.Field != "factKey" {
		t.Errorf("field = %q, want factKey — the client is told which value to fix", parse.Field)
	}
	if parse.Code != "fact_key_malformed" {
		t.Errorf("code = %q, want fact_key_malformed", parse.Code)
	}
	if !strings.Contains(parse.Message, "phone:") {
		t.Errorf("message = %q, want it to show the bare-colon spelling a single-value fact takes", parse.Message)
	}
}
