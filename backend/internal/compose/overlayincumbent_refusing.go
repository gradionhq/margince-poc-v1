// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// In the integration build, compose binds an incumbent that refuses every call
// instead of one that reaches HubSpot.
//
// The suites were reaching it for real. WithKeyvault builds the overlay Service
// with the live factory, and Connect resolves the incumbent's portal id and its
// owners directory through it — both best-effort, so the vendor's 401 came back,
// was logged as a warning, and the suite went green while a merge gate depended
// on a third party being up (#1996). Measured at 32 connects x 2 round trips per
// overlay run, ~24s of a 34s package.
//
// Refusing rather than answering, because a fixture that answers plausibly is
// the thing this seam already has: a suite that wants connect-path behaviour
// passes its own incumbent through WithOverlayIncumbentResolver, and one that
// does not should get a named refusal it can read rather than a vendor status
// code that means nothing here.
//
// Both call sites tolerate the error by design — overlay/connection.go's
// fetchPortalID stores NULL and seedUserMapOnConnect logs and returns — so this
// changes what the suites SAY, not what they do.

import (
	"context"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
)

// errNoLiveIncumbent is what every method below returns. It names the
// substitution to make, because the reader hitting it is running a suite that
// unexpectedly depends on incumbent behaviour and needs to know there is a seam.
//
// It WRAPS hubspot.ErrUnreachable, and that is load-bearing rather than
// decorative. jobs_overlay.go's isConnectionLevelIncumbentError is what decides
// whether a reconcile sweep aborts and backs the connection off or logs one
// object class and carries on; an error it does not recognise takes the second
// branch. So a bare sentinel here would let the fleet worker sweep a connection
// where EVERY incumbent call refused, treat each refusal as one class's data
// defect, and finish by calling RecordSweepSuccess — a green sweep that did
// nothing, which is the exact false-green this build is supposed to remove.
// "Could not reach the incumbent" is also simply what happened.
var errNoLiveIncumbent = fmt.Errorf(
	"compose: the integration build binds no live incumbent — this suite reached one; "+
		"pass your own through WithOverlayIncumbentResolver (applied AFTER WithKeyvault) "+
		"if it needs incumbent behaviour: %w", hubspot.ErrUnreachable)

// liveIncumbentFactory is the integration half of the split — see
// overlayincumbent_live.go for the production one.
//
//nolint:ireturn // returns the overlay.Incumbent seam by design, matching the production half's signature.
func liveIncumbentFactory(_, _ string) overlay.Incumbent {
	return refusingIncumbent{}
}

// refusingIncumbent implements overlay.Incumbent by declining, with the same
// error from every method. It reports HubSpot's name so a caller that only
// switches on the incumbent's identity behaves as it does in production.
type refusingIncumbent struct{}

func (refusingIncumbent) Name() string { return incumbentHubSpot }

func (refusingIncumbent) Backfill(context.Context, string, string) (overlay.Page, error) {
	return overlay.Page{}, errNoLiveIncumbent
}

func (refusingIncumbent) Modified(context.Context, string, time.Time, string) (overlay.Page, error) {
	return overlay.Page{}, errNoLiveIncumbent
}

func (refusingIncumbent) Deletions(context.Context, string, time.Time, string) (overlay.DeletionPage, error) {
	return overlay.DeletionPage{}, errNoLiveIncumbent
}

func (refusingIncumbent) Get(context.Context, string, string) (overlay.Record, error) {
	return overlay.Record{}, errNoLiveIncumbent
}

func (refusingIncumbent) Associations(context.Context, string, string, string) ([]overlay.Assoc, error) {
	return nil, errNoLiveIncumbent
}

func (refusingIncumbent) OwnerEmail(context.Context, string) (string, error) {
	return "", errNoLiveIncumbent
}

func (refusingIncumbent) Owners(context.Context) ([]overlay.OwnerRef, error) {
	return nil, errNoLiveIncumbent
}

func (refusingIncumbent) Create(context.Context, string, map[string]any) (overlay.WriteResult, error) {
	return overlay.WriteResult{}, errNoLiveIncumbent
}

func (refusingIncumbent) Update(context.Context, string, string, map[string]any, time.Time) (overlay.WriteResult, error) {
	return overlay.WriteResult{}, errNoLiveIncumbent
}

func (refusingIncumbent) Archive(context.Context, string, string, time.Time) error {
	return errNoLiveIncumbent
}

// AccountID answers fetchPortalID's optional incumbentAccountReader. Implemented
// rather than omitted: leaving it off takes the "adapter exposes no account
// accessor" path, which is a legitimate production shape and would hide the fact
// that a suite asked.
func (refusingIncumbent) AccountID(context.Context) (string, error) {
	return "", errNoLiveIncumbent
}
