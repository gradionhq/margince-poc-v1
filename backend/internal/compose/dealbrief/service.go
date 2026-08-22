// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package dealbrief writes the deal's standing brief: where it stands, what
// happened last and what is booked next, what is still owed, and what the
// buyer said in the Deal Room. Deterministic — every sentence restates a
// record the caller can open and cites it. The facts are gathered through
// the modules' own gated reads, so a record the caller cannot see never
// reaches a sentence; the fold over them is a pure function a test drives
// without a database.
package dealbrief

import (
	"context"
	"errors"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/dealrooms"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// timelineWindow bounds how much of the timeline the brief reads. The
// timeline is ordered by occurred_at DESC, so the window holds the nearest
// scheduled rows first and then the most recent past ones; a deal with more
// booked future rows than the window would show no "last activity", which
// the activity section says rather than guessing.
const timelineWindow = 25

// Service gathers the facts from the three modules that hold them.
type Service struct {
	deals      *deals.Store
	activities *activities.Store
	rooms      *dealrooms.Store
	now        func() time.Time
}

// NewService binds the reads. Each store carries its own gates.
func NewService(d *deals.Store, a *activities.Store, r *dealrooms.Store, now func() time.Time) *Service {
	return &Service{deals: d, activities: a, rooms: r, now: now}
}

// facts are the inputs the sentences fold.
type facts struct {
	deal      crmcontracts.Deal
	health    *deals.DealHealth
	timeline  []crmcontracts.Activity
	openTasks []activities.OpenTask
	// moreTasks says the open-task read was cut at its window, so the count
	// is a floor rather than the number.
	moreTasks bool
	room      *crmcontracts.DealRoom
	threads   []crmcontracts.DealRoomThread
	decisions []crmcontracts.DealRoomDecision
	now       time.Time
}

// Get assembles the brief for one deal.
func (s *Service) Get(ctx context.Context, dealID ids.DealID) (crmcontracts.DealBrief, error) {
	f, err := s.gather(ctx, dealID)
	if err != nil {
		return crmcontracts.DealBrief{}, err
	}
	return crmcontracts.DealBrief{
		DealId:      openapi_types.UUID(dealID.UUID),
		GeneratedAt: f.now,
		GeneratedBy: crmcontracts.Deterministic,
		Sections:    write(f),
	}, nil
}

func (s *Service) gather(ctx context.Context, dealID ids.DealID) (facts, error) {
	f := facts{now: s.now()}
	deal, err := s.deals.GetDeal(ctx, dealID, storekit.LiveOnly)
	if err != nil {
		return facts{}, err
	}
	f.deal = deal
	health, err := s.deals.DealHealth(ctx, dealID, f.now)
	if err != nil {
		return facts{}, fmt.Errorf("deal brief: reading the deal's health: %w", err)
	}
	f.health = &health
	entityType := "deal"
	limit := timelineWindow
	timeline, _, err := s.activities.ListActivities(ctx, activities.ListActivitiesInput{
		EntityType: &entityType, EntityID: &dealID.UUID, Limit: &limit,
	})
	if err != nil {
		return facts{}, fmt.Errorf("deal brief: reading the deal's timeline: %w", err)
	}
	f.timeline = timeline
	open, more, err := s.activities.ListOpenTasks(ctx, activities.ListOpenTasksInput{
		EntityType: &entityType, EntityID: &dealID.UUID, Limit: timelineWindow,
	})
	if err != nil {
		return facts{}, fmt.Errorf("deal brief: reading the deal's open tasks: %w", err)
	}
	f.openTasks, f.moreTasks = open, more
	if err := s.gatherRoom(ctx, dealID, &f); err != nil {
		return facts{}, err
	}
	return f, nil
}

// gatherRoom reads the deal's room, when there is one, with its conversation
// and decisions. A caller without the deal_room grant reads no room; the
// brief then simply has no room section, which is the truth for them.
func (s *Service) gatherRoom(ctx context.Context, dealID ids.DealID, f *facts) error {
	rooms, _, err := s.rooms.ListRooms(ctx, dealrooms.ListRoomsInput{DealID: &dealID})
	if err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return nil
		}
		return fmt.Errorf("deal brief: reading the deal's room: %w", err)
	}
	if len(rooms) == 0 {
		return nil
	}
	room := rooms[0]
	f.room = &room
	roomID := ids.From[ids.DealRoomKind](ids.UUID(room.Id))
	threads, err := s.rooms.ListThreads(ctx, roomID, nil)
	if err != nil {
		return fmt.Errorf("deal brief: reading the room's conversation: %w", err)
	}
	f.threads = threads
	decisions, err := s.rooms.ListDecisions(ctx, roomID)
	if err != nil {
		return fmt.Errorf("deal brief: reading the room's decisions: %w", err)
	}
	f.decisions = decisions
	return nil
}
