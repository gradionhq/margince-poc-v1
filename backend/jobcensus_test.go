// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The census as a fitness function, in both directions. api/jobs.yaml says what
// every job kind is; internal/compose is what actually wires one. Each half is
// checked against the other elsewhere in pieces — the generated union stops an
// undeclared kind compiling, MustBeTotal refuses a boot that registers one — but
// nothing until now held the WHOLE declaration to the WHOLE wiring: a kind
// declared and never registered, a derived timeout whose Go constant moved, an
// args field nobody declared. Those are the drifts a declaration nothing checks
// accumulates, and this is where the composition is held to it.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

func TestJobCensusMatchesTheContract(t *testing.T) {
	census, err := compose.NewJobCensus()
	if err != nil {
		t.Fatalf("building the job census: %v", err)
	}
	if err := census.Validate(); err != nil {
		t.Error(err)
	}
}
