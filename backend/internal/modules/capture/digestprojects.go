// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The morning digest's projects section: what moved on the bodies of work
// overnight, and which have gone quiet. The shape is this module's — it is
// stored in the digest payload this module owns — but the QUESTIONS are the
// deals module's to answer (the phase ladder, the quiet rule the
// projects-gone-quiet report and the project_gone_quiet signal share), and a
// module never imports a sibling. So the section arrives through a seam the
// composition layer injects, and a digest built without one carries no
// section rather than an empty one: absent says "this build could not ask",
// empty says "nothing happened".

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// DigestProjectsSource answers the projects section for one reader: what
// happened since `since`, measured at `now`, inside the build's transaction.
// ctx carries THAT reader as the acting principal — their live grants and row
// scope — so the section names only what they could open themselves. nil
// means the reader holds no project grant and gets no section.
type DigestProjectsSource func(ctx context.Context, tx pgx.Tx, since, now time.Time) (*DigestProjects, error)

// DigestProjects is the section as stored and served.
type DigestProjects struct {
	// PhaseChanges are the ladder moves recorded in the window, newest first.
	PhaseChanges []DigestProjectPhaseChange `json:"phase_changes"`
	// NewCommitments are the projects that gained open tasks in the window,
	// with how many, most first.
	NewCommitments []DigestProjectCommitments `json:"new_commitments"`
	// GoneQuiet are the projects in flight the quiet rule fires on, quietest
	// first.
	GoneQuiet []DigestProjectQuiet `json:"gone_quiet"`
}

// DigestProjectRef names one project the way a reader links to it.
type DigestProjectRef struct {
	ProjectID ids.UUID `json:"project_id"`
	Name      string   `json:"name"`
	Key       *string  `json:"key,omitempty"`
}

// DigestProjectPhaseChange is one recorded ladder move.
type DigestProjectPhaseChange struct {
	DigestProjectRef
	FromPhase  *string   `json:"from_phase,omitempty"`
	ToPhase    string    `json:"to_phase"`
	OccurredAt time.Time `json:"occurred_at"`
}

// DigestProjectCommitments is one project and the open tasks filed under it
// in the window.
type DigestProjectCommitments struct {
	DigestProjectRef
	NewOpenCommitments int `json:"new_open_commitments"`
}

// DigestProjectQuiet is one project the quiet rule fired on.
type DigestProjectQuiet struct {
	DigestProjectRef
	Phase string `json:"phase"`
	// QuietSince is when the silence began: the last filed activity, or the
	// project's creation when nothing was ever filed.
	QuietSince time.Time `json:"quiet_since"`
	DaysQuiet  int       `json:"days_quiet"`
	OwnerID    *ids.UUID `json:"owner_id,omitempty"`
}

// WithDigestProjects wires the projects section into every digest this
// registry builds.
func (r *Registry) WithDigestProjects(src DigestProjectsSource) *Registry {
	r.digestProjects = src
	return r
}
