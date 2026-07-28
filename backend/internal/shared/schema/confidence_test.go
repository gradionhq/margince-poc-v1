// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package schema_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// A provider that honours the declared `type: number` and one that returns the
// same value quoted must decode to the same score. Both shapes are observed
// from models this codebase binds.
func TestConfidenceAcceptsANumberOrANumericString(t *testing.T) {
	for _, raw := range []string{`0.9`, `"0.9"`, `" 0.9 "`, `1`, `"1"`, `0`, `"0"`} {
		var got schema.Confidence
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Errorf("Unmarshal(%s) = %v, want nil", raw, err)
			continue
		}
		if got < 0 || got > 1 {
			t.Errorf("Unmarshal(%s) = %v, outside [0,1]", raw, got)
		}
	}
	var got schema.Confidence
	if err := json.Unmarshal([]byte(`"0.9"`), &got); err != nil || float64(got) != 0.9 {
		t.Fatalf("Unmarshal(\"0.9\") = %v, %v; want 0.9, nil", got, err)
	}
}

// The bug was a VALID answer in the wrong wrapper being rejected. Reading an
// invalid one must not get easier: anything that is not a finite number is
// refused in both shapes, because a value that cannot be compared passes every
// range gate downstream.
func TestConfidenceRefusesWhatCannotBeCompared(t *testing.T) {
	for _, raw := range []string{
		`"high"`, `""`, `null`, `true`, `[]`, `{}`,
		`"NaN"`, `"Inf"`, `"-Inf"`, `"1e400"`,
	} {
		var got schema.Confidence
		if err := json.Unmarshal([]byte(raw), &got); err == nil {
			t.Errorf("Unmarshal(%s) = %v, want an error", raw, got)
		}
	}
}

// An out-of-range score decodes and is refused by the site's own gate, which
// names the value. Refusing it here instead would cost a list-shaped reply
// every field it got right, to punish the one it did not.
func TestConfidenceLeavesTheRangeToTheGateThatOwnsIt(t *testing.T) {
	for raw, want := range map[string]float64{`1.5`: 1.5, `"1.5"`: 1.5, `-0.1`: -0.1, `"-0.1"`: -0.1} {
		var got schema.Confidence
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Errorf("Unmarshal(%s) = %v, want the value for the caller to gate", raw, err)
			continue
		}
		if float64(got) != want {
			t.Errorf("Unmarshal(%s) = %v, want %v", raw, got, want)
		}
	}
}

// A NaN cannot arrive as a bare JSON literal, but a numeric string can carry
// one past ParseFloat. It must never reach a range gate, where `< 0 || > 1` is
// false for NaN and the field would sail through as valid.
func TestConfidenceRefusesNonFiniteFromAString(t *testing.T) {
	var got schema.Confidence
	if err := json.Unmarshal([]byte(`"NaN"`), &got); err == nil {
		t.Fatalf("Unmarshal(\"NaN\") = %v, want an error", got)
	}
	if math.IsNaN(float64(got)) {
		t.Fatal("a refused decode still wrote NaN into the destination")
	}
}
