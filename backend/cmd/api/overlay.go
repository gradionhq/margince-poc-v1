// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// overlayBackfillLimitEnv is the variable read below, named so the role can
// declare it without spelling the string a second time.
const overlayBackfillLimitEnv = "MARGINCE_OVERLAY_BACKFILL_LIMIT"

// overlayBackfillLimitFromEnv reads overlayBackfillLimitEnv: unset
// → 0 (the overlay initial backfill runs uncapped); a non-negative integer
// → that per-object-class cap (dev/demo, so a large portal's initial load
// stays bounded); anything else is a boot error, never a silent default.
func overlayBackfillLimitFromEnv() (int, error) {
	v := config.FromOS(overlayBackfillLimitEnv)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid MARGINCE_OVERLAY_BACKFILL_LIMIT %q: want a non-negative integer", v)
	}
	return n, nil
}
