// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Meeting scheduling (features/07 §5c): availability is a 🟢 read that
// PROPOSES, booking is the 🟡 action that commits. Until a calendar
// connector is connected, free/busy derives from the CRM's own record
// — the host's meeting activities in the window — which is exactly the
// single-source-of-truth posture: the CRM cannot see a calendar it was
// never granted, and says so by construction rather than pretending.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"

	"github.com/jackc/pgx/v5"
)

// Business-hours envelope for proposed slots and the assumed length of
// a meeting whose end the record does not carry (activity has only
// occurred_at). Both refine when a real calendar connector lands.
// defaultSlotDuration applies inside Availability when the caller
// names no duration, so the REST and MCP transports cannot drift.
const (
	businessDayStartHour   = 9
	businessDayEndHour     = 17
	assumedMeetingDuration = time.Hour
	maxProposedSlots       = 20
	maxAvailabilityWindow  = 31 * 24 * time.Hour
	defaultSlotDuration    = 30 * time.Minute
	minSlotDuration        = 15 * time.Minute
	maxSlotDuration        = 8 * time.Hour
)

// SchedulingArgumentError refuses a from/to/start/end/duration combination no
// calendar can answer. Availability and booking share it because they refuse the
// same shape of mistake about the same kind of argument.
//
// Field carries a wire field name and nothing else: both surfaces publish it as
// the MACHINE field — REST as details.errors[].field, the MCP dispatcher as the
// field token in `<field>=<code>` — so an explanation there is unreadable by the
// only party that would act on it. The explanation is Message. And the Code says
// what is actually wrong: a value that was supplied and merely inconsistent is
// not `required`.
type SchedulingArgumentError struct {
	Field   string
	Code    string
	Message string
}

func (e *SchedulingArgumentError) Error() string { return e.Field + ": " + e.Message }

// FieldFault names the argument, the code, and what to fix — one mapping for
// every surface.
func (e *SchedulingArgumentError) FieldFault() (field, code, message string) {
	return e.Field, e.Code, e.Message
}

// The scheduling argument refusals. Package-level values, not built at the call
// site: every bound in the text is read off the constant that enforces it, so a
// bound and its message cannot drift apart.
var (
	errAvailabilityToNotAfterFrom = &SchedulingArgumentError{
		Field: "to", Code: "invalid_date_range",
		Message: "`to` must be later than `from`",
	}
	errAvailabilityWindowTooWide = &SchedulingArgumentError{
		Field: "to", Code: "window_too_wide",
		Message: fmt.Sprintf("the window between `from` and `to` may span at most %d days",
			int(maxAvailabilityWindow/(24*time.Hour))),
	}
	errAvailabilityDurationOutOfRange = &SchedulingArgumentError{
		Field: "duration_minutes", Code: "out_of_range",
		Message: fmt.Sprintf("`duration_minutes` must be between %d and %d",
			int(minSlotDuration.Minutes()), int(maxSlotDuration.Minutes())),
	}
	// The booking twin of errAvailabilityToNotAfterFrom. book_meeting's StageInfo
	// pre-empts the same condition in its own terms so no approval is minted for
	// an unbookable slot; this is what makes it true on every path that does not
	// stage, REST included.
	errBookingEndNotAfterStart = &SchedulingArgumentError{
		Field: "end", Code: "invalid_date_range",
		Message: "`end` must be later than `start`",
	}
)

