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
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

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
	// EnrichedFields is what the system read about the subject from a public
	// page or a mail signature, each with the verbatim text it came from.
	// Art. 15(1)(g) makes the source itself disclosable, and the snippet IS
	// the source.
	EnrichedFields []map[string]any `json:"enriched_fields"`
	// Corrections is what a human recorded over what the system inferred
	// about this subject. It is theirs twice over: the value was typed by a
	// person about them, and the suppressions are the record of which claims
	// this installation has agreed to stop making.
	Corrections []map[string]any `json:"corrections"`
	// ProviderClaims is what a licensed data provider asserted about the
	// subject and this installation retained — bought from a third party
	// rather than given by them, which is precisely the holding Art. 15(1)(g)
	// makes the SOURCE of disclosable too.
	ProviderClaims []map[string]any `json:"provider_claims"`
	// ProviderRuns is why and when that purchase happened: which provider was
	// asked, what was requested, what came back. Art. 15(1)(a)-(d) asks for
	// the purposes and the categories, and a values-only export would answer
	// what we hold while hiding that we went out and bought it.
	ProviderRuns []map[string]any `json:"provider_runs"`
	// What capture decided about the subject's own address, and why — an
	// automated decision the subject is owed sight of (CAP-DDL-8).
	CaptureDispositions []map[string]any `json:"capture_dispositions"`
	// The governed outbound messages addressed to the subject: what was sent
	// to them, when, and whether it left (comms_outbound).
	SentMessages []map[string]any `json:"sent_messages"`
	// The messages addressed to the subject that have NOT been sent: waiting
	// for their moment, withdrawn, or held for a human (scheduled_send,
	// ADR-0104). Held apart from SentMessages because the distinction is the
	// subject's to know — one is a message they received, the other is one
	// somebody wrote to them that the system is still holding.
	ScheduledMessages []map[string]any `json:"scheduled_messages"`
	// The messages written to the subject that nobody has DECIDED yet: an
	// automation composed them and they are waiting in somebody's approval
	// inbox (#707). Held apart from ScheduledMessages for the same reason that
	// is held apart from SentMessages — the distinction is the subject's to
	// know. One is a message with a moment already chosen; this is one a
	// colleague has not yet agreed to send at all, and may never.
	StagedMessages []map[string]any `json:"staged_messages"`
}

// AssembleSAR builds the package. It is a privileged read: the caller must be
// a human holding the person.delete grant (the same trust level erasure needs)
// over an unbounded row scope — see the checks below.
func AssembleSAR(ctx context.Context, db *database.DB, personID ids.PersonID) (SARPackage, error) {
	if err := auth.Require(ctx, "person", principal.ActionDelete); err != nil {
		return SARPackage{}, err
	}
	// Human-only, for the reason the grant above cannot express: an agent
	// acting under a passport carries the granting human's live grants, so an
	// admin's read-scoped passport would otherwise assemble a subject's entire
	// Art. 15 package — every activity, capture row, correction and outbound
	// message. Nothing wires this to a route yet, and the arm goes in now
	// rather than being discovered missing by whoever does.
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return SARPackage{}, fmt.Errorf("human-only subject-access assembly: %w", apperrors.ErrPermissionDenied)
	}
	// The assembly deliberately crosses the caller's row scope — Art. 15 owes
	// the subject everything held, not the slice one rep may see — so a bounded
	// caller cannot run it. Scope is the second condition, not a stand-in for
	// authority: the person.delete grant above is what limits this to the roles
	// trusted with erasure, and it is what keeps read_only out.
	if !auth.Unbounded(actor) {
		return SARPackage{}, apperrors.ErrPermissionDenied
	}
	var pkg SARPackage
	err := db.Tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleForSubjectRights(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		// The subject's addresses and lead twins, read BEFORE the sections run:
		// the staged-approvals section matches on them, and unlike erasure this
		// path destroys nothing, so they are still there to read.
		emails, leads, err := subjectReach(ctx, tx, personID)
		if err != nil {
			return err
		}
		sections := sarSections(&pkg, personID, emails, leads)

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
			args := section.args
			if args == nil {
				args = []any{personID}
			}
			rows, err := rowMaps(ctx, tx, section.query, args...)
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
	// args overrides the default single person-id argument. Only the staged
	// approvals section needs it: it reaches rows by the subject's addresses
	// and lead twins as well as by their person id, through the SAME predicate
	// erasure uses — and that predicate takes those as bound parameters rather
	// than rebuilding them in SQL, so the export and the erasure cannot come to
	// different conclusions about which rows are the subject's.
	args []any
}

