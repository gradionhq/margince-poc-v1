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
	"strings"

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
	Subject map[string]any   `json:"subject"`
	Emails  []map[string]any `json:"emails"`
	Phones  []map[string]any `json:"phones"`
	// The messaging-channel accounts bound to the subject: which provider
	// identity writes as them, the handle it carries, whether they have blocked
	// this installation's bot, and whether the binding is still live.
	ChannelIdentities []map[string]any `json:"channel_identities"`
	// Which conversations the subject was recorded as being IN, and in what
	// role (ACT-DDL-3). Distinct from Activities, which is what was said: this
	// is the record that they were a party to it at all, and it is held about
	// them whether or not they were ever a contact.
	InteractionParticipation []map[string]any `json:"interaction_participation"`
	// Where the subject appears in a colleague's imported LinkedIn network.
	// They never consented to that import and would have no way to know of it.
	LinkedInConnections []map[string]any `json:"linkedin_connections"`
	Relationships       []map[string]any `json:"relationships"`
	Deals               []map[string]any `json:"deals"`
	Leads               []map[string]any `json:"leads"`
	Activities          []map[string]any `json:"activities"`
	Attachments         []map[string]any `json:"attachments"`
	Consent             []map[string]any `json:"consent"`
	ConsentEvents       []map[string]any `json:"consent_events"`
	RawCapture          []map[string]any `json:"raw_capture"`
	FieldOrigins        []map[string]any `json:"field_origins"`
	// What capture decided about the subject's own address, and why — an
	// automated decision the subject is owed sight of (CAP-DDL-8).
	CaptureDispositions []map[string]any `json:"capture_dispositions"`
	// The governed outbound messages addressed to the subject: what was sent
	// to them, when, and whether it left (comms_outbound).
	SentMessages []map[string]any `json:"sent_messages"`
}

// AssembleSAR builds the package. It is a privileged read: the caller
// needs the person.delete grant (the same trust level erasure needs)
// AND an unbounded row scope — see the admin check below.
func AssembleSAR(ctx context.Context, pool *pgxpool.Pool, personID ids.PersonID) (SARPackage, error) {
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
		if err := auth.EnsureVisibleForSubjectRights(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		sections := sarSections(&pkg)

		subject, err := rowMaps(ctx, tx, `
			SELECT p.id, p.full_name, p.first_name, p.last_name, p.title,
			       (SELECT jsonb_object_agg(ps.platform, ps.handle) FROM person_social ps WHERE ps.person_id = p.id) AS social,
			       p.address_line1, p.address_line2, p.address_city, p.address_region, p.address_postal_code, p.address_country,
			       p.source, p.created_at
			FROM person p WHERE p.id = $1`, personID)
		if err != nil {
			return err
		}
		if len(subject) == 0 {
			return apperrors.ErrNotFound
		}
		pkg.Subject = subject[0]
		if err := appendSubjectCustomValues(ctx, tx, personID, pkg.Subject); err != nil {
			return err
		}

		for _, section := range sections {
			rows, err := rowMaps(ctx, tx, section.query, personID)
			if err != nil {
				return err
			}
			*section.dest = rows
		}

		_, err = storekit.Audit(ctx, tx, "export", "person", personID.UUID, nil, map[string]any{
			"kind": "sar", "activities": len(pkg.Activities), "raw_rows": len(pkg.RawCapture),
		})
		return err
	})
	return pkg, err
}

// appendSubjectCustomValues merges the subject's stored cf_ values into
// the export's subject map, keyed by column name. The column set comes
// from the catalog with ANY status (see subjectcolumns.go): Art. 15 owes
// the subject everything HELD, and a retired field's column still stores
// its values. Extraction rides the same storekit mechanics the record
// surface reads with, so each value exports in its documented wire shape;
// a NULL column stays absent, like every other empty section detail.
func appendSubjectCustomValues(ctx context.Context, tx pgx.Tx, personID ids.PersonID, subject map[string]any) error {
	columns, err := subjectCustomColumns(ctx, tx, "person")
	if err != nil || len(columns) == 0 {
		return err
	}
	dests := storekit.ScanDests(columns)
	query := `SELECT ` + strings.TrimPrefix(storekit.SelectSuffix(columns), ", ") + ` FROM person WHERE id = $1`
	if err := tx.QueryRow(ctx, query, personID).Scan(dests...); err != nil {
		return err
	}
	for name, value := range storekit.ExtractValues(columns, dests) {
		subject[name] = value
	}
	return nil
}

// sarSection pairs a destination package section with the query that fills
// it. Every query is keyed to the single personID bound param ($1).
type sarSection struct {
	dest  *[]map[string]any
	query string
}

