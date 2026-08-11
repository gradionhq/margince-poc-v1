// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the REST door OWES the read bound it refuses on.
//
// The admission half lives in agentgate.go: a non-mutating agent call is
// admitted against MCP-SESS-READS before any handler runs. This file is the
// other half — the records that answer actually hands over, charged where they
// leave. A counter a door consults and never increments bounds nobody: a
// credential reading only over /v1 would sit at zero forever, however much it
// read, which is exactly the asymmetry ADR-0055 exists to close.

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// servedMeter counts the records one agent response hands over and charges them
// against MCP-SESS-READS.
//
// It wraps the WRITER rather than threading a counter through ~290 handler
// signatures: httperr.WriteJSON is the one place a record becomes a REST
// response, and it reports what it is about to write to whatever meter the
// writer carries. That is the seam newWireRecord is on the MCP door.
type servedMeter struct {
	http.ResponseWriter
	r   *http.Request
	reg *agents.Registry
	// mayRefuse is agents/quota.go's one rule applied on this door: refuse only
	// while nothing has happened yet.
	//
	// A READ has committed nothing, so an uncountable one is withheld — a charge
	// lost to a blinking Redis is lost for good, and those records would be read
	// again for free. A MUTATION's body is written after its effect landed, so
	// withholding it would report a completed change as a failure and invite the
	// retry of something that already happened.
	mayRefuse refusability
}

// refusability names the two sides of that rule at the call sites, so a bare
// true/false cannot silently pick the wrong one.
type refusability bool

const (
	nothingHasHappenedYet  refusability = true
	theEffectAlreadyLanded refusability = false
)

// NoteServed charges n records and answers whether the response may proceed. A
// refusal is written here rather than reported upward because this is the layer
// holding the request; httperr has neither it nor any notion of a quota.
func (m *servedMeter) NoteServed(n int) bool {
	err := m.reg.ChargeServedRecords(m.r.Context(), n)
	if err == nil || !bool(m.mayRefuse) {
		return true
	}
	httperr.Write(m.ResponseWriter, m.r, err)
	return false
}

// Unwrap exposes the wrapped writer so http.NewResponseController still reaches
// the real connection through this layer — an embedded-only wrapper silently
// swallows Flush and SetWriteDeadline.
func (m *servedMeter) Unwrap() http.ResponseWriter { return m.ResponseWriter }

// remeter re-points an existing meter at a different sink, for a response that
// is BUFFERED before it is replayed (the split-update path). Without it the
// buffer — which carries no meter — would swallow the count, and the replay
// writes raw bytes rather than going back through WriteJSON, so those records
// would never be charged at all. An unmetered writer is left alone.
func remeter(w http.ResponseWriter, sink http.ResponseWriter) http.ResponseWriter {
	meter, metered := w.(*servedMeter)
	if !metered {
		return sink
	}
	resunk := *meter
	resunk.ResponseWriter = sink
	return &resunk
}
