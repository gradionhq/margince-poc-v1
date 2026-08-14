// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The outbound half of the extension error seam: a refusal a UNIT raised, on
// its way back to that unit's own HTTP caller.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// unitRefusal maps the four published refusal classes onto the product's own
// wire vocabulary, and leaves everything else exactly as it found it.
//
// It is portRefusal's MIRROR, and the direction is why it is a second function
// rather than an inverse table. Inbound, a core error is stripped to its class
// because a unit is other people's code. Outbound, the unit is answering its
// OWN caller about its OWN contract, so the sentence travels: a member told
// "connecting needs the personal access token from your Dispact profile" can
// act on it.
//
// Without this every one of the four reaches httperr unclassified, because
// they are the extension surface's sentinels and httperr's table is the core's
// fixed §0 registry — which no extension may extend. A caller who mistyped a
// base URL was answered `500 internal`, sending them to look for an outage
// instead of at their own input, and the operator's log grew an "unhandled
// error" line for a request nothing was wrong with.
//
// The mapped classes are the core's own codes rather than extension-specific
// ones, which is the same rule the route states about shape: a unit's refusal
// reads on the wire exactly like the refusal of the core route beside it.
func unitRefusal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, extension.ErrInvalid):
		// 422 with no field entry: the class says the request is malformed and
		// the unit's sentence says how, but WHICH declared argument is at fault
		// is a fact only the unit holds and this seam cannot invent one.
		return &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "validation_error",
			Detail: unitSentence(err, extension.ErrInvalid),
		}
	case errors.Is(err, extension.ErrForbidden):
		return fmt.Errorf("%w: %s", apperrors.ErrPermissionDenied, unitSentence(err, extension.ErrForbidden))
	case errors.Is(err, extension.ErrNotFound):
		return fmt.Errorf("%w: %s", apperrors.ErrNotFound, unitSentence(err, extension.ErrNotFound))
	case errors.Is(err, extension.ErrConflict):
		return fmt.Errorf("%w: %s", apperrors.ErrConflict, unitSentence(err, extension.ErrConflict))
	}
	return err
}

// unitSentence is what the unit added to a class, without the class's own text
// repeated in front of it.
//
// A unit writes `fmt.Errorf("%w: <what to do about it>", extension.ErrInvalid)`,
// so the raw string carries both halves and the mapped error would carry a
// third: "unprocessable: extension: the request is malformed: <sentence>". The
// class is already on the wire as the status and the code, so the detail owes
// the caller only the part they cannot read there. A unit that wrapped nothing
// falls back to the class's own text, which is still a true sentence.
func unitSentence(err error, class error) string {
	sentence := strings.TrimPrefix(err.Error(), class.Error()+": ")
	if sentence == "" {
		return class.Error()
	}
	return sentence
}