// sarSections is the Art. 15 gather list: the exact set of tables that hold
// data about the subject, each bound to the package field it populates. The
// query set is compliance-critical — adding or dropping a source changes what
// the export owes the data subject. It is assembled chapter by chapter, and the
// order the chapters concatenate in is the order the export runs them in.
func sarSections(pkg *SARPackage) []sarSection {
	sections := sarIdentitySections(pkg)
	sections = append(sections, sarRecordSections(pkg)...)
	sections = append(sections, sarMessagingSections(pkg)...)
	sections = append(sections, sarConsentSections(pkg)...)
	return append(sections, sarProvenanceSections(pkg)...)
}

// sarIdentitySections gather how the subject is identified and where they are
// named: their own addresses, numbers and channel accounts, the conversations
// they were recorded as a party to, and the imported networks they appear in.
func sarIdentitySections(pkg *SARPackage) []sarSection {
	return []sarSection{
		// The three identifier sections export ARCHIVED rows alongside live
		// ones: Art. 15 owes what is HELD, and a retired address, number or
		// channel binding is still a record of how the subject was reached, and
		// of which account wrote as them. Each therefore carries archived_at.
		// Without it every identifier in the export reads as current, so the
		// subject cannot tell a retirement that happened from one that did not —
		// in the very package they would check it in.
		{&pkg.Emails, `SELECT email, email_type, is_primary, archived_at FROM person_email WHERE person_id = $1`},
		{&pkg.Phones, `SELECT phone, phone_type, archived_at FROM person_phone WHERE person_id = $1`},
		{&pkg.ChannelIdentities, `SELECT provider, channel_user_id, username, blocked_at, source, created_at, archived_at
		   FROM person_channel_identity WHERE person_id = $1`},
		{&pkg.InteractionParticipation, `SELECT ap.activity_id, ap.role, ap.address, ap.created_at,
		       a.kind, a.occurred_at, a.direction
		   FROM activity_participant ap
		   JOIN activity a ON a.id = ap.activity_id
		  WHERE ap.person_id = $1
		     OR (ap.address IS NOT NULL AND ap.address IN (
		         SELECT lower(email) FROM person_email WHERE person_id = $1))`},
		// The same reach erasure uses: matched, or carrying their address, or
		// bearing their name at an employer they actually work for. Art. 15
		// owes what is HELD, and an unmatched ghost holds their name and
		// employer just as surely as a confirmed one does.
		{&pkg.LinkedInConnections, `SELECT full_name, position, company_name, connected_on,
		       email, profile_url, match_status, source, synced_at
		   FROM linkedin_connection g
		  WHERE g.matched_person_id = $1
		     OR (g.email IS NOT NULL AND g.email IN (
		         SELECT lower(email) FROM person_email WHERE person_id = $1))
		     -- The profile URL is an identifier the subject is reachable by,
		     -- and it is held about them whether or not the matcher ever
		     -- linked the row. A package that omitted it would answer "what do
		     -- you hold about me" with less than is held.
		     OR (g.profile_url IS NOT NULL AND g.profile_url IN (
		         SELECT handle FROM person_social
		          WHERE person_id = $1 AND platform = 'linkedin'))
		     OR (g.normalized_company IS NOT NULL
		         AND g.normalized_name = (SELECT lower(f_unaccent(full_name)) FROM person WHERE id = $1)
		         AND EXISTS (
		             SELECT 1 FROM relationship r
		              WHERE r.person_id = $1 AND r.kind = 'employment'
		                AND r.archived_at IS NULL
		                AND r.organization_id = g.matched_org_id))`},
	}
}

// sarRecordSections gather the business records the subject appears in: who
// they are connected to, the deals they hold a stake in, the leads they came
// from, and their timeline with the files hanging off it.
func sarRecordSections(pkg *SARPackage) []sarSection {
	return []sarSection{
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
		{&pkg.Attachments, `SELECT at.id, at.entity_type, at.entity_id, at.filename,
		      at.content_type, at.byte_size, at.created_at
		   FROM attachment at
		   WHERE (at.entity_type = 'person' AND at.entity_id = $1)
		      OR (at.entity_type = 'activity' AND at.entity_id IN (
		            SELECT l.activity_id FROM activity_link l WHERE l.person_id = $1))`},
	}
}

