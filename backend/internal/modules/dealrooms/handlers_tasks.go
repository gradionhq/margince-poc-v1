// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func taskPathID(id openapi_types.UUID) ids.DealRoomTaskID {
	return ids.DealRoomTaskID{UUID: ids.UUID(id)}
}

// ListDealRoomTasks returns the shared to-do list.
func (h Handlers) ListDealRoomTasks(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ListDealRoomTasksParams) {
	tasks, page, err := h.store.ListTasks(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomTaskListResponse{
		Data: tasks,
		Page: pageInfo(page),
	})
}

// CreateDealRoomTask adds an item to the shared to-do list.
func (h Handlers) CreateDealRoomTask(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.CreateDealRoomTaskRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := createTaskInput(req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	task, err := h.store.CreateTask(r.Context(), pathID(id), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, task)
}

// UpdateDealRoomTask rewords, reassigns, reorders or ticks off one item.
func (h Handlers) UpdateDealRoomTask(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, taskID openapi_types.UUID, _ crmcontracts.UpdateDealRoomTaskParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateDealRoomTaskRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := updateTaskInput(req, ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	task, err := h.store.UpdateTask(r.Context(), pathID(id), taskPathID(taskID), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, task)
}

// ArchiveDealRoomTask takes an item off the list, keeping it attributed.
func (h Handlers) ArchiveDealRoomTask(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, taskID openapi_types.UUID, _ crmcontracts.ArchiveDealRoomTaskParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	task, err := h.store.ArchiveTask(r.Context(), pathID(id), taskPathID(taskID), ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, task)
}
