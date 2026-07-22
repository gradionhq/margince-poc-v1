// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extensionCompositionObserved is the system_log action carrying the
// composed extension set; one row per observed CHANGE, so install,
// upgrade and removal — which all happen in source (ADR-0069 §5) — leave
// an attributable trail even though no request performed them.
const extensionCompositionObserved = "extension.composition_observed"

// observedExtension is one unit of the recorded set. It gains the
// manifest digest when the governance slice embeds digests into the
// composed binary; until then name+version identify the unit in the log
// (the version string carries no authority, ADR-0069 §7).
type observedExtension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ObserveExtensionInventory records the composed extension set in
// system_log when it differs from the last observation. Pre-bootstrap
// there is no workspace to record against — the observation is skipped
// and the first boot after bootstrap records it.
func ObserveExtensionInventory(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, exts []extension.Extension) error {
	wsID, err := identity.NewService(pool).InstallationWorkspace(ctx)
	if errors.Is(err, identity.ErrNotBootstrapped) {
		if len(exts) > 0 {
			log.Info("extension inventory not recorded: installation not bootstrapped yet")
		}
		return nil
	}
	if err != nil {
		return err
	}

	current := make([]observedExtension, 0, len(exts))
	for _, e := range exts {
		current = append(current, observedExtension{Name: e.Name, Version: e.Version})
	}
	slices.SortFunc(current, func(a, b observedExtension) int {
		return strings.Compare(a.Name, b.Name)
	})

	ctx = principal.WithWorkspaceID(ctx, wsID.UUID)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:extension-inventory"})
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())

	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		last, err := lastObservedExtensions(ctx, tx)
		if err != nil {
			return err
		}
		if slices.Equal(last, current) {
			return nil
		}
		_, err = storekit.LogSystem(ctx, tx, extensionCompositionObserved, map[string]any{
			"extensions": current,
		})
		if err != nil {
			return err
		}
		log.Info("extension composition changed", "extensions", len(current))
		return nil
	})
}

// lastObservedExtensions reads the most recent observation; none yet
// reads as the empty set, so a vanilla installation never logs and the
// first enabled extension does.
func lastObservedExtensions(ctx context.Context, tx pgx.Tx) ([]observedExtension, error) {
	var detail []byte
	err := tx.QueryRow(ctx,
		`SELECT detail->'extensions' FROM system_log WHERE action = $1 ORDER BY id DESC LIMIT 1`,
		extensionCompositionObserved).Scan(&detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []observedExtension
	if err := json.Unmarshal(detail, &out); err != nil {
		return nil, fmt.Errorf("compose: last extension observation unreadable: %w", err)
	}
	return out, nil
}
