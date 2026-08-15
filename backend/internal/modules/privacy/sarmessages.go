// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The messages half of a subject-access package: what was captured, what was
// sent to the subject, and what is still waiting to be sent to them. Split from
// sar.go because these three projections carry the file's hardest rule — which
// addresses may be disclosed to whom — and it is easier to check when it is not
// interleaved with the identity and provenance sections.

import "github.com/gradionhq/margince/backend/internal/shared/kernel/ids"

// sarMessagingSections gather both directions of the messaging boundary: what
// capture decided about mail arriving from the subject, and what this
// installation sent out about or to them.
func sarMessagingSections(pkg *SARPackage, personID ids.PersonID, emails []string, leads []ids.UUID) []sarSection {
	return []sarSection{
		{&pkg.CaptureDispositions, `SELECT p.email, p.display_name, p.status, p.disposition_reason, p.created_at, p.resolved_at
		   FROM capture_pending_counterparty p
		   WHERE p.email IN (SELECT email FROM person_email WHERE person_id = $1)`, nil},
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
		// html_body rides beside body rather than instead of it: a message
		// carrying both is ONE message the subject received in two renderings,
		// and disclosing only the plain one withholds markup the system still
		// holds about them. from_name joins them for the same reason: this
		// projection discloses the message AS THE SUBJECT RECEIVED IT, and who
		// it appeared to be from is part of what they were shown.
		//
		// The address match spans bcc, and the DISCLOSURE of it is narrowed to
		// the subject's own address.
		//
		// Both halves are required and they pull opposite ways. A person bcc'd
		// on a message with no activity link would otherwise be absent from
		// their own export of a message they received — and exporting the
		// whole bcc array would hand one subject every other blind recipient's
		// address, which is the disclosure a blind copy exists to prevent. So
		// the row is FOUND on any addressee and the blind list is REDUCED to
		// the asker.
		//
		// attachments too, and it is not covered by the attachment query
		// below: that one finds files hanging off the subject or an activity
		// linked to them, while a send may carry any file its sender could see
		// — one attached to an organization or a deal reaches the subject
		// without ever being attached TO them.
		{&pkg.SentMessages, `SELECT o.subject, o.body, o.html_body, o.from_name, o.attachments, o.recipients, o.cc,
		      (SELECT coalesce(jsonb_agg(addr), '[]'::jsonb)
		         FROM jsonb_array_elements_text(coalesce(o.bcc, '[]'::jsonb)) AS addr
		        WHERE lower(addr) IN (SELECT email FROM person_email WHERE person_id = $1)) AS bcc,
		      o.consent_purpose,
		      o.provider, o.channel_user_id, o.status, o.sent_at, o.created_at
		   FROM comms_outbound o
		   WHERE o.activity_id IN (SELECT l.activity_id FROM activity_link l WHERE l.person_id = $1)
		      OR EXISTS (
		           SELECT 1 FROM jsonb_array_elements_text(
		                          o.recipients || o.cc || coalesce(o.bcc, '[]'::jsonb)) AS addr
		           WHERE lower(addr) IN (SELECT email FROM person_email WHERE person_id = $1))`, nil},
		// The messages nobody has sent yet. They hold the subject's address and
		// the body BEFORE any activity exists, so none of the three routes the
		// query above takes can reach them — and a message written to this
		// person, sitting unsent, is data held about them that Art. 15 owes
		// them sight of.
		//
		// Same two-sided address rule as the sent projection, pulling the same
		// two ways: the row is FOUND on any addressee including a blind one, so
		// a bcc'd subject is not absent from their own export; and the blind
		// list is REDUCED to the asker, so they do not learn who else was
		// blind-copied. Exporting the whole bcc array would hand one subject
		// every other blind recipient's address, which is the disclosure a
		// blind copy exists to prevent.
		//
		// status and held_reason are disclosed because they are the honest
		// answer to "what is happening to this message" — waiting, withdrawn,
		// or stopped by a gate and waiting for a human. Showing the content
		// while hiding the state would tell the subject a message exists
		// without telling them whether it is still going to arrive.
		{&pkg.ScheduledMessages, `SELECT s.payload->>'subject' AS subject,
		      s.payload->>'body' AS body,
		      s.payload->>'html_body' AS html_body,
		      -- The To LINE, derived by subtracting cc and bcc from the merged
		      -- list. payload.recipients is the CONSENT superset — every To, Cc
		      -- AND Bcc address — which is the right shape for a gate and the
		      -- wrong one to disclose: handing it over verbatim would give this
		      -- subject every other blind recipient's address, which is exactly
		      -- what narrowing the bcc column below exists to prevent. The sent
		      -- projection above needs no such subtraction because
		      -- comms_outbound keeps the To line and the blind list apart.
		      --
		      -- Compared case-insensitively and trimmed, which is what
		      -- activities.normalizeAddress does on the send side. Matching raw
		      -- would leave a differently-cased blind address in the To line
		      -- here while the send itself correctly removed it — the one place
		      -- the two derivations must not disagree.
		      (SELECT coalesce(jsonb_agg(addr), '[]'::jsonb)
		         FROM jsonb_array_elements_text(coalesce(s.payload->'recipients', '[]'::jsonb)) AS addr
		        WHERE lower(btrim(addr)) NOT IN (
		                SELECT lower(btrim(c))
		                  FROM jsonb_array_elements_text(coalesce(s.payload->'cc', '[]'::jsonb)) AS c
		                UNION ALL
		                SELECT lower(btrim(b))
		                  FROM jsonb_array_elements_text(coalesce(s.payload->'bcc', '[]'::jsonb)) AS b)) AS recipients,
		      s.payload->'cc' AS cc,
		      (SELECT coalesce(jsonb_agg(addr), '[]'::jsonb)
		         FROM jsonb_array_elements_text(coalesce(s.payload->'bcc', '[]'::jsonb)) AS addr
		        WHERE lower(addr) IN (SELECT email FROM person_email WHERE person_id = $1)) AS bcc,
		      s.payload->>'consent_purpose' AS consent_purpose,
		      s.status, s.held_reason, s.scheduled_at, s.scheduled_tz, s.created_at
		   FROM scheduled_send s
		   WHERE EXISTS (
		           SELECT 1 FROM jsonb_array_elements_text(
		                          coalesce(s.payload->'recipients', '[]'::jsonb)
		                          || coalesce(s.payload->'cc', '[]'::jsonb)
		                          || coalesce(s.payload->'bcc', '[]'::jsonb)) AS addr
		           WHERE lower(addr) IN (SELECT email FROM person_email WHERE person_id = $1))`, nil},
		// The messages still waiting for a human to decide them. A staged
		// approval holds a whole composed message before any scheduled row or
		// activity exists, so neither section above can see it — and a subject
		// asking what this installation holds about them is owed a draft
		// somebody is about to send them just as much as one already sent.
		//
		// Matched on the payload as TEXT rather than on a shape, because there
		// is no shape to match: proposed_change is per-kind JSON this package
		// does not parse. The looseness errs toward including a proposal that
		// merely mentions the subject, which is the right direction for an
		// export — Art. 15 owes them what is held, and a staged message naming
		// them is held about them whatever kind wrote it.
		//
		// Loose about SHAPE is not loose about WHO, and this is the half that
		// bites: an address dropped into a substring match hands a THIRD PARTY's
		// composed message to whoever asked. subjectApprovalMatch anchors it,
		// and the export shares that predicate with the erasure so a subject
		// cannot be told two different things about which rows are theirs.
		//
		// The export then reaches PAST it, onto the quotations, and the
		// containment runs one way ON PURPOSE: everything the erasure destroys
		// is listed here, and the export lists more besides. A row this reaches
		// and the cascade leaves alone is a proposal read out of a record the
		// cascade is not entitled to destroy — held, floor-shielded, or shared
		// with somebody else — and the subject is owed sight of it either way.
		// The reverse containment is the one that must never hold: a row
		// destroyed and never listed tells a subject nothing was staged about
		// them and then destroys it on the strength of the same reading.
		//
		// The proposal is returned whole, for the same reason the sent
		// messages' recipient lists are: this assembly is admin-mediated, so
		// the disclosure is a human handing a package to a subject rather than
		// an endpoint answering one.
		//
		// Evidence rides along because it is the part of a staging held in the
		// subject's OWN words — the verbatim lines a claim was read out of, a
		// sentence they spoke in a meeting. The proposal is what this
		// installation concluded; the evidence is what it kept of them in order
		// to conclude it, which is the half an Art. 15 answer would be strangest
		// to omit.
		//
		// It is the one column here that is NARROWED rather than returned whole,
		// and the bcc lists above are narrowed for the same reason. Everything
		// else on this row is text this installation COMPOSED — a summary, a
		// proposal — and the loose arms match on exactly that. Evidence is not
		// composed: it is a raw line lifted out of some record, so a row matched
		// because a summary mentions the subject's address would otherwise hand
		// them a verbatim sentence out of a meeting they were never part of,
		// about people they have no relationship to. So the ROW is found by any
		// arm, and the QUOTATIONS are reduced to the ones that are the subject's
		// to see: read out of a record they are linked to, or naming them
		// outright. The rest belongs to another record.
		//
		// Per ITEM, not per row, because evidence is per-claim: a proposal
		// asserting two things cites two sources, and only one of them may be
		// theirs.
		{
			&pkg.StagedMessages, `SELECT id, kind, status, summary, proposed_change,
		      (SELECT coalesce(jsonb_agg(item), '[]'::jsonb)
		         FROM jsonb_array_elements(` + evidenceArray + `) AS item
		        WHERE item->>'source_id' IN (
		                SELECT l.activity_id::text FROM activity_link l WHERE l.person_id = $1)
		           OR item->>'evidence_snippet' ~* ANY($3::text[])) AS evidence,
		      created_at, expires_at, decided_at
		   FROM approval
		   WHERE (` + subjectApprovalMatch + `)
		      OR evidence::text ~* ANY($3::text[])
		      OR ` + evidenceCitesSubjectActivity,
			[]any{personID.UUID, leads, addressPatterns(emails)},
		},
	}
}
