// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// The sides a to-do may be owed by, spelled here because the contract carries
// them as a plain string — an inline enum would generate package-scope Go
// constants named Seller and Buyer in the shared contracts package and silently
// rename any other schema declaring the same values.
const (
	sideSeller = "seller"
	sideBuyer  = "buyer"
)

// titleLimit bounds a to-do's wording. Unbounded, it is a line the other side
// has to read in a list they did not write.
const titleLimit = 255

// createTaskInput validates and normalizes a new to-do.
func createTaskInput(req crmcontracts.CreateDealRoomTaskRequest) (CreateTaskInput, error) {
	if err := provenance.Refuse("source", req.Source); err != nil {
		return CreateTaskInput{}, err
	}
	title, err := cleanTaskTitle(req.Title)
	if err != nil {
		return CreateTaskInput{}, err
	}
	if err := refuseUnknownSide(req.Side); err != nil {
		return CreateTaskInput{}, err
	}
	in := CreateTaskInput{Side: req.Side, Title: title, Source: req.Source}
	if req.Position != nil {
		in.Position = *req.Position
	}
	return in, nil
}

// updateTaskInput validates a patch. Every field is optional, so each is checked
// only when the caller sent it — an omitted field means "leave it alone", which
// is not the same as sending an empty one.
func updateTaskInput(req crmcontracts.UpdateDealRoomTaskRequest, ifVersion *int64) (UpdateTaskInput, error) {
	in := UpdateTaskInput{Position: req.Position, Done: req.Done, IfVersion: ifVersion}
	if req.Side != nil {
		if err := refuseUnknownSide(*req.Side); err != nil {
			return UpdateTaskInput{}, err
		}
		in.Side = req.Side
	}
	if req.Title != nil {
		title, err := cleanTaskTitle(*req.Title)
		if err != nil {
			return UpdateTaskInput{}, err
		}
		in.Title = &title
	}
	return in, nil
}

// cleanTaskTitle trims the wording and refuses what cannot stand as an item.
func cleanTaskTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", &fieldError{field: columnTitle, code: "required", msg: "title is required"}
	}
	if len([]rune(trimmed)) > titleLimit {
		return "", &fieldError{
			field: columnTitle,
			code:  "too_long",
			msg:   "title is longer than 255 characters",
		}
	}
	return trimmed, nil
}

// refuseUnknownSide names the closed set rather than letting the schema CHECK
// answer: a constraint violation surfaces as a 500 with a table name in it, and
// the caller learns nothing about which values are legal.
func refuseUnknownSide(side string) error {
	switch side {
	case sideSeller, sideBuyer:
		return nil
	}
	return &fieldError{
		field: "side",
		code:  "unknown_side",
		msg:   "side must be seller or buyer",
	}
}
