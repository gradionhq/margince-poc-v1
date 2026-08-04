// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The sections read from the timeline and its neighbours: recent activity,
// open tasks, the two last-touch directions, who knows this contact, the
// consent guard, the enrichment evidence, and the visit delta.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// personLinkedActivity is the reachability predicate the person timeline
// uses: an activity linked to this person. It is the same link table the
// entity-scoped activity list walks, so the 360's recent rows and the full
// timeline agree about what belongs to this contact.
const personLinkedActivity = `EXISTS (
	SELECT 1 FROM activity_link l
	WHERE l.activity_id = a.id AND l.person_id = $%d)`

// activityScope renders the caller's activity row scope, defaulting to the
// permissive clause when the scope adds no predicate of its own.
func activityScope(ctx context.Context, arg func(any) int) (string, error) {
	clause, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return "true", nil
	}
	return clause, nil
}

// activitiesSection is the recent timeline — a summary, not a paging
// surface: page two comes from GET /activities with its own cursor.
func (s *Service) activitiesSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "activity"); err != nil {
		return err
	}
	rows, hasMore, err := s.readActivities(ctx, tx, personID, "")
	if err != nil {
		return err
	}
	out.Activities = &struct {
		Data []crmcontracts.Activity `json:"data"`
		Page crmcontracts.PageInfo   `json:"page"`
	}{Data: rows, Page: crmcontracts.PageInfo{HasMore: hasMore}}
	return nil
}

// nextStepsSection is the open work filed against this person: tasks not
// yet done. A task with no due date still counts — it is owed either way.
func (s *Service) nextStepsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "activity"); err != nil {
		return err
	}
	rows, hasMore, err := s.readActivities(ctx, tx, personID,
		`AND a.kind = 'task' AND coalesce(a.is_done, false) = false`)
	if err != nil {
		return err
	}
	out.NextSteps = &struct {
		Data []crmcontracts.Activity `json:"data"`
		Page crmcontracts.PageInfo   `json:"page"`
	}{Data: rows, Page: crmcontracts.PageInfo{HasMore: hasMore}}
	return nil
}

// readActivities is the shared body of the timeline and next-step reads.
func (s *Service) readActivities(ctx context.Context, tx pgx.Tx, personID ids.PersonID, extra string) ([]crmcontracts.Activity, bool, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	scope, err := activityScope(ctx, arg)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT a.id, a.kind, a.subject, a.body, a.direction, a.occurred_at,
		       a.due_at, a.is_done, a.source, a.captured_by, a.created_at
		FROM activity a
		WHERE a.archived_at IS NULL AND %s AND (%s) %s
		ORDER BY a.occurred_at DESC, a.id DESC
		LIMIT %d`,
		fmt.Sprintf(personLinkedActivity, personPos), scope, extra, sectionCap+1), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]crmcontracts.Activity, 0, sectionCap)
	for rows.Next() {
		var a crmcontracts.Activity
		var id ids.UUID
		if err := rows.Scan(&id, &a.Kind, &a.Subject, &a.Body, &a.Direction,
			&a.OccurredAt, &a.DueAt, &a.IsDone, &a.Source, &a.CapturedBy, &a.CreatedAt); err != nil {
			return nil, false, err
		}
		a.Id = openapi_types.UUID(id)
		// The link is implied by the read — every row here is linked to this
		// person — so the payload carries the id rather than re-reading
		// activity_link for a fact the query already asserted.
		a.Links = &[]crmcontracts.ActivityLink{{
			EntityType: crmcontracts.ActivityLinkEntityTypePerson,
			EntityId:   openapi_types.UUID(personID.UUID),
		}}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > sectionCap
	if hasMore {
		out = out[:sectionCap]
	}
	return out, hasMore, nil
}

// lastTouchSection reads the two directions separately. Folding them into
// one "last touch" hides the only distinction a reader acts on: a contact
// we mailed a fortnight ago with no reply and one who wrote to us this
// morning have the same last-touch date and opposite meanings.
func (s *Service) lastTouchSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "activity"); err != nil {
		return err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	scope, err := activityScope(ctx, arg)
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT max(a.occurred_at) FILTER (WHERE a.direction = 'inbound'),
		       max(a.occurred_at) FILTER (WHERE a.direction = 'outbound')
		FROM activity a
		WHERE a.archived_at IS NULL AND %s AND (%s)`,
		fmt.Sprintf(personLinkedActivity, personPos), scope), args...).
		Scan(&out.LastInboundAt, &out.LastOutboundAt)
}