type slot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Availability computes free slots for one host inside the window:
// business-hour candidates minus the host's existing meetings. A
// non-positive duration means the caller named none and takes the
// default.
//
// truncated reports that at least one more free slot exists after the last one
// returned. It is returned rather than left implicit because every caller puts
// these slots on a wire, and a capped list presented as the whole truth is how a
// model comes to tell someone there is no later opening. To reach the rest, ask
// again with `from` after the last slot returned.
func (s *Store) Availability(ctx context.Context, host ids.UserID, from, to time.Time, duration time.Duration) (free []slot, truncated bool, err error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, false, err
	}
	if duration <= 0 {
		duration = defaultSlotDuration
	}
	if !to.After(from) {
		return nil, false, errAvailabilityToNotAfterFrom
	}
	if to.Sub(from) > maxAvailabilityWindow {
		return nil, false, errAvailabilityWindowTooWide
	}
	if duration < minSlotDuration || duration > maxSlotDuration {
		return nil, false, errAvailabilityDurationOutOfRange
	}

	// The busy read is a read of the host's meetings and carries the
	// activity row scope: a caller sees only the busy blocks whose
	// linked records their timeline would show. A hidden meeting can
	// still surface as slot_taken at booking time — that reveals one
	// bit at one attempted slot, not another rep's calendar.
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	hostPos := arg(host)
	fromPos := arg(from.Add(-assumedMeetingDuration))
	toPos := arg(to)
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, false, err
	}
	if scope == "" {
		scope = "TRUE"
	}

	var busy []slot
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT a.occurred_at FROM activity a
			WHERE a.kind = 'meeting' AND a.archived_at IS NULL
			  AND a.host_user_id = $%d
			  AND a.occurred_at BETWEEN $%d AND $%d
			  AND %s
			ORDER BY a.occurred_at`, hostPos, fromPos, toPos, scope), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var start time.Time
			if err := rows.Scan(&start); err != nil {
				return err
			}
			busy = append(busy, slot{Start: start, End: start.Add(assumedMeetingDuration)})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}

	slots, truncated := freeSlots(from, to, duration, busy)
	return slots, truncated, nil
}

// freeSlots walks the duration-aligned candidate grid inside the window
// and keeps business-hour slots that miss every busy block. Candidates
// align to the duration grid, never before the caller's window, and
// must END inside business hours (17:00 sharp is fine, 17:01 is not).
//
// truncated reports that a further free slot EXISTS beyond the ones returned —
// established by finding it, not inferred from hitting the cap. A window holding
// exactly maxProposedSlots free slots withheld nothing, and saying otherwise
// would send a caller looking for slots that are not there.
func freeSlots(from, to time.Time, duration time.Duration, busy []slot) (free []slot, truncated bool) {
	// Empty, never nil: "the host is booked solid" is a real answer and arrives
	// shaped like the array the contract declares. Normalized here, once, so no
	// transport can put `null` on the wire — which a model reads as "unknown"
	// rather than "none free".
	free = []slot{}
	cursor := from.UTC().Truncate(duration)
	if cursor.Before(from.UTC()) {
		cursor = cursor.Add(duration)
	}
	for ; !cursor.Add(duration).After(to.UTC()); cursor = cursor.Add(duration) {
		end := cursor.Add(duration)
		endsAtClose := end.Hour() == businessDayEndHour && end.Minute() == 0 && end.Second() == 0
		if cursor.Hour() < businessDayStartHour ||
			(!endsAtClose && (end.Hour() > businessDayEndHour || end.Hour() == businessDayEndHour && end.Minute() > 0)) ||
			end.Hour() < businessDayStartHour ||
			cursor.Weekday() == time.Saturday || cursor.Weekday() == time.Sunday {
			continue
		}
		candidate := slot{Start: cursor, End: cursor.Add(duration)}
		if overlapsAny(candidate, busy) {
			continue
		}
		// One past the cap, then trimmed: the extra candidate is the evidence
		// that something was actually withheld. Stopping AT the cap could not
		// tell "twenty and no more" from "twenty of many", and only the second
		// is a truncated answer.
		free = append(free, candidate)
		if len(free) > maxProposedSlots {
			return free[:maxProposedSlots], true
		}
	}
	return free, false
}

func overlapsAny(candidate slot, busy []slot) bool {
	for _, b := range busy {
		if candidate.Start.Before(b.End) && b.Start.Before(candidate.End) {
			return true
		}
	}
	return false
}

type BookMeetingInput struct {
	Host           ids.UserID
	Start          time.Time
	End            time.Time
	Subject        string
	AttendeeEmails []string
	Links          []ActivityLinkInput
	// Source names the capture surface ("manual" when empty — the
	// authenticated default). The anonymous page passes public_booking
	// so a stranger's submission never masquerades as hand-entered data.
	Source string
}

// BookMeeting commits one slot: the meeting lands as an activity on the
// linked records' timelines, and a taken slot answers slot_taken
// instead of double-booking the host.
func (s *Store) BookMeeting(ctx context.Context, in BookMeetingInput) (crmcontracts.Activity, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, err
	}
	// Booking writes onto the host's calendar; a caller may commit their
	// OWN slots, and only an unbounded (admin) scope may book on behalf
	// of another host — the spec's calendar_delegate grant (features/04
	// §1) is not yet adopted in this build.
	actor, ok := principal.Actor(ctx)
	if !ok {
		return crmcontracts.Activity{}, apperrors.ErrPermissionDenied
	}
	if in.Host.UUID != actor.UserID && !auth.Unbounded(actor) {
		return crmcontracts.Activity{}, apperrors.ErrPermissionDenied
	}
	if !in.End.After(in.Start) {
		return crmcontracts.Activity{}, errBookingEndNotAfterStart
	}
	// The conflict probe reads only the calendar the caller may write
	// (their own, or any as admin — gated above) and gives the polite
	// answer; the GUARANTEE is the activity_meeting_no_overlap exclusion
	// constraint (0032) — two racing bookings cannot both commit, the
	// loser's 23P01 maps to the same slot_taken below.
	var taken bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM activity
			  WHERE kind = 'meeting' AND archived_at IS NULL AND host_user_id = $1
			    AND occurred_at < $3 AND occurred_at + $4::interval > $2)`,
			in.Host, in.Start, in.End, assumedMeetingDuration.String()).Scan(&taken)
	})
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if taken {
		return crmcontracts.Activity{}, &SlotTakenError{Start: in.Start}
	}

	subject := in.Subject
	if subject == "" {
		subject = "Meeting"
	}
	source := in.Source
	if source == "" {
		source = sourceManual
	}
	occurred := in.Start
	activity, _, err := s.LogActivity(ctx, LogActivityInput{
		Kind:       "meeting",
		Subject:    &subject,
		OccurredAt: &occurred,
		HostUserID: &in.Host,
		Links:      in.Links,
		Source:     source,
	})
	if _, excluded := storekit.ExclusionViolation(err); excluded {
		return crmcontracts.Activity{}, &SlotTakenError{Start: in.Start}
	}
	// Invite delivery rides the deployment's calendar/mail seam; the
	// governed, audited fact is the meeting on the timeline.
	return activity, err
}

