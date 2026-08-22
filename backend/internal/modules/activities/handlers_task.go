// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// CreateTask is the one door that knows a task is an activity of kind `task`.
// It folds the task's shape into the activity write and goes through exactly
// the store path LogActivity does, so a task created here and one logged
// there are the same row with the same gates.
func (h Handlers) CreateTask(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateTaskParams) {
	var req crmcontracts.CreateTaskRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// The activity writer admits a blank subject (a captured mail may carry
	// none); a task is nothing but its subject, so this door refuses one.
	if strings.TrimSpace(req.Subject) == "" {
		writeStoreErr(w, r, &RequiredFieldError{Field: fieldSubject})
		return
	}
	activityReq, err := activityOfTask(req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	in, err := LogActivityInputFrom(activityReq)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	activity, _, err := h.store.LogActivity(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/activities/"+activity.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, activity)
}

// activityOfTask is the fold: the task request as the activity request the
// writer understands. Kept as a function rather than inlined so a field the
// task grows is added here once, where its mapping is visible.
func activityOfTask(req crmcontracts.CreateTaskRequest) (crmcontracts.CreateActivityRequest, error) {
	subject := req.Subject
	out := crmcontracts.CreateActivityRequest{
		Kind:       crmcontracts.CreateActivityRequestKindTask,
		Subject:    &subject,
		Body:       req.Body,
		DueAt:      req.DueAt,
		AssigneeId: req.AssigneeId,
		Source:     req.Source,
	}
	if req.Links != nil {
		// The two link shapes are generated separately and identical on the
		// wire; a round trip through JSON is the one spelling of the copy that
		// cannot drift from either generated struct.
		raw, err := json.Marshal(req.Links)
		if err != nil {
			return out, fmt.Errorf("encode task links: %w", err)
		}
		if err := json.Unmarshal(raw, &out.Links); err != nil {
			return out, fmt.Errorf("decode task links as activity links: %w", err)
		}
	}
	return out, nil
}