// networkSection answers "who here knows them", warmest first — the
// ordering IS the answer, so it over-fetches and ranks before capping,
// exactly as GET /people/{id}/network does.
func (s *Service) networkSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time, out *crmcontracts.Person360) error {
	edges, err := search.EdgesForPerson(ctx, tx, personID.UUID, networkFetch)
	if err != nil {
		return err
	}
	search.SortByStrength(edges, now)
	if len(edges) > networkCap {
		edges = edges[:networkCap]
	}
	names, err := network.UserNames(ctx, tx, network.EdgeUsers(edges))
	if err != nil {
		return err
	}
	colleagues := make([]crmcontracts.PersonNetworkColleague, 0, len(edges))
	for _, e := range edges {
		colleagues = append(colleagues, network.WireColleague(e, names[e.UserID], now))
	}
	out.Network = &struct {
		Colleagues []crmcontracts.PersonNetworkColleague `json:"colleagues"`
	}{Colleagues: colleagues}
	return nil
}

// networkCap and networkFetch mirror the standalone network endpoint: the
// record page must not name a different strongest colleague than the card.
const (
	networkCap   = 10
	networkFetch = 100
)

// consentSection is the outbound guard, not the ledger: per-purpose state
// only. The append-only proof log stays at GET /people/{id}/consent.
func (s *Service) consentSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	states, _, err := s.consent.PersonConsentTx(ctx, tx, personID)
	if err != nil {
		return err
	}
	wire := make([]crmcontracts.PersonConsentState, 0, len(states))
	for _, st := range states {
		s := crmcontracts.PersonConsentState{
			PurposeId:              openapi_types.UUID(st.PurposeID.UUID),
			State:                  crmcontracts.PersonConsentStateState(st.State),
			LawfulBasis:            st.LawfulBasis,
			DoubleOptInConfirmedAt: st.DoubleOptInConfirmedAt,
			UpdatedAt:              st.UpdatedAt,
		}
		if st.PurposeKey != "" {
			key := st.PurposeKey
			s.PurposeKey = &key
		}
		wire = append(wire, s)
	}
	out.Consent = &struct {
		State []crmcontracts.PersonConsentState `json:"state"`
	}{State: wire}
	return nil
}

// profileFieldsSection is the enrichment evidence sidecar. Evidence-or-omit
// is enforced at write time (the snippet column is NOT NULL), so every row
// here can show the reader the text its value was read from.
func (s *Service) profileFieldsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	fields, err := s.readProfileFields(ctx, tx, personID)
	if err != nil {
		return err
	}
	out.ProfileFields = &fields
	return nil
}

// profileFieldClaimPath names one enriched field as a claim. It is a function
// rather than a format string at each call site so the page that RENDERS the
// key and the ledger that stores it cannot spell it differently — a mismatch
// would silently lose every correction.
func profileFieldClaimPath(field string) string { return "profile_field:" + field }

// readProfileFields is EVERY read of person_profile_field — the 360 section
// and the standalone sidecar endpoint both come through here.
//
// That matters because the human's verdict is folded in below. A corrected
// value rendered without its marker reads as the machine's assertion, which is
// exactly the claim the human overrode, so consulting the ledger cannot be one
// caller's job: a second read path that skipped it would keep serving the
// rejected value on a surface nobody thought to check.
func (s *Service) readProfileFields(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]crmcontracts.PersonProfileField, error) {
	rows, err := tx.Query(ctx, `
		-- updated_at, not created_at: this is when the value took its CURRENT
		-- form, which is the date the receipt should show after a human edit.
		SELECT field, value, evidence_snippet, source_ref, confidence, source, captured_by, updated_at
		FROM person_profile_field
		WHERE person_id = $1
		ORDER BY field`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]crmcontracts.PersonProfileField, 0, 5)
	for rows.Next() {
		var f crmcontracts.PersonProfileField
		var field string
		if err := rows.Scan(&field, &f.Value, &f.EvidenceSnippet, &f.SourceRef,
			&f.Confidence, &f.Source, &f.CapturedBy, &f.CapturedAt); err != nil {
			return nil, err
		}
		f.Field = crmcontracts.PersonProfileFieldField(field)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.applyFieldVerdicts(ctx, tx, personID, out)
}