// sarSections is the Art. 15 gather list: the exact set of tables that hold
// data about the subject, each bound to the package field it populates. The
// query set is compliance-critical — adding or dropping a source changes what
// the export owes the data subject. It is assembled chapter by chapter, and the
// order the chapters concatenate in is the order the export runs them in.
func sarSections(pkg *SARPackage, personID ids.PersonID, emails []string, leads []ids.UUID) []sarSection {
	sections := sarIdentitySections(pkg)
	sections = append(sections, sarRecordSections(pkg)...)
	sections = append(sections, sarMessagingSections(pkg, personID, emails, leads)...)
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
		{&pkg.Emails, `SELECT email, email_type, is_primary, archived_at FROM person_email WHERE person_id = $1`, nil},
		{&pkg.Phones, `SELECT phone, phone_type, archived_at FROM person_phone WHERE person_id = $1`, nil},
		{&pkg.ChannelIdentities, `SELECT provider, channel_user_id, username, blocked_at, source, created_at, archived_at
		   FROM person_channel_identity WHERE person_id = $1`, nil},
		{&pkg.InteractionParticipation, `SELECT ap.activity_id, ap.role, ap.address, ap.created_at,
		       a.kind, a.occurred_at, a.direction
		   FROM activity_participant ap
		   JOIN activity a ON a.id = ap.activity_id
		  WHERE ap.person_id = $1
		     OR (ap.address IS NOT NULL AND ap.address IN (
		         SELECT lower(email) FROM person_email WHERE person_id = $1))`, nil},
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
		                AND r.organization_id = g.matched_org_id))`, nil},
	}
}

// sarRecordSections gather the business records the subject appears in: who
// they are connected to, the deals they hold a stake in, the leads they came
// from, and their timeline with the files hanging off it.
func sarRecordSections(pkg *SARPackage) []sarSection {
	return []sarSection{
		{&pkg.Relationships, `SELECT kind, organization_id, deal_id, role, started_at, ended_at
		   FROM relationship WHERE person_id = $1 AND archived_at IS NULL`, nil},
		{&pkg.Deals, `SELECT d.id, d.name, d.status, d.amount_minor, d.currency
		   FROM deal d JOIN relationship r ON r.deal_id = d.id
		   WHERE r.kind = 'deal_stakeholder' AND r.person_id = $1 AND r.archived_at IS NULL`, nil},
		{&pkg.Leads, `SELECT l.id, l.full_name, l.email, l.title, l.company_name, l.status, l.created_at
		   FROM lead l
		   WHERE l.promoted_person_id = $1
		      OR l.id IN (SELECT converted_from_lead_id FROM person WHERE id = $1 AND converted_from_lead_id IS NOT NULL)
		      OR (l.email IS NOT NULL AND EXISTS (
		            SELECT 1 FROM person_email pe WHERE pe.person_id = $1 AND pe.email = lower(l.email)))`, nil},
		// Art. 15 reaches a record the statutory floor is HOLDING about this
		// subject, and does so deliberately: the restriction bars further
		// PROCESSING of the record, not the subject's own access to what is
		// held about them (A165/ADR-0114 — the subject's Art. 15 access is
		// named among what does not change). Every other reader in this tree
		// excludes `restricted_at IS NOT NULL`; this is the one that must not,
		// and saying so here is what keeps the omission a decision rather than
		// an oversight the next reader "fixes".
		{&pkg.Activities, `SELECT a.id, a.kind, a.subject, a.body, a.occurred_at, a.source_system
		   FROM activity a JOIN activity_link l ON l.activity_id = a.id
		   WHERE l.person_id = $1`, nil},
		{&pkg.Attachments, `SELECT at.id, at.entity_type, at.entity_id, at.filename,
		      at.content_type, at.byte_size, at.created_at
		   FROM attachment at
		   WHERE (at.entity_type = 'person' AND at.entity_id = $1)
		      OR (at.entity_type = 'activity' AND at.entity_id IN (
		            SELECT l.activity_id FROM activity_link l WHERE l.person_id = $1))`, nil},
	}
}

// sarConsentSections gather the per-purpose consent state and the proof log
// behind it — what the subject agreed to, and every change of mind on record.
func sarConsentSections(pkg *SARPackage) []sarSection {
	return []sarSection{
		{&pkg.Consent, `SELECT cp.key AS purpose, pc.state, pc.lawful_basis, pc.captured_at
		   FROM person_consent pc JOIN consent_purpose cp ON cp.id = pc.purpose_id
		   WHERE pc.person_id = $1`, nil},
		{&pkg.ConsentEvents, `SELECT cp.key AS purpose, ce.new_state, ce.source, ce.captured_at
		   FROM consent_event ce JOIN consent_purpose cp ON cp.id = ce.purpose_id
		   WHERE ce.person_id = $1`, nil},
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
		                      OR rc.payload->'my_chat_member'->'chat'->>'id' = pci.channel_user_id))`, nil},
		{&pkg.FieldOrigins, `SELECT fp.field_name, fp.source, fp.captured_by, fp.captured_at, fp.confidence, fp.evidence_ref
		   FROM field_provenance fp
		   WHERE fp.object_type = 'person' AND fp.object_id = $1`, nil},
		// The STORED value, not the one the 360 renders. person360's
		// readProfileFields overlays whatever verdict a human recorded, because
		// a page showing the machine's claim as fact would be showing a claim
		// its reader already overrode. An export is the other obligation: it
		// owes the subject what this installation HOLDS, and it holds both the
		// enriched value and the correction — which travels beside it in
		// Corrections below, as its own section. Merging them here would hand
		// the subject one value and conceal that the override exists at all.
		{&pkg.EnrichedFields, `SELECT ppf.field, ppf.value, ppf.evidence_snippet, ppf.source_ref,
		          ppf.confidence, ppf.source, ppf.captured_by, ppf.updated_at
		   FROM person_profile_field ppf
		   WHERE ppf.person_id = $1`, nil},
		// claim_key is exported as it stands: it is a hash of the claim's
		// path, so it names WHICH claim was decided without carrying the
		// asserted value, which the ledger never stores in the first place.
		{&pkg.Corrections, `SELECT af.claim_kind, af.claim_key, af.verdict, af.corrected_value, af.note,
		          af.captured_by, af.created_at, af.updated_at
		   FROM ai_feedback af
		   WHERE af.subject_type = 'person' AND af.subject_id = $1`, nil},
		// validation_status is deliberately absent: no writer populates that
		// column, and the per-value validation the provider reports lives
		// INSIDE value_json, which this exports whole. A column exported as
		// null on every row would tell the subject their address was never
		// validated, which is not what the platform knows.
		{&pkg.ProviderClaims, `SELECT ppc.provider, ppc.claim_key, ppc.value_json, ppc.confidence,
		          ppc.source, ppc.captured_by, ppc.retrieved_at
		   FROM person_provider_claim ppc
		   WHERE ppc.person_id = $1`, nil},
		// The run history carries no credential and no vault reference: the
		// closed safe status code is a product reason, never a provider body.
		{&pkg.ProviderRuns, `SELECT pr.provider, pr.trigger, pr.state, pr.skip_reason,
		          pr.requested_categories, pr.claims_unwritten, pr.last_safe_status_code,
		          pr.submitted_at, pr.completed_at, pr.created_at
		   FROM provider_run pr
		   WHERE pr.person_id = $1`, nil},
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

// subjectReach reads the two things beyond a person id that identify the
// subject in a staged proposal: the addresses a message to them carries, and
// the lead rows that were the same human before promotion.
//
// Read here rather than derived inside the query so the export can hand the
// SAME bound parameters to subjectApprovalMatch that the erasure hands it.
// Erasure cannot read them at that point — it has already destroyed them — so
// the predicate takes them as arguments and each caller supplies them from
// wherever it still can.
func subjectReach(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]string, []ids.UUID, error) {
	emails, err := subjectStrings(ctx, tx,
		`SELECT email FROM person_email WHERE person_id = $1 AND email <> ''`, personID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the subject's addresses for the export: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT id FROM lead WHERE promoted_person_id = $1`, personID.UUID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the subject's lead twins for the export: %w", err)
	}
	defer rows.Close()
	leads := []ids.UUID{}
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		leads = append(leads, id)
	}
	return emails, leads, rows.Err()
}

// subjectStrings runs a one-column text query into a slice.
func subjectStrings(ctx context.Context, tx pgx.Tx, query string, personID ids.PersonID) ([]string, error) {
	rows, err := tx.Query(ctx, query, personID.UUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