// sarMessagingSections gather both directions of the messaging boundary: what
// capture decided about mail arriving from the subject, and what this
// installation sent out about or to them.
func sarMessagingSections(pkg *SARPackage) []sarSection {
	return []sarSection{
		{&pkg.CaptureDispositions, `SELECT p.email, p.display_name, p.status, p.disposition_reason, p.created_at, p.resolved_at
		   FROM capture_pending_counterparty p
		   WHERE p.email IN (SELECT email FROM person_email WHERE person_id = $1)`},
		// The governed outbound messages this installation sent about or to the
		// subject. Reached BOTH ways on purpose, unlike the erasure cascade: a
		// send whose activity was never linked to their record still went to
		// their address, and one addressed to a third party but filed on their
		// timeline is still a message about them.
		//
		// The two arms err in OPPOSITE directions and both are deliberate.
		// Reaching by address alone would miss the timeline; reaching by link
		// alone would miss the unlinked send.
		//
		// The PROJECTION is deliberate too: recipients and cc are returned
		// whole, so a message the subject shared with other people hands the
		// export those people's addresses as well — whichever arm matched the
		// row. Narrowing the arrays to the subject's own address would be the
		// safer default in a self-serve export, and it is rejected here for two
		// reasons. An address list is part of what the message WAS, and Art. 15
		// owes the subject the data held about them rather than a redraft of
		// it. And this assembly is admin-mediated (AssembleSAR demands the
		// person.delete grant and an unbounded scope, above), so the disclosure
		// is a human handing a package to a subject, not an endpoint answering
		// one — the same posture, and the same tolerated over-inclusion, as the
		// Activities and Attachments sections, whose free text and filenames
		// name third parties for exactly the same reason. It is what separates
		// this from the erasure cascade, which must refuse the equivalent
		// reach: a disclosure to an admin-mediated export is recoverable, and
		// destroying another subject's evidence is not.
		//
		// It spans BOTH shapes the row admits (comms_outbound_shape, 0155): a
		// channel delivery leaves subject/recipients/cc null and names its
		// addressee in channel_user_id, so a mail-only projection would hand a
		// channel-only subject a message with no addressee — withholding the
		// account id the row holds about them.
		{&pkg.SentMessages, `SELECT o.subject, o.body, o.recipients, o.cc, o.consent_purpose,
		      o.provider, o.channel_user_id, o.status, o.sent_at, o.created_at
		   FROM comms_outbound o
		   WHERE o.activity_id IN (SELECT l.activity_id FROM activity_link l WHERE l.person_id = $1)
		      OR EXISTS (
		           SELECT 1 FROM jsonb_array_elements_text(o.recipients || o.cc) AS addr
		           WHERE lower(addr) IN (SELECT email FROM person_email WHERE person_id = $1))`},
	}
}

// sarConsentSections gather the per-purpose consent state and the proof log
// behind it — what the subject agreed to, and every change of mind on record.
func sarConsentSections(pkg *SARPackage) []sarSection {
	return []sarSection{
		{&pkg.Consent, `SELECT cp.key AS purpose, pc.state, pc.lawful_basis, pc.captured_at
		   FROM person_consent pc JOIN consent_purpose cp ON cp.id = pc.purpose_id
		   WHERE pc.person_id = $1`},
		{&pkg.ConsentEvents, `SELECT cp.key AS purpose, ce.new_state, ce.source, ce.captured_at
		   FROM consent_event ce JOIN consent_purpose cp ON cp.id = ce.purpose_id
		   WHERE ce.person_id = $1`},
	}
}

// sarProvenanceSections gather where the held data CAME FROM: the raw provider
// payloads the subject was captured out of, and the per-field record of who
// captured what from where.
func sarProvenanceSections(pkg *SARPackage) []sarSection {
	return []sarSection{
		// Reached two ways, like the erasure purge this mirrors (erasure.go's
		// purgeDerivedTraces): by email, ILIKE against the stored address, and
		// by channel identity, a typed JSONB path equality rather than a
		// substring match — a Telegram-only subject carries no email at all,
		// so the email arm alone would silently omit their entire channel
		// history from the export, and their sender id is a bare digit run
		// that a substring match would also match against other rows' message
		// ids, timestamps and other people's ids. The two payload shapes
		// matched (message.from.id, my_chat_member.chat.id) are the same two
		// capture/telegram's Normalize and ParseMembership read the customer's
		// id from — both update kinds land in raw_capture. The membership arm
		// reads the chat and not new_chat_member.user, which is the BOT
		// (capture/telegram/membership.go): keyed on that, a subject who only
		// ever blocked the bot would be handed an export missing the one
		// record the installation holds about them.
		{&pkg.RawCapture, `SELECT rc.source_system, rc.source_id, rc.payload, rc.received_at
		   FROM raw_capture rc
		   WHERE EXISTS (SELECT 1 FROM person_email pe WHERE pe.person_id = $1
		                 AND rc.payload::text ILIKE
		                     '%' || replace(replace(replace(pe.email, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\')
		      OR EXISTS (SELECT 1 FROM person_channel_identity pci WHERE pci.person_id = $1
		                 AND rc.source_system = pci.provider
		                 AND (rc.payload->'message'->'from'->>'id' = pci.channel_user_id
		                      OR rc.payload->'my_chat_member'->'chat'->>'id' = pci.channel_user_id))`},
		{&pkg.FieldOrigins, `SELECT fp.field_name, fp.source, fp.captured_by, fp.captured_at, fp.confidence, fp.evidence_ref
		   FROM field_provenance fp
		   WHERE fp.object_type = 'person' AND fp.object_id = $1`},
	}
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