// SlotTakenError maps to the contract's 409 slot_taken.
type SlotTakenError struct{ Start time.Time }

func (e *SlotTakenError) Error() string {
	return fmt.Sprintf("the host is already booked around %s", e.Start.Format(time.RFC3339))
}

func (h Handlers) GetAvailability(w http.ResponseWriter, r *http.Request, params crmcontracts.GetAvailabilityParams) {
	actor, ok := principal.Actor(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "availability needs an authenticated caller")
		return
	}
	host := ids.From[ids.UserKind](actor.UserID)
	if params.HostUserId != nil {
		host = ids.From[ids.UserKind](ids.UUID(*params.HostUserId))
	}
	var duration time.Duration
	if params.DurationMinutes != nil {
		duration = time.Duration(*params.DurationMinutes) * time.Minute
	}
	slots, truncated, err := h.store.Availability(r.Context(), host, params.From, params.To, duration)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	// truncated travels on both transports, because the cap is the store's and
	// so is the obligation to admit it (ADR-0055: the two surfaces do not get to
	// disagree about what an answer means).
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"slots": slots, "truncated": truncated})
}

func (h Handlers) BookMeeting(w http.ResponseWriter, r *http.Request, _ crmcontracts.BookMeetingParams) {
	var req crmcontracts.BookMeetingJSONRequestBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	actor, ok := principal.Actor(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "booking needs an authenticated caller")
		return
	}
	in := BookMeetingInput{
		Host:  ids.From[ids.UserKind](actor.UserID),
		Start: req.Start,
		End:   req.End,
	}
	if req.Subject != nil {
		in.Subject = *req.Subject
	}
	if req.AttendeeEmails != nil {
		for _, e := range *req.AttendeeEmails {
			in.AttendeeEmails = append(in.AttendeeEmails, string(e))
		}
	}
	if req.HostUserId != nil {
		in.Host = ids.From[ids.UserKind](ids.UUID(*req.HostUserId))
	}
	for _, l := range req.Links {
		in.Links = append(in.Links, ActivityLinkInput{EntityType: string(l.EntityType), EntityID: ids.UUID(l.EntityId)})
	}

	// The optional CaptureConsent passthrough records the booked subject's
	// consent BEFORE the slot commits, on the same seam the anonymous
	// booking page rides — and with the same stance: a slot_taken 409
	// after the grant leaves the grant standing, because the subject DID
	// give it. Recording it is mandatory once the field is present; a
	// process role composed without the consent seam refuses rather than
	// booking with an unrecorded consent.
	if req.Consent != nil {
		if h.publicConsent == nil {
			httperr.Write(w, r, apperrors.ErrPermissionDenied)
			return
		}
		if req.Consent.PolicyVersion == "" {
			httperr.Write(w, r, httperr.Validation("consent.policy_version", "required", "the consent wording version shown to the subject is required"))
			return
		}
		personID, ok := consentSubjectLink(w, r, in.Links)
		if !ok {
			return
		}
		purposeID := ids.UUID(req.Consent.PurposeId)
		if err := h.publicConsent.ValidatePurpose(r.Context(), purposeID); err != nil {
			writeStoreErr(w, r, err)
			return
		}
		if err := h.publicConsent.CaptureBookingConsent(r.Context(), personID, BookingConsent{
			PurposeID:        purposeID,
			PolicyVersion:    req.Consent.PolicyVersion,
			Wording:          req.Consent.Wording,
			DoubleOptInToken: req.Consent.DoubleOptInToken,
		}); err != nil {
			writeStoreErr(w, r, err)
			return
		}
	}

	booked, err := h.store.BookMeeting(r.Context(), in)
	if err != nil {
		var slotTaken *SlotTakenError
		if errors.As(err, &slotTaken) {
			httperr.Write(w, r, httperr.Duplicate("slot_taken", ""))
			return
		}
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, booked)
}

// consentSubjectLink resolves which linked record the CaptureConsent
// passthrough attaches to: exactly one linked person. The ConsentCapturer
// seam is person-keyed (the compose adapter records against a person), so
// a lead-only booking cannot carry consent through this endpoint yet —
// that refuses loudly rather than accepting a consent it would not
// record. Returns false after writing the response.
func consentSubjectLink(w http.ResponseWriter, r *http.Request, links []ActivityLinkInput) (ids.UUID, bool) {
	var persons []ids.UUID
	for _, l := range links {
		if l.EntityType == "person" {
			persons = append(persons, l.EntityID)
		}
	}
	switch len(persons) {
	case 1:
		return persons[0], true
	case 0:
		httperr.Write(w, r, httperr.Validation("consent", "subject_required",
			"recording consent with a booking requires a linked person to attach it to"))
		return ids.UUID{}, false
	default:
		httperr.Write(w, r, httperr.Validation("consent", "subject_ambiguous",
			"recording consent with a booking requires exactly one linked person"))
		return ids.UUID{}, false
	}
}
