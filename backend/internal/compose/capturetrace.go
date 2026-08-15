// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture-trace read surface: what the pipeline did with a member's
// messages in the last 24 hours.
//
// It is composed separately from the capture pipeline itself because the two
// answer to different things. The pipeline is wired wherever capture runs; this
// surface exists only where there is an HTTP role to serve it, and it carries
// the deployment's payload posture, which the pipeline takes as a Sink option.

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
)

// WithCaptureTrace composes the trace read surface.
//
// tracePayloads is the deployment's capture.trace_payloads posture, reported on
// every answer so a client can distinguish "the operator did not enable this"
// from "this row has no payload" — the same distinction /ai/calls draws with
// payload_capture_enabled, and for the same reason.
func WithCaptureTrace(tracePayloads bool) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.traceHandlers = capture.NewTraceHandlers(
			capture.NewTraceStore(InstallationDB(pool)), tracePayloads)
	}
}
