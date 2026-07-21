// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "github.com/jackc/pgx/v5/pgxpool"

// WithAiPayloadCaptureFlag mirrors the model-path capture posture onto
// the trace read so clients can explain why payload content is absent.
func WithAiPayloadCaptureFlag(enabled bool) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.voiceHandlers = s.WithPayloadCaptureFlag(enabled)
	}
}
