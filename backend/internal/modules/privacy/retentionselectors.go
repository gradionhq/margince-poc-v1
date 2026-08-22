// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The selector table: which records each (object_type, category) policy scope
// governs. Split out of retention.go because it is also the AUTHORABLE
// VOCABULARY — retentionscope.go derives what an admin may author from exactly
// these keys (GCS-PARAM-8), so the one list serves the evaluator and the
// authoring surface, and neither can drift from the other.
//
// Adding a key here widens what an admin may author AND moves
// MaxPassDuration's bound, which is derived from the count.

// selectors name the records a (object_type, category) policy governs.
// The closed map is deliberate: a policy row with a scope the engine
// does not understand is skipped LOUDLY (logged every pass), never
// half-applied. Every query filters the hold column — and for
// activities, the holds of every linked record plus the statutory floor.
var retentionSelectors = map[string]string{
	"lead/unconverted": `SELECT id FROM lead
		WHERE status IN ('new','contacted','engaged') AND archived_at IS NULL AND NOT legal_hold
		  AND full_name IS DISTINCT FROM 'Anonymized Lead'
		  AND created_at < now() - make_interval(days => $1) LIMIT $2`,
	"activity/": `SELECT a.id FROM activity a
		WHERE a.archived_at IS NULL
		  AND a.occurred_at < now() - make_interval(days => $1)
		  ` + correspondenceFloorPredicate(3, 4) + `
		  AND NOT EXISTS (SELECT 1 FROM activity_link l
		        LEFT JOIN person p ON p.id = l.person_id
		        LEFT JOIN organization o ON o.id = l.organization_id
		        LEFT JOIN deal d ON d.id = l.deal_id
		        LEFT JOIN lead ld ON ld.id = l.lead_id
		        LEFT JOIN project pj ON pj.id = l.project_id
		        WHERE l.activity_id = a.id
		          AND (coalesce(p.legal_hold, false) OR coalesce(o.legal_hold, false) OR coalesce(d.legal_hold, false)
		               OR coalesce(ld.legal_hold, false) OR coalesce(pj.legal_hold, false)))
		LIMIT $2`,
	"activity/transcript": `SELECT a.id FROM activity a
		WHERE a.source_system = 'transcript' AND a.body IS NOT NULL
		  AND a.occurred_at < now() - make_interval(days => $1)
		  ` + correspondenceFloorPredicate(3, 4) + `
		  AND NOT EXISTS (SELECT 1 FROM activity_link l
		        LEFT JOIN person p ON p.id = l.person_id
		        LEFT JOIN organization o ON o.id = l.organization_id
		        LEFT JOIN deal d ON d.id = l.deal_id
		        LEFT JOIN lead ld ON ld.id = l.lead_id
		        LEFT JOIN project pj ON pj.id = l.project_id
		        WHERE l.activity_id = a.id
		          AND (coalesce(p.legal_hold, false) OR coalesce(o.legal_hold, false) OR coalesce(d.legal_hold, false)
		               OR coalesce(ld.legal_hold, false) OR coalesce(pj.legal_hold, false)))
		LIMIT $2`,
	"person/no_consent_no_deal": `SELECT p.id FROM person p
		WHERE p.archived_at IS NULL AND NOT p.legal_hold
		  AND p.full_name IS DISTINCT FROM 'Erased Subject'
		  AND p.created_at < now() - make_interval(days => $1)
		  AND NOT EXISTS (SELECT 1 FROM person_consent pc WHERE pc.person_id = p.id AND pc.state = 'granted')
		  AND NOT EXISTS (SELECT 1 FROM relationship r
		        WHERE r.kind = 'deal_stakeholder' AND r.person_id = p.id AND r.archived_at IS NULL)
		LIMIT $2`,
	"deal/lost": `SELECT id FROM deal
		WHERE status = 'lost' AND archived_at IS NULL AND NOT legal_hold
		  AND closed_at < now() - make_interval(days => $1) LIMIT $2`,
	// deal/won is authorable but NOT seeded: no DM-SEED row plants it, because
	// a won deal is the commercial record of the relationship and the product
	// takes no view on when a workspace stops keeping it. It exists because
	// UC-GDPR-09's main success scenario authors exactly this policy ("archive
	// deal/won after a shorter period than the default") — without a selector
	// the use case's own worked example could not be performed.
	"deal/won": `SELECT id FROM deal
		WHERE status = 'won' AND archived_at IS NULL AND NOT legal_hold
		  AND closed_at < now() - make_interval(days => $1) LIMIT $2`,
	"ai_call_payload/content": `SELECT id FROM ai_call_payload
		WHERE occurred_at < now() - make_interval(days => $1) LIMIT $2`,
}
