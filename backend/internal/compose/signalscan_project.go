// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The second deterministic signal producer: a project in flight that nothing
// has been filed against for a month. Like the ghosted-thread rule it is a
// comparison rather than a judgment — the project's own clock
// (project.last_activity_at, kept by the activity write) against today — so it
// needs no model and cannot be wrong about anything a reader could not count
// for themselves.
//
// It asks the SAME question the projects-gone-quiet report answers, through
// the one predicate the deals module spells (deals.ProjectQuietSQL), so the
// report and the signal never name different projects.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// kindProjectGoneQuiet is spelled once: the fingerprint, the INSERT and the
// audit must agree.
const kindProjectGoneQuiet = "project_gone_quiet"

// severityWarn is the severity a comparison rule raises at: worth a look,
// not an alarm.
const severityWarn = "warn"

// quietProject is one project the rule fired on.
type quietProject struct {
	ProjectID      ids.UUID
	OrganizationID ids.UUID
	Name           string
	// QuietSince is the instant the silence is measured from — the last filed
	// activity, or the project's creation when nothing was ever filed. It is
	// what keys the finding to ONE quiet episode: a new activity moves it, and
	// a later silence is a new finding rather than the old one re-raised.
	QuietSince time.Time
}

// scanQuietProjects lists the projects in flight that have been silent for
// the default window, with the instant each fell silent.
func scanQuietProjects(ctx context.Context, tx pgx.Tx, now time.Time) ([]quietProject, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.organization_id, p.name, `+deals.ProjectQuietAnchorSQL("p")+`
		  FROM project p
		 WHERE p.archived_at IS NULL
		   AND `+deals.ProjectInFlightSQL("p")+`
		   AND `+deals.ProjectQuietSQL("p", "$1", 2)+`
		 ORDER BY `+deals.ProjectQuietAnchorSQL("p")+`, p.id`,
		now, deals.DefaultProjectQuietDays)
	if err != nil {
		return nil, fmt.Errorf("scan quiet projects: %w", err)
	}
	defer rows.Close()
	var out []quietProject
	for rows.Next() {
		var found quietProject
		if err := rows.Scan(&found.ProjectID, &found.OrganizationID, &found.Name, &found.QuietSince); err != nil {
			return nil, err
		}
		out = append(out, found)
	}
	return out, rows.Err()
}

// WriteProjectQuietSignals is the producer pass for quiet projects: compose
// computes WHICH projects the rule fired on and the signals module writes the
// rows. Considered and Raised are reported apart for the reason GhostedPass
// gives: a rule that fired and wrote nothing has already said so.
//
// The fingerprint carries the quiet episode's anchor, so a pass over an
// unchanged project raises nothing new and a dismissal holds until somebody
// files something and the project goes quiet AGAIN — which is a new fact
// worth a new card.
func WriteProjectQuietSignals(ctx context.Context, tx pgx.Tx, now time.Time) (GhostedPass, error) {
	found, err := scanQuietProjects(ctx, tx, now)
	if err != nil {
		return GhostedPass{}, err
	}
	pass := GhostedPass{Considered: len(found)}
	for _, project := range found {
		days := int(now.Sub(project.QuietSince).Hours() / 24)
		raised, err := signals.RecordDerived(ctx, tx, signals.DerivedSignal{
			Kind:           kindProjectGoneQuiet,
			OrganizationID: project.OrganizationID,
			ProjectID:      project.ProjectID,
			Summary:        fmt.Sprintf("Nothing has been filed on %s for %d days.", project.Name, days),
			Severity:       severityWarn,
			Fingerprint:    fingerprintOf(kindProjectGoneQuiet, project.ProjectID.String(), project.QuietSince.UTC().Format(time.RFC3339Nano)),
			Audit: map[string]any{
				paramKind: kindProjectGoneQuiet, "days_silent": days,
				attributionFieldProject: project.ProjectID.String(), "quiet_since": project.QuietSince.UTC(),
			},
		}, now)
		if err != nil {
			return pass, err
		}
		if raised {
			pass.Raised++
		}
	}
	return pass, nil
}