// applyFieldVerdicts overlays what a human already decided about each field.
func (s *Service) applyFieldVerdicts(
	ctx context.Context,
	tx pgx.Tx,
	personID ids.PersonID,
	fields []crmcontracts.PersonProfileField,
) ([]crmcontracts.PersonProfileField, error) {
	verdicts, err := s.feedback.VerdictsForTx(ctx, tx, "person", personID.UUID)
	if err != nil {
		return nil, err
	}
	for i := range fields {
		f := &fields[i]
		claim := profileFieldClaimPath(string(f.Field))
		f.ClaimKey = &claim
		v, found := verdicts[ai.VerdictLookupKey(ai.ClaimProfileField, ai.ClaimKey(claim))]
		if !found {
			continue
		}
		verdict := crmcontracts.PersonProfileFieldVerdict(v.Verdict)
		f.Verdict = &verdict
		f.VerdictNote = v.Note
		if v.Verdict == ai.VerdictCorrected && v.CorrectedValue != nil {
			// The human's value stands. The captured snippet is left in place
			// beneath it on purpose — what the machine read is still the
			// evidence for why it got this wrong, and hiding it would leave the
			// correction unexplainable.
			f.Value = *v.CorrectedValue
		}
	}
	return fields, nil
}

// sinceLastVisitSection counts what arrived since the caller's own
// baseline. READ-ONLY: nothing here advances the mark — only view-ack does,
// because a GET that moved it would destroy the answer the caller opened
// the page to read.
func (s *Service) sinceLastVisitSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "activity"); err != nil {
		return err
	}
	var view crmcontracts.Person360SinceLastVisit
	since, visited, err := s.baselineFor(ctx, tx, personID)
	if err != nil {
		return err
	}
	if visited {
		view.BaselineAt = &since
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos, sincePos := arg(personID), arg(since)
	scope, err := activityScope(ctx, arg)
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*)
		FROM activity a
		WHERE a.archived_at IS NULL AND a.created_at > $%d AND %s AND (%s)`,
		sincePos, fmt.Sprintf(personLinkedActivity, personPos), scope), args...).
		Scan(&view.NewActivities); err != nil {
		return fmt.Errorf("count new activities: %w", err)
	}
	out.SinceLastVisit = &view
	return nil
}

// actingUser resolves the user a baseline belongs to. It answers for agents
// too — an agent's UserID is the granting human's — so it is a lookup, not
// a gate: Acknowledge's auth.RequireHuman is what keeps an agent from
// writing that human's mark.
func actingUser(ctx context.Context) (ids.UserID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return ids.UserID{}, fmt.Errorf(
			"the visit baseline is per-user and this call carries no user: %w",
			apperrors.ErrPermissionDenied)
	}
	return ids.From[ids.UserKind](p.UserID), nil
}

// baselineFor reads the caller's own mark. The user_id predicate is
// explicit: RLS binds the workspace, so without it one rep would read
// another rep's reading history.
func (s *Service) baselineFor(ctx context.Context, tx pgx.Tx, personID ids.PersonID) (at time.Time, visited bool, err error) {
	userID, err := actingUser(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	err = tx.QueryRow(ctx, `
		SELECT last_viewed_at FROM user_record_view
		WHERE user_id = $1 AND entity_type = $2 AND entity_id = $3`,
		userID, entityTypePerson, personID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}

func ptr[T any](v T) *T { return &v }
