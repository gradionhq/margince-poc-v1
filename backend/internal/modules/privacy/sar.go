// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// GDPR Art. 15 subject-access assembly (admin-mediated in V1): one
// operation gathers everything held about a person — the normalized
// row, channels, relationships, deals they hold a stake in, timeline
// activities, consent state and proof log, and the raw capture
// payloads that mention them — into a single export package. The
// export is itself audited (action=export): who pulled whose data,
// when.

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SARPackage is the assembled export. Sections hold raw row maps —
// the package is a data handover, not an API shape.
type SARPackage struct {
	Subject       map[string]any   `json:"subject"`
	Emails        []map[string]any `json:"emails"`
	Phones        []map[string]any `json:"phones"`
	Relationships []map[string]any `json:"relationships"`
	Deals         []map[string]any `json:"deals"`
	Leads         []map[string]any `json:"leads"`
	Activities    []map[string]any `json:"activities"`
	Consent       []map[string]any `json:"consent"`
	ConsentEvents []map[string]any `json:"consent_events"`
	RawCapture    []map[string]any `json:"raw_capture"`
}

// AssembleSAR builds the package. It is a privileged read: the caller
// needs the person.delete grant (the same trust level erasure needs)
// AND an unbounded row scope — see the admin check below.
func AssembleSAR(ctx context.Context, pool *pgxpool.Pool, personID ids.UUID) (SARPackage, error) {
	if err := auth.Require(ctx, "person", principal.ActionDelete); err != nil {
		return SARPackage{}, err
	}
	// Admin-mediated means ADMIN: the assembly deliberately crosses the
	// caller's row scope (Art. 15 owes the subject everything, not the
	// slice one rep may see), so only an unbounded scope may run it.
	actor, ok := principal.Actor(ctx)
	if !ok || !auth.Unbounded(actor) {
		return SARPackage{}, apperrors.ErrPermissionDenied
	}
	var pkg SARPackage
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID); err != nil {
			return err
		}
		sections := []struct {
			dest  *[]map[string]any
			query string
		}{
			{&pkg.Emails, `SELECT email, email_type, is_primary FROM person_email WHERE person_id = $1`},
			{&pkg.Phones, `SELECT phone, phone_type FROM person_phone WHERE person_id = $1`},
			{&pkg.Relationships, `SELECT kind, organization_id, deal_id, role, started_at, ended_at
			   FROM relationship WHERE person_id = $1 AND archived_at IS NULL`},
			{&pkg.Deals, `SELECT d.id, d.name, d.status, d.amount_minor, d.currency
			   FROM deal d JOIN relationship r ON r.deal_id = d.id
			   WHERE r.kind = 'deal_stakeholder' AND r.person_id = $1 AND r.archived_at IS NULL`},
			{&pkg.Leads, `SELECT l.id, l.full_name, l.email, l.title, l.company_name, l.status, l.created_at
			   FROM lead l
			   WHERE l.promoted_person_id = $1
			      OR l.id IN (SELECT converted_from_lead_id FROM person WHERE id = $1 AND converted_from_lead_id IS NOT NULL)
			      OR (l.email IS NOT NULL AND EXISTS (
			            SELECT 1 FROM person_email pe WHERE pe.person_id = $1 AND pe.email = lower(l.email)))`},
			{&pkg.Activities, `SELECT a.id, a.kind, a.subject, a.body, a.occurred_at, a.source_system
			   FROM activity a JOIN activity_link l ON l.activity_id = a.id
			   WHERE l.person_id = $1`},
			{&pkg.Consent, `SELECT cp.key AS purpose, pc.state, pc.lawful_basis, pc.captured_at
			   FROM person_consent pc JOIN consent_purpose cp ON cp.id = pc.purpose_id
			   WHERE pc.person_id = $1`},
			{&pkg.ConsentEvents, `SELECT cp.key AS purpose, ce.new_state, ce.source, ce.captured_at
			   FROM consent_event ce JOIN consent_purpose cp ON cp.id = ce.purpose_id
			   WHERE ce.person_id = $1`},
			{&pkg.RawCapture, `SELECT rc.source_system, rc.source_id, rc.payload, rc.received_at
			   FROM raw_capture rc
			   WHERE EXISTS (SELECT 1 FROM person_email pe WHERE pe.person_id = $1
			                 AND rc.payload::text ILIKE
			                     '%' || replace(replace(replace(pe.email, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\')`},
		}

		subject, err := rowMaps(ctx, tx, `
			SELECT id, full_name, first_name, last_name, title, social, address, source, created_at
			FROM person WHERE id = $1`, personID)
		if err != nil {
			return err
		}
		if len(subject) == 0 {
			return apperrors.ErrNotFound
		}
		pkg.Subject = subject[0]

		for _, section := range sections {
			rows, err := rowMaps(ctx, tx, section.query, personID)
			if err != nil {
				return err
			}
			*section.dest = rows
		}

		_, err = storekit.Audit(ctx, tx, "export", "person", personID, nil, map[string]any{
			"kind": "sar", "activities": len(pkg.Activities), "raw_rows": len(pkg.RawCapture),
		})
		return err
	})
	return pkg, err
}

// rowMaps runs one query and returns each row as column→value.
func rowMaps(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(values))
		for i, field := range rows.FieldDescriptions() {
			row[field.Name] = values[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
