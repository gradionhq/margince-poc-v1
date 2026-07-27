// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Confidence is a model-reported score that decodes from a JSON number OR a
// numeric string.
//
// Providers disagree about which they emit for the same declared shape: a
// field this codebase declares `type: number` comes back quoted often enough
// from bound models that a decoder accepting only one form turns a good answer
// into an unusable reply — and a caller that cannot read the reply cannot act
// on it. The generation-side schema stays [Number]: that is what a conforming
// provider should send, and tolerating the deviation is the reader's job, not
// a reason to declare a looser wire contract.
//
// Tolerance is only about the WRAPPER. What the decoder refuses is what cannot
// be COMPARED — a non-finite score, against which every threshold comparison
// is false, so a floor silently stops being a floor and an out-of-range gate
// silently stops catching anything.
//
// The [0,1] range is deliberately NOT refused here. Each decode site already
// gates it with a message naming the offending value, and only the site knows
// what a violation costs: a single-answer reply has nothing to keep, while a
// list of extracted fields must lose the one bad entry rather than all of them.
type Confidence float64

// UnmarshalJSON accepts a JSON number or a numeric string, and refuses
// anything that is not a finite number.
func (c *Confidence) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("confidence: %w", err)
	}

	var f float64
	switch v := raw.(type) {
	case float64:
		f = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("confidence: %q is not a number", v)
		}
		f = parsed
	default:
		return fmt.Errorf("confidence: want a number or a numeric string, got %T", raw)
	}

	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("confidence: %v is not a finite score", f)
	}
	*c = Confidence(f)
	return nil
}
