// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import "github.com/gradionhq/margince/backend/internal/shared/kernel/values"

// State is the per-purpose consent vocabulary — the Go spelling of the
// person_consent state CHECK (0010), kept in sync by the enumsync
// fitness gate. Unknown and withdrawn both suppress (default-deny);
// only a proven granted authorizes an outbound action.
type ConsentState string

const (
	StateUnknown   ConsentState = "unknown"
	StateGranted   ConsentState = "granted"
	StateWithdrawn ConsentState = "withdrawn"
)

// ParseRecordableState guards the record seam: a client records a grant
// or a withdrawal; "unknown" is the absence of a decision, never an
// input (consent_event's own CHECK carries the same two-value rule).
func ParseRecordableState(raw string) (ConsentState, error) {
	switch s := ConsentState(raw); s {
	case StateGranted, StateWithdrawn:
		return s, nil
	}
	return "", &values.ParseError{Field: "state", Code: "invalid_consent_state",
		Message: "state is granted or withdrawn"}
}
