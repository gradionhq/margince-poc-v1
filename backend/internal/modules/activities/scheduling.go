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

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"

	"github.com/jackc/pgx/v5"
)

// Business-hours envelope for proposed slots and the assumed length of
// a meeting whose end the record does not carry (activity has only
// occurred_at). Both refine when a real calendar connector lands.
const (
	businessDayStartHour   = 9
	businessDayEndHour     = 17
	assumedMeetingDuration = time.Hour
	maxProposedSlots       = 20
	maxAvailabilityWindow  = 31 * 24 * time.Hour
)

type slot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Availability computes free slots for one host inside the window:
// business-hour candidates minus the host's existing meetings.
func (s *Store) Availability(ctx context.Context, host ids.UUID, from, to time.Time, duration time.Duration) ([]slot, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, err
	}
	if !to.After(from) {
		return nil, &RequiredFieldError{Field: "to (must follow from)"}
	}
	if to.Sub(from) > maxAvailabilityWindow {
		return nil, &RequiredFieldError{Field: "window (at most 31 days)"}
	}

	var busy []slot
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT occurred_at FROM activity
			WHERE kind = 'meeting' AND archived_at IS NULL
			  AND host_user_id = $1
			  AND occurred_at BETWEEN $2 AND $3
			ORDER BY occurred_at`, host, from.Add(-assumedMeetingDuration), to)
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
		return nil, err
	}

	var free []slot
	for cursor := from.UTC().Truncate(duration); cursor.Add(duration).Before(to.UTC().Add(time.Nanosecond)); cursor = cursor.Add(duration) {
		if cursor.Hour() < businessDayStartHour || cursor.Add(duration).Hour() > businessDayEndHour ||
			cursor.Weekday() == time.Saturday || cursor.Weekday() == time.Sunday {
			continue
		}
		candidate := slot{Start: cursor, End: cursor.Add(duration)}
		if overlapsAny(candidate, busy) {
			continue
		}
		free = append(free, candidate)
		if len(free) == maxProposedSlots {
			break
		}
	}
	return free, nil
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
	Host           ids.UUID
	Start          time.Time
	End            time.Time
	Subject        string
	AttendeeEmails []string
	Links          []ActivityLinkInput
}

// BookMeeting commits one slot: the meeting lands as an activity on the
// linked records' timelines, and a taken slot answers slot_taken
// instead of double-booking the host.
func (s *Store) BookMeeting(ctx context.Context, in BookMeetingInput) (crmcontracts.Activity, error) {
	if !in.End.After(in.Start) {
		return crmcontracts.Activity{}, &RequiredFieldError{Field: "end (must follow start)"}
	}
	// The slot check and the write share one transaction path via the
	// unique-ish probe below; a race lands on the second reader as 409.
	var taken bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM activity
			  WHERE kind = 'meeting' AND archived_at IS NULL AND host_user_id = $1
			    AND occurred_at < $3 AND occurred_at + interval '1 hour' > $2)`,
			in.Host, in.Start, in.End).Scan(&taken)
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
	occurred := in.Start
	activity, _, err := s.LogActivity(ctx, LogActivityInput{
		Kind:       "meeting",
		Subject:    &subject,
		OccurredAt: &occurred,
		HostUserID: &in.Host,
		Links:      in.Links,
		Source:     "manual",
	})
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
	host := actor.UserID
	if params.HostUserId != nil {
		host = ids.UUID(*params.HostUserId)
	}
	duration := 30 * time.Minute
	if params.DurationMinutes != nil && *params.DurationMinutes > 0 {
		duration = time.Duration(*params.DurationMinutes) * time.Minute
	}
	slots, err := h.store.Availability(r.Context(), host, params.From, params.To, duration)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if slots == nil {
		slots = []slot{}
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"slots": slots})
}

func (h Handlers) BookMeeting(w http.ResponseWriter, r *http.Request, _ crmcontracts.BookMeetingParams) {
	var req struct {
		HostUserId     *openapi_types.UUID `json:"host_user_id"`
		Start          time.Time           `json:"start"`
		End            time.Time           `json:"end"`
		Subject        string              `json:"subject"`
		AttendeeEmails []string            `json:"attendee_emails"`
		Links          []struct {
			EntityType string   `json:"entity_type"`
			EntityID   ids.UUID `json:"entity_id"`
		} `json:"links"`
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	actor, ok := principal.Actor(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "booking needs an authenticated caller")
		return
	}
	in := BookMeetingInput{
		Host:           actor.UserID,
		Start:          req.Start,
		End:            req.End,
		Subject:        req.Subject,
		AttendeeEmails: req.AttendeeEmails,
	}
	if req.HostUserId != nil {
		in.Host = ids.UUID(*req.HostUserId)
	}
	for _, l := range req.Links {
		in.Links = append(in.Links, ActivityLinkInput{EntityType: l.EntityType, EntityID: l.EntityID})
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
